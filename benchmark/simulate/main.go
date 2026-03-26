package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhope/hedge"
	"github.com/cristalhq/hedgedhttp"
)

const (
	numRequests  = 50_000
	concurrency  = 20
	baseMeanMs   = 5.0
	baseStddevMs = 2.0
	stragglerP   = 0.05
	stragglerMul = 10.0
)

var (
	muLn    float64
	sigmaLn float64
)

func init() {
	const meanNs = baseMeanMs * 1e6
	const stddevNs = baseStddevMs * 1e6
	cv2 := (stddevNs / meanNs) * (stddevNs / meanNs)
	sigmaLn = math.Sqrt(math.Log(1 + cv2))
	muLn = math.Log(meanNs) - sigmaLn*sigmaLn/2
}

func sampleLatency() time.Duration {
	base := time.Duration(math.Exp(muLn + sigmaLn*rand.NormFloat64()))
	if rand.Float64() < stragglerP {
		return time.Duration(float64(base) * stragglerMul)
	}
	return base
}

type simResult struct {
	latencies []time.Duration
	overhead  float64
}

func runSim(transport http.RoundTripper, serverHits *atomic.Int64, url string) simResult {
	before := serverHits.Load()

	latencies := make([]time.Duration, numRequests)
	work := make(chan int, numRequests)
	for i := 0; i < numRequests; i++ {
		work <- i
	}
	close(work)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for i := range work {
				start := time.Now()
				req, _ := http.NewRequest("GET", url, nil)
				resp, err := transport.RoundTrip(req)
				if err == nil {
					resp.Body.Close()
				}
				latencies[i] = time.Since(start)
			}
		}()
	}
	wg.Wait()

	hits := serverHits.Load() - before
	overhead := float64(hits-numRequests) / float64(numRequests) * 100

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return simResult{latencies: latencies, overhead: overhead}
}

func pct(sorted []time.Duration, p float64) time.Duration {
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func fmtDur(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}

func main() {
	var serverHits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		d := sampleLatency()
		select {
		case <-time.After(d):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// Estimate RPS: concurrency workers each taking avgLatency per request.
	avgLatencyMs := baseMeanMs*(1-stragglerP) + baseMeanMs*stragglerMul*stragglerP
	estimatedRPS := float64(concurrency) * 1000.0 / avgLatencyMs

	var adaptiveStats *hedge.Stats
	adaptiveTransport := hedge.New(http.DefaultTransport,
		hedge.WithEstimatedRPS(estimatedRPS),
		hedge.WithBudgetPercent(10), // p90 fires on ~10% of requests; match budget to threshold rate
		hedge.WithStats(&adaptiveStats),
	)
	fmt.Printf("warming up adaptive transport (estimatedRPS=%.0f)...\n", estimatedRPS)
	for i := 0; i < 25; i++ {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := adaptiveTransport.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
	}

	static10, err := hedgedhttp.NewRoundTripper(10*time.Millisecond, 2, http.DefaultTransport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "static10: %v\n", err)
		os.Exit(1)
	}
	static50, err := hedgedhttp.NewRoundTripper(50*time.Millisecond, 2, http.DefaultTransport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "static50: %v\n", err)
		os.Exit(1)
	}

	configs := []struct {
		name      string
		transport http.RoundTripper
	}{
		{"No hedging", http.DefaultTransport},
		{"Static 10ms", static10},
		{"Static 50ms", static50},
		{"Adaptive (hedge)", adaptiveTransport},
	}

	type row struct {
		name string
		r    simResult
	}
	rows := make([]row, 0, len(configs))

	for _, c := range configs {
		fmt.Printf("running %-20s (%d requests, concurrency=%d)...\n", c.name, numRequests, concurrency)
		serverHits.Store(0)
		r := runSim(c.transport, &serverHits, srv.URL)
		rows = append(rows, row{c.name, r})
	}

	u, _ := url.Parse(srv.URL)
	total := adaptiveStats.TotalRequests.Load()
	hedged := adaptiveStats.HedgedRequests.Load()
	exhausted := adaptiveStats.BudgetExhausted.Load()
	fmt.Printf("\nadaptive debug (host=%s):\n", u.Host)
	fmt.Printf("  sketch p90 threshold : %v\n", hedge.LatencyEstimate(adaptiveTransport, u.Host, 0.90))
	fmt.Printf("  hedged / total       : %d / %d (%.1f%%)\n", hedged, total, float64(hedged)/float64(total)*100)
	fmt.Printf("  budget exhausted     : %d\n", exhausted)

	fmt.Printf("\n  %-18s %8s %8s %8s %9s %9s %9s\n",
		"Configuration", "p50", "p90", "p95", "p99", "p999", "Overhead")
	fmt.Printf("  %-18s %8s %8s %8s %9s %9s %9s\n",
		"------------------", "--------", "--------", "--------", "---------", "---------", "---------")
	for _, r := range rows {
		l := r.r.latencies
		fmt.Printf("  %-18s %8s %8s %8s %9s %9s %8.1f%%\n",
			r.name,
			fmtDur(pct(l, 0.50)),
			fmtDur(pct(l, 0.90)),
			fmtDur(pct(l, 0.95)),
			fmtDur(pct(l, 0.99)),
			fmtDur(pct(l, 0.999)),
			r.r.overhead,
		)
	}

	f, err := os.Create("../results.csv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create csv: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	_ = w.Write([]string{"configuration", "p50", "p90", "p95", "p99", "p999", "overhead_pct"})
	for _, r := range rows {
		l := r.r.latencies
		_ = w.Write([]string{
			r.name,
			fmtDur(pct(l, 0.50)),
			fmtDur(pct(l, 0.90)),
			fmtDur(pct(l, 0.95)),
			fmtDur(pct(l, 0.99)),
			fmtDur(pct(l, 0.999)),
			fmt.Sprintf("%.1f", r.r.overhead),
		})
	}
	w.Flush()
	fmt.Println("\nresults written to benchmark/results.csv")
}
