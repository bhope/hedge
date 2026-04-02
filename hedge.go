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

var timerPool = sync.Pool{
	New: func() any {
		t := time.NewTimer(time.Hour)
		t.Stop()
		return t
	},
}

type result struct {
	resp    *http.Response
	err     error
	elapsed time.Duration
	primary bool
}

type hedgedTransport struct {
	base     http.RoundTripper
	cfg      config
	stats    *Stats
	budget   *budget.TokenBucket
	mu       sync.RWMutex
	sketches map[string]*sketch.WindowedSketch
	counters map[string]*atomic.Int64
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
		sketches: make(map[string]*sketch.WindowedSketch),
		counters: make(map[string]*atomic.Int64),
	}
}

func (t *hedgedTransport) sketchFor(host string) *sketch.WindowedSketch {
	t.mu.RLock()
	s, ok := t.sketches[host]
	t.mu.RUnlock()
	if ok {
		return s
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok = t.sketches[host]; ok {
		return s
	}

	s = sketch.NewWindowedSketch(0.01, t.cfg.windowDuration)
	t.sketches[host] = s
	return s
}

func (t *hedgedTransport) counterFor(host string) *atomic.Int64 {
	t.mu.RLock()
	c, ok := t.counters[host]
	t.mu.RUnlock()
	if ok {
		return c
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok = t.counters[host]; ok {
		return c
	}

	c = &atomic.Int64{}
	t.counters[host] = c
	return c
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
	defer primaryCancel()

	ch := make(chan result, 2)
	start := time.Now()

	go func() {
		resp, err := t.base.RoundTrip(req.Clone(primaryCtx))
		ch <- result{resp, err, time.Since(start), true}
	}()

	timer := timerPool.Get().(*time.Timer)
	timer.Reset(hedgeDelay)

	select {
	case res := <-ch:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerPool.Put(timer)
		sk.Add(float64(res.elapsed))
		return res.resp, res.err
	case <-timer.C:
		timerPool.Put(timer)
	}

	if !canHedge(req) {
		res := <-ch
		sk.Add(float64(res.elapsed))
		return res.resp, res.err
	}

	if !t.budget.TryAcquire() {
		t.stats.BudgetExhausted.Add(1)
		res := <-ch
		sk.Add(float64(res.elapsed))
		return res.resp, res.err
	}

	hedgeCtx, hedgeCancel := context.WithCancel(parentCtx)
	defer hedgeCancel()

	hedgeReq, err := cloneRequest(req, hedgeCtx)
	if err != nil {
		res := <-ch
		sk.Add(float64(res.elapsed))
		return res.resp, res.err
	}

	t.stats.HedgedRequests.Add(1)

	go func() {
		resp, err := t.base.RoundTrip(hedgeReq)
		ch <- result{resp, err, time.Since(start), false}
	}()

	first := <-ch
	if first.primary {
		hedgeCancel()
	} else {
		primaryCancel()
	}

	go drainLoser(ch)

	sk.Add(float64(first.elapsed))
	if first.primary {
		t.stats.PrimaryWins.Add(1)
	} else {
		t.stats.HedgeWins.Add(1)
	}
	return first.resp, first.err
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
