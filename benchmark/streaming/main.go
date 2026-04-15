// Benchmark: TTFB vs TTFT measurement for streaming LLM workloads.
//
// LLM inference servers commonly flush the HTTP 200 OK status line before
// the first token is ready. In that pattern, time-to-first-header (TTFB) is
// near-zero for every request while time-to-first-body-byte (TTFT) captures
// the real inference latency. This benchmark shows what each measurement mode
// records in the DDSketch and how the resulting hedge calibration differs.
//
// Two server models:
//
//	streaming – 200 OK flushed immediately; body (first token) written after
//	            the TTFT delay. Models an inference server that commits to a
//	            response before token generation is complete.
//
//	gateway   – full response (headers + body) delayed by TTFT. Models an
//	            LLM proxy that buffers until the first token is ready, so
//	            TTFH ≈ TTFT and the hedge timer fires while RoundTrip blocks.
//
// Three measurement / hedging configurations run in each part:
//
//	TTFB transport    – records sketch sample at header receipt (old behaviour).
//	                    Near-zero observations on the streaming server.
//
//	TTFT transport    – hedge.New with body-read trigger (issue #10 fix).
//	                    Records first-body-byte time = actual inference cost.
//
//	TTFB hedge (1ms)  – static 1 ms hedge delay: what a transport calibrated
//	                    from a near-zero TTFB sketch would use. Fires a
//	                    redundant request on virtually every call.
//
//	TTFT hedge (p80)  – hedge.New, learned p80 as threshold. p80 sits at the
//	                    ceiling of the cache-hit distribution so the hedge fires
//	                    only when TTFT exceeds the typical hit latency.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhope/hedge"
	"github.com/bhope/hedge/sketch"
)

// ── simulation constants ────────────────────────────────────────────────────

const (
	numRequests = 5_000
	warmupReqs  = 200
	concurrency = 20

	// TTFT distribution – two modes: cache hit (fast) and cache miss (slow).
	cacheHitMeanMs  = 15.0
	cacheHitStdMs   = 3.0
	cacheMissMeanMs = 200.0
	cacheMissStdMs  = 25.0
	cacheMissRate   = 0.20 // fraction of requests that are cache misses

	// hedgePercentile is the sketch quantile used by the adaptive transport.
	// p80 sits at the ceiling of the cache-hit distribution so the hedge fires
	// for all cache misses but skips most cache hits.
	hedgePercentile = 0.80
)

// ── log-normal sampling (same style as simulate/main.go) ───────────────────

var (
	hitMu, hitSigma   float64
	missMu, missSigma float64
)

func init() {
	hitMu, hitSigma = lognParams(cacheHitMeanMs*1e6, cacheHitStdMs*1e6)
	missMu, missSigma = lognParams(cacheMissMeanMs*1e6, cacheMissStdMs*1e6)
}

func lognParams(meanNs, stdNs float64) (float64, float64) {
	cv2 := (stdNs / meanNs) * (stdNs / meanNs)
	sigma := math.Sqrt(math.Log(1 + cv2))
	mu := math.Log(meanNs) - sigma*sigma/2
	return mu, sigma
}

// sampleTTFT draws a first-token latency from the configured distribution.
func sampleTTFT() time.Duration {
	if rand.Float64() < cacheMissRate {
		return time.Duration(math.Exp(missMu + missSigma*rand.NormFloat64()))
	}
	return time.Duration(math.Exp(hitMu + hitSigma*rand.NormFloat64()))
}

// ── test servers ────────────────────────────────────────────────────────────

// newStreamingServer flushes 200 OK headers immediately then writes the first
// body byte only after the TTFT delay. This separates header arrival from
// first-token time and makes TTFB measurement produce near-zero observations.
func newStreamingServer(hits *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-time.After(sampleTTFT()):
			w.Write([]byte("tok")) //nolint:errcheck
		case <-r.Context().Done():
		}
	}))
}

// newGatewayServer delays the full response (headers + body) by TTFT. Models
// an LLM gateway that buffers until the first token is available, so TTFH ≈
// TTFT. The hedge timer fires while RoundTrip blocks waiting for headers.
func newGatewayServer(hits *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(sampleTTFT()):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("tok")) //nolint:errcheck
	}))
}

// ── transports ───────────────────────────────────────────────────────────────

// ttfbTransport records a sketch sample at header-receipt time — the instant
// RoundTrip returns — matching the pre-fix behaviour of hedge.New. On a
// streaming server this produces near-zero observations regardless of how
// long token generation actually takes.
type ttfbTransport struct {
	base http.RoundTripper
	sk   *sketch.WindowedSketch
}

func newTTFBTransport() *ttfbTransport {
	return &ttfbTransport{
		base: http.DefaultTransport,
		sk:   sketch.NewWindowedSketch(0.01, 30*time.Second),
	}
}

func (t *ttfbTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	t.sk.Add(float64(time.Since(start)))
	return resp, err
}

// staticHedgeTransport fires a hedge request after a fixed delay regardless of
// learned latency. It represents a transport miscalibrated from a TTFB sketch
// (hedge delay ≈ 1 ms = minDelay clamp) that fires a redundant request on
// virtually every call.
//
// Both primary and hedge share the caller's context so neither is cancelled
// prematurely. The losing response drains in a background goroutine.
type staticHedgeTransport struct {
	base  http.RoundTripper
	delay time.Duration
}

func (h *staticHedgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	type entry struct {
		resp *http.Response
		err  error
	}
	ch := make(chan entry, 2)

	go func() {
		resp, err := h.base.RoundTrip(req.Clone(req.Context()))
		ch <- entry{resp, err}
	}()

	timer := time.NewTimer(h.delay)
	select {
	case r := <-ch:
		timer.Stop()
		return r.resp, r.err
	case <-timer.C:
	}

	hedgeReq, _ := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
	go func() {
		resp, err := h.base.RoundTrip(hedgeReq)
		ch <- entry{resp, err}
	}()

	first := <-ch
	go func() {
		r := <-ch
		if r.resp != nil {
			io.Copy(io.Discard, io.LimitReader(r.resp.Body, 1<<20)) //nolint:errcheck
			r.resp.Body.Close()
		}
	}()
	return first.resp, first.err
}

// ── simulation harness ───────────────────────────────────────────────────────

type simResult struct {
	latencies []time.Duration
	overhead  float64
}

// runSim executes numRequests concurrent requests and returns sorted latencies
// plus hedge overhead. Set readBody to true for TTFT-aware transports so that
// ttftBody.Read fires and records the first-body-byte time into the sketch.
func runSim(tr http.RoundTripper, url string, hits *atomic.Int64, readBody bool) simResult {
	before := hits.Load()

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
				resp, err := tr.RoundTrip(req)
				if err == nil {
					if readBody {
						io.ReadAll(resp.Body) //nolint:errcheck
					}
					resp.Body.Close()
				}
				latencies[i] = time.Since(start)
			}
		}()
	}
	wg.Wait()

	total := hits.Load() - before
	overhead := float64(total-int64(numRequests)) / float64(numRequests) * 100
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return simResult{latencies: latencies, overhead: overhead}
}

func warmup(tr http.RoundTripper, url string, readBody bool) {
	for i := 0; i < warmupReqs; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		resp, err := tr.RoundTrip(req)
		if err == nil {
			if readBody {
				io.ReadAll(resp.Body) //nolint:errcheck
			}
			resp.Body.Close()
		}
	}
}

// ── output helpers ───────────────────────────────────────────────────────────

func pct(sorted []time.Duration, p float64) time.Duration {
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
}

// sketchPct queries a WindowedSketch and formats the result. Returns "<1ms"
// when the sketch is empty or the quantile value is below one millisecond.
func sketchPct(sk *sketch.WindowedSketch, q float64) string {
	v := sk.Quantile(q)
	if v <= 0 {
		return "<1ms"
	}
	d := time.Duration(v)
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fms", float64(d.Nanoseconds())/1e6)
	}
	return fmtMs(d)
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	fmt.Println("=== TTFB vs TTFT: Streaming LLM Benchmark ===")
	fmt.Println()
	fmt.Printf("TTFT distribution\n")
	fmt.Printf("  cache hit  (%2.0f%%):  log-normal  mean=%.0fms  σ=%.0fms\n",
		(1-cacheMissRate)*100, cacheHitMeanMs, cacheHitStdMs)
	fmt.Printf("  cache miss (%2.0f%%):  log-normal  mean=%.0fms  σ=%.0fms\n",
		cacheMissRate*100, cacheMissMeanMs, cacheMissStdMs)
	fmt.Printf("  requests=%d  concurrency=%d  warmup=%d\n\n",
		numRequests, concurrency, warmupReqs)

	// ── Part 1: sketch accuracy, streaming server ────────────────────────────

	var streamHits atomic.Int64
	srvStream := newStreamingServer(&streamHits)
	defer srvStream.Close()
	streamHost := srvStream.URL[len("http://"):]

	ttfbTr := newTTFBTransport()
	warmup(ttfbTr, srvStream.URL, false)
	streamHits.Store(0)
	runSim(ttfbTr, srvStream.URL, &streamHits, false)

	var ttftStats *hedge.Stats
	ttftTr := hedge.New(http.DefaultTransport,
		hedge.WithBudgetPercent(0), // no hedging; pure measurement
		hedge.WithMinDelay(time.Millisecond),
		hedge.WithPercentile(hedgePercentile),
		hedge.WithStats(&ttftStats),
	)
	warmup(ttftTr, srvStream.URL, true)
	streamHits.Store(0)
	runSim(ttftTr, srvStream.URL, &streamHits, true)

	ttfbEstimate := sketchPct(ttfbTr.sk, hedgePercentile)
	ttftEstimate := fmtMs(hedge.LatencyEstimate(ttftTr, streamHost, hedgePercentile))

	fmt.Println("── Part 1: Sketch signal · streaming server (200 OK flushed immediately) ──")
	fmt.Println()
	fmt.Println("  What each mode records into the DDSketch and what LatencyEstimate() returns.")
	fmt.Printf("  TTFB sketch p%-2d → %-8s   (near-zero: useless for EPP routing or hedge calibration)\n",
		int(hedgePercentile*100), ttfbEstimate)
	fmt.Printf("  TTFT sketch p%-2d → %-8s   (actual inference latency: correct signal)\n",
		int(hedgePercentile*100), ttftEstimate)
	fmt.Println()

	const col1w = 24
	fmt.Printf("  %-*s  %9s  %9s  %9s  %9s  %s\n",
		col1w, "mode", "p50", "p90", "p95", "p99",
		fmt.Sprintf("LatencyEstimate(p%d)", int(hedgePercentile*100)))
	fmt.Printf("  %-*s  %9s  %9s  %9s  %9s  %s\n",
		col1w, "────────────────────────",
		"─────────", "─────────", "─────────", "─────────",
		"──────────────────────────────────────────────────")

	fmt.Printf("  %-*s  %9s  %9s  %9s  %9s  %-8s  ← near-zero: wrong signal\n",
		col1w, "TTFB (old, broken)",
		sketchPct(ttfbTr.sk, 0.50),
		sketchPct(ttfbTr.sk, 0.90),
		sketchPct(ttfbTr.sk, 0.95),
		sketchPct(ttfbTr.sk, 0.99),
		ttfbEstimate,
	)
	fmt.Printf("  %-*s  %9s  %9s  %9s  %9s  %-8s  ← actual inference latency\n",
		col1w, "TTFT (new, fixed)",
		fmtMs(hedge.LatencyEstimate(ttftTr, streamHost, 0.50)),
		fmtMs(hedge.LatencyEstimate(ttftTr, streamHost, 0.90)),
		fmtMs(hedge.LatencyEstimate(ttftTr, streamHost, 0.95)),
		fmtMs(hedge.LatencyEstimate(ttftTr, streamHost, 0.99)),
		ttftEstimate,
	)
	fmt.Println()

	// ── Part 2: end-to-end latency, gateway server ──────────────────────────

	var gatewayHits atomic.Int64
	srvGateway := newGatewayServer(&gatewayHits)
	defer srvGateway.Close()
	gatewayHost := srvGateway.URL[len("http://"):]

	fmt.Println("── Part 2: End-to-end latency · gateway server (TTFH ≈ TTFT) ──────────────")
	fmt.Println()

	gatewayHits.Store(0)
	noHedge := runSim(http.DefaultTransport, srvGateway.URL, &gatewayHits, true)

	miscalTr := &staticHedgeTransport{base: http.DefaultTransport, delay: time.Millisecond}
	gatewayHits.Store(0)
	ttfbHedge := runSim(miscalTr, srvGateway.URL, &gatewayHits, true)

	var adaptStats *hedge.Stats
	adaptTr := hedge.New(http.DefaultTransport,
		hedge.WithBudgetPercent(100),
		hedge.WithMinDelay(time.Millisecond),
		hedge.WithPercentile(hedgePercentile),
		hedge.WithStats(&adaptStats),
	)
	warmup(adaptTr, srvGateway.URL, true)
	gatewayHits.Store(0)
	ttftHedge := runSim(adaptTr, srvGateway.URL, &gatewayHits, true)

	learnedDelay := hedge.LatencyEstimate(adaptTr, gatewayHost, hedgePercentile)

	rows := []row{
		{"No hedging", noHedge, "baseline"},
		{"TTFB-miscalibrated (1ms)", ttfbHedge, "hedges ~100% — massive waste"},
		{fmt.Sprintf("TTFT-calibrated (p%d≈%s)", int(hedgePercentile*100), fmtMs(learnedDelay)),
			ttftHedge, "hedges only slow outliers (~20%)"},
	}

	const col2w = 32
	fmt.Printf("  %-*s  %8s  %8s  %8s  %8s  %9s  note\n",
		col2w, "mode", "p50", "p90", "p95", "p99", "overhead")
	fmt.Printf("  %-*s  %8s  %8s  %8s  %8s  %9s  ────\n",
		col2w, "────────────────────────────────",
		"────────", "────────", "────────", "────────", "─────────")
	for _, r := range rows {
		l := r.r.latencies
		fmt.Printf("  %-*s  %8s  %8s  %8s  %8s  %8.1f%%  %s\n",
			col2w, r.name,
			fmtMs(pct(l, 0.50)),
			fmtMs(pct(l, 0.90)),
			fmtMs(pct(l, 0.95)),
			fmtMs(pct(l, 0.99)),
			r.r.overhead,
			r.note,
		)
	}

	fmt.Println()
	fmt.Printf("adaptive transport debug (host=%s):\n", gatewayHost)
	fmt.Printf("  learned p%d hedge delay : %v\n",
		int(hedgePercentile*100), learnedDelay)
	fmt.Printf("  hedged / total          : %d / %d (%.1f%%)\n",
		adaptStats.HedgedRequests.Load(),
		adaptStats.TotalRequests.Load(),
		float64(adaptStats.HedgedRequests.Load())/float64(adaptStats.TotalRequests.Load())*100)
	fmt.Printf("  budget exhausted        : %d\n", adaptStats.BudgetExhausted.Load())

	writeCSV(rows)
}

type row struct {
	name string
	r    simResult
	note string
}

func writeCSV(rows []row) {
	f, err := os.Create("results.csv")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create csv:", err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	_ = w.Write([]string{"mode", "p50_ms", "p90_ms", "p95_ms", "p99_ms", "overhead_pct", "note"})
	for _, r := range rows {
		l := r.r.latencies
		_ = w.Write([]string{
			r.name,
			fmtMs(pct(l, 0.50)),
			fmtMs(pct(l, 0.90)),
			fmtMs(pct(l, 0.95)),
			fmtMs(pct(l, 0.99)),
			fmt.Sprintf("%.1f", r.r.overhead),
			r.note,
		})
	}
	w.Flush()
	fmt.Println("\nresults written to benchmark/streaming/results.csv")
}
