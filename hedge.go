package hedge

import (
	"context"
	"io"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhope/hedge/budget"
	"github.com/bhope/hedge/sketch"
)

const drainLimit = 1 << 20 // 1MB

type result struct {
	resp    *http.Response
	err     error
	primary bool
}

// ttftBody wraps a response body and records time-to-first-byte to the sketch on
// the first successful Read. Falls back to recording at Close if the body is never read
// (e.g. empty responses or HEAD requests). cancel is called on Close so that the
// request context outlives RoundTrip while the caller streams the response body.
type ttftBody struct {
	io.ReadCloser
	once   sync.Once
	start  time.Time
	sk     *sketch.WindowedSketch
	cancel context.CancelFunc
}

func (b *ttftBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() {
			b.sk.Add(float64(time.Since(b.start)))
		})
	}
	return n, err
}

func (b *ttftBody) Close() error {
	// Fallback: if the body was closed without any bytes being read (empty body,
	// HEAD response, etc.) record elapsed now so the sketch always gets a sample.
	b.once.Do(func() {
		b.sk.Add(float64(time.Since(b.start)))
	})
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

var timerPool = sync.Pool{
	New: func() any {
		t := time.NewTimer(0)
		if !t.Stop() {
			<-t.C
		}
		return t
	},
}

type sketchMap struct {
	mu sync.RWMutex
	m  map[string]*sketch.WindowedSketch
}

func (s *sketchMap) get(host string, windowDuration time.Duration) *sketch.WindowedSketch {
	s.mu.RLock()
	v, ok := s.m[host]
	s.mu.RUnlock()
	if ok {
		return v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[host]; ok {
		return v
	}
	v = sketch.NewWindowedSketch(0.01, windowDuration)
	s.m[host] = v
	return v
}

type counterMap struct {
	mu sync.RWMutex
	m  map[string]*atomic.Int64
}

func (c *counterMap) get(host string) *atomic.Int64 {
	c.mu.RLock()
	v, ok := c.m[host]
	c.mu.RUnlock()
	if ok {
		return v
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[host]; ok {
		return v
	}
	var ctr atomic.Int64
	c.m[host] = &ctr
	return &ctr
}

type hedgedTransport struct {
	base     http.RoundTripper
	cfg      config
	stats    *Stats
	budget   *budget.TokenBucket
	sketches sketchMap
	counters counterMap
}

func New(transport http.RoundTripper, opts ...Option) http.RoundTripper {
	cfg := defaults()
	for _, o := range opts {
		o(&cfg)
	}
	s := &Stats{}
	if cfg.stats != nil {
		*cfg.stats = s
	}
	return &hedgedTransport{
		base:     transport,
		cfg:      cfg,
		stats:    s,
		budget:   budget.NewTokenBucket(cfg.budgetPercent, cfg.budgetRPS),
		sketches: sketchMap{m: make(map[string]*sketch.WindowedSketch)},
		counters: counterMap{m: make(map[string]*atomic.Int64)},
	}
}

func wrapTTFT(resp *http.Response, err error, start time.Time, sk *sketch.WindowedSketch, cancel context.CancelFunc) (*http.Response, error) {
	if err != nil || resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		sk.Add(float64(time.Since(start)))
		cancel()
		return resp, err
	}
	resp.Body = &ttftBody{ReadCloser: resp.Body, start: start, sk: sk, cancel: cancel}
	return resp, err
}

func (t *hedgedTransport) sketchFor(host string) *sketch.WindowedSketch {
	return t.sketches.get(host, t.cfg.windowDuration)
}

func (t *hedgedTransport) counterFor(host string) *atomic.Int64 {
	return t.counters.get(host)
}

func (t *hedgedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.stats.TotalRequests.Add(1)

	host := req.URL.Host
	sk := t.sketchFor(host)
	counter := t.counterFor(host)
	n := counter.Add(1)

	var hedgeDelay time.Duration
	if n <= int64(t.cfg.warmupRequests) {
		t.stats.WarmupRequests.Add(1)
		hedgeDelay = t.cfg.warmupDelay
	} else {
		if est := sk.Quantile(t.cfg.percentile); est > 0 {
			hedgeDelay = time.Duration(est)
		} else {
			hedgeDelay = t.cfg.warmupDelay
		}
	}
	if hedgeDelay < t.cfg.minDelay {
		hedgeDelay = t.cfg.minDelay
	}

	parentCtx := req.Context()
	primaryCtx, primaryCancel := context.WithCancel(parentCtx)

	ch := make(chan result, 2)
	start := time.Now()

	go func() {
		resp, err := t.base.RoundTrip(req.Clone(primaryCtx))
		ch <- result{resp, err, true}
	}()

	timer := timerPool.Get().(*time.Timer)
	timer.Reset(hedgeDelay)

	select {
	case res := <-ch:
		if !timer.Stop() {
			<-timer.C
		}
		timerPool.Put(timer)
		return wrapTTFT(res.resp, res.err, start, sk, primaryCancel)
	case <-timer.C:
		timerPool.Put(timer)
	}

	if !canHedge(req) {
		res := <-ch
		return wrapTTFT(res.resp, res.err, start, sk, primaryCancel)
	}

	if !t.budget.TryAcquire() {
		t.stats.BudgetExhausted.Add(1)
		res := <-ch
		return wrapTTFT(res.resp, res.err, start, sk, primaryCancel)
	}

	hedgeCtx, hedgeCancel := context.WithCancel(parentCtx)

	hedgeReq, err := cloneRequest(req, hedgeCtx)
	if err != nil {
		hedgeCancel()
		res := <-ch
		return wrapTTFT(res.resp, res.err, start, sk, primaryCancel)
	}

	t.stats.HedgedRequests.Add(1)

	go func() {
		resp, err := t.base.RoundTrip(hedgeReq)
		ch <- result{resp, err, false}
	}()

	first := <-ch
	go drainLoser(ch)

	if first.primary {
		// Primary won: cancel hedge immediately, defer primary cancel to body close.
		hedgeCancel()
		t.stats.PrimaryWins.Add(1)
		return wrapTTFT(first.resp, first.err, start, sk, primaryCancel)
	}
	// Hedge won: cancel primary immediately, defer hedge cancel to body close.
	primaryCancel()
	t.stats.HedgeWins.Add(1)
	return wrapTTFT(first.resp, first.err, start, sk, hedgeCancel)
}

func canHedge(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return true
	}
	return req.GetBody != nil
}

func cloneRequest(req *http.Request, ctx context.Context) (*http.Request, error) {
	cloned := req.Clone(ctx)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		cloned.Body = body
	}
	return cloned, nil
}

func drainLoser(ch <-chan result) {
	res, ok := <-ch
	if !ok || res.resp == nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(res.resp.Body, drainLimit))
	res.resp.Body.Close()
}

// LatencyEstimate returns the current hedge-delay threshold the transport
// would use for the given host and quantile. Returns 0 if rt was not
// created by New.
func LatencyEstimate(rt http.RoundTripper, host string, q float64) time.Duration {
	ht, ok := rt.(*hedgedTransport)
	if !ok {
		return 0
	}
	sk := ht.sketchFor(host)
	est := sk.Quantile(q)
	if math.IsNaN(est) || est <= 0 {
		return ht.cfg.warmupDelay
	}
	d := time.Duration(est)
	if d < ht.cfg.minDelay {
		return ht.cfg.minDelay
	}
	return d
}
