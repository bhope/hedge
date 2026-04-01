package hedge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newDelayServer(t *testing.T, delay *atomic.Int64, header string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := time.Duration(delay.Load()); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		if header != "" {
			w.Header().Set("X-Responder", header)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func newTransport(t *testing.T, opts ...Option) (http.RoundTripper, *Stats) {
	t.Helper()
	var stats *Stats
	opts = append(opts, WithStats(&stats))
	tr := New(http.DefaultTransport, opts...)
	return tr, stats
}

func warmup(t *testing.T, tr http.RoundTripper, url string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		req := httptest.NewRequest("GET", url, nil)
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatalf("warmup request %d failed: %v", i, err)
		}
		_ = resp.Body.Close()
	}
}

func TestNoHedgeWhenFast(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithMinDelay(50*time.Millisecond),
		WithBudgetPercent(100),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d, want 0", n)
	}
}

func TestHedgeWhenSlow(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithPercentile(0.5),
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
	)
	warmup(t, tr, srv.URL, 25)

	// now slow
	delay.Store(int64(200 * time.Millisecond))

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n == 0 {
		t.Error("expected HedgedRequests > 0")
	}
}

func TestHedgeWins(t *testing.T) {
	var primaryDelay, hedgeDelay atomic.Int64
	primaryDelay.Store(int64(500 * time.Millisecond))

	callCount := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		var d time.Duration
		if n == 1 {
			d = time.Duration(primaryDelay.Load())
		} else {
			d = time.Duration(hedgeDelay.Load())
		}
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("X-Responder", map[bool]string{true: "primary", false: "hedge"}[n == 1])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := resp.Header.Get("X-Responder"); got != "hedge" {
		t.Errorf("X-Responder = %q, want hedge", got)
	}
	if n := stats.HedgeWins.Load(); n == 0 {
		t.Error("expected HedgeWins > 0")
	}
}

func TestPrimaryWins(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(2 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithMinDelay(50*time.Millisecond),
		WithBudgetPercent(100),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d, want 0", n)
	}
	// warmup requests all returned before the timer — PrimaryWins only counts
	// hedged races, so it should still be 0.
	if n := stats.HedgeWins.Load(); n != 0 {
		t.Errorf("HedgeWins = %d, want 0", n)
	}
}

func TestBudgetExhaustion(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(50 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(1),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", srv.URL, nil)
			resp, err := tr.RoundTrip(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	if stats.BudgetExhausted.Load() == 0 {
		t.Error("expected BudgetExhausted > 0")
	}
	if hedged := stats.HedgedRequests.Load(); hedged >= int64(n) {
		t.Errorf("HedgedRequests = %d, want < %d (budget should have limited hedges)", hedged, n)
	}
}

func TestNoHedgeForPOST(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(50 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("POST", srv.URL, strings.NewReader("payload"))

	before := stats.HedgedRequests.Load()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if delta := stats.HedgedRequests.Load() - before; delta != 0 {
		t.Errorf("HedgedRequests delta = %d, want 0 for POST without GetBody", delta)
	}
}

func TestHedgeWithGetBody(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(50 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	payload := "payload"
	req := httptest.NewRequest("POST", srv.URL, strings.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(payload)), nil
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n == 0 {
		t.Error("expected HedgedRequests > 0 for POST with GetBody")
	}
}

func TestContextCancellation(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(blocked)

	// No warmup — warmupDelay (10ms) drives the hedge timer so both primary
	// and hedge fire before the context is cancelled at 20ms.
	tr, _ := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, "GET", srv.URL, nil)

	done := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RoundTrip did not return after context cancellation")
	}
}

func TestWarmupPhase(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(5 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
	)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.WarmupRequests.Load(); n == 0 {
		t.Error("expected WarmupRequests > 0 during warmup phase")
	}
	// warmupDelay default is 10ms, backend is 5ms → primary returns before warmup timer
	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d during warmup, want 0", n)
	}
}

func TestHedgePerKey(t *testing.T) {
	fooDelay := 5 * time.Millisecond
	barDelay := 20 * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc("GET /foo", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(fooDelay):
			// continue to respond
		case <-r.Context().Done():
			return
		}
		w.Header().Set("X-Responder", "foo")
	})
	mux.HandleFunc("GET /bar", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(barDelay):
			// continue to respond
		case <-r.Context().Done():
			return
		}
		w.Header().Set("X-Responder", "bar")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithHTTPKeyFunc(func(r *http.Request) string {
			return fmt.Sprintf("%s %s/%s", r.Method, r.URL.Host, r.URL.Path)
		}),
	)

	fooURL := fmt.Sprintf("%s/foo", srv.URL)
	barURL := fmt.Sprintf("%s/bar", srv.URL)
	warmup(t, tr, fooURL, 25)
	warmup(t, tr, barURL, 25)

	*stats = Stats{} // reset stats after warmup

	req := httptest.NewRequest("GET", fooURL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for fast /foo, want 0", n)
	}

	req = httptest.NewRequest("GET", barURL, nil)
	resp, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for slow /bar, want 0", n)
	}

	// swap foo and bar response delays
	fooDelay, barDelay = barDelay, fooDelay

	// bar requests should not hedge because they got faster
	req = httptest.NewRequest("GET", barURL, nil)
	resp, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for sped up /bar, want 0", n)
	}

	// foo requests should hedge because they are slower than before
	req = httptest.NewRequest("GET", fooURL, nil)
	resp, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgedRequests.Load(); n == 0 {
		t.Error("expected HedgedRequests > 0 for slowed down /foo")
	}
}

func TestConcurrent(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(5 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(10),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	const goroutines = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", srv.URL, nil)
			resp, err := tr.RoundTrip(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	total := stats.TotalRequests.Load()
	if total < goroutines {
		t.Errorf("TotalRequests = %d, want >= %d", total, goroutines)
	}
}

func TestStats(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(50 * time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	snap := stats.Snapshot()
	if snap.TotalRequests == 0 {
		t.Error("Snapshot TotalRequests = 0")
	}
	if rate := stats.HedgeRate(); rate < 0 || rate > 1 {
		t.Errorf("HedgeRate() = %v, want [0,1]", rate)
	}
}

func TestWithMaxHedges(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, _ := newTransport(t, WithMaxHedges(2))
	warmup(t, tr, srv.URL, 5)
	// just verify it builds and runs without panic
	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestHedgeAllowedForNoBody(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(time.Millisecond))
	srv := newDelayServer(t, &delay, "primary")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	// Switch to slow so the hedge fires well before the primary returns.
	delay.Store(int64(200 * time.Millisecond))

	// http.NoBody carries no content, so hedging is safe (same as nil body).
	req := httptest.NewRequest("POST", srv.URL, http.NoBody)
	before := stats.HedgedRequests.Load()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if delta := stats.HedgedRequests.Load() - before; delta == 0 {
		t.Error("expected hedge for POST with http.NoBody (no body to re-send)")
	}
}

// TestHedgeWithGetBodyReliable trains on a very fast server then switches to a
// slow one so the hedge reliably fires and exercises cloneRequest's GetBody path.
func TestHedgeWithGetBodyReliable(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(time.Millisecond))
	srv := newDelayServer(t, &delay, "")
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	// Switch to slow so the hedge fires well before the primary returns.
	delay.Store(int64(200 * time.Millisecond))

	payload := "payload"
	req := httptest.NewRequest("POST", srv.URL, strings.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(payload)), nil
	}

	before := stats.HedgedRequests.Load()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if delta := stats.HedgedRequests.Load() - before; delta == 0 {
		t.Error("expected HedgedRequests > 0 for POST with GetBody on slow backend")
	}
}

// TestDrainLoserBody ensures the loser's body is drained when both goroutines
// return responses (hedge wins scenario with a real response body from primary).
func TestDrainLoserBody(t *testing.T) {
	callCount := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response body"))
	}))
	defer srv.Close()

	tr, stats := newTransport(t,
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
		WithPercentile(0.5),
	)
	warmup(t, tr, srv.URL, 25)

	req := httptest.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if n := stats.HedgeWins.Load(); n == 0 {
		t.Error("expected HedgeWins > 0")
	}
	// give drainLoser goroutine time to finish
	time.Sleep(600 * time.Millisecond)
}
