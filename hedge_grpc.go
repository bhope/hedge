package hedge

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhope/hedge/budget"
	"github.com/bhope/hedge/sketch"
	"google.golang.org/grpc"
)

type grpcInterceptor struct {
	cfg      config
	stats    *Stats
	budget   *budget.TokenBucket
	sketches sync.Map // target -> *sketch.WindowedSketch
	counters sync.Map // target -> *atomic.Int64
}

type grpcResult struct {
	reply   interface{}
	err     error
	elapsed time.Duration
	primary bool
}

func NewUnaryClientInterceptor(opts ...Option) grpc.UnaryClientInterceptor {
	cfg := defaults()
	for _, o := range opts {
		o(&cfg)
	}
	s := &Stats{}
	if cfg.stats != nil {
		*cfg.stats = s
	}
	gi := &grpcInterceptor{
		cfg:    cfg,
		stats:  s,
		budget: budget.NewTokenBucket(cfg.budgetPercent, 100),
	}
	return gi.intercept
}

func (gi *grpcInterceptor) sketchFor(target string) *sketch.WindowedSketch {
	v, ok := gi.sketches.Load(target)
	if ok {
		return v.(*sketch.WindowedSketch)
	}
	s := sketch.NewWindowedSketch(0.01, gi.cfg.windowDuration)
	actual, loaded := gi.sketches.LoadOrStore(target, s)
	if loaded {
		s.Stop()
		return actual.(*sketch.WindowedSketch)
	}
	return s
}

func (gi *grpcInterceptor) counterFor(target string) *atomic.Int64 {
	v, ok := gi.counters.Load(target)
	if ok {
		return v.(*atomic.Int64)
	}
	var c atomic.Int64
	actual, _ := gi.counters.LoadOrStore(target, &c)
	return actual.(*atomic.Int64)
}

func allocReply(reply interface{}) interface{} {
	return reflect.New(reflect.TypeOf(reply).Elem()).Interface()
}

func mergeReply(dst, src interface{}) {
	reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(src).Elem())
}

func (gi *grpcInterceptor) intercept(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	gi.stats.TotalRequests.Add(1)

	target := cc.Target()
	sk := gi.sketchFor(target)
	counter := gi.counterFor(target)
	n := counter.Add(1)

	var hedgeDelay time.Duration
	if n <= int64(gi.cfg.warmupRequests) {
		gi.stats.WarmupRequests.Add(1)
		hedgeDelay = gi.cfg.warmupDelay
	} else {
		if est := sk.Quantile(gi.cfg.percentile); est > 0 {
			hedgeDelay = time.Duration(est)
		} else {
			hedgeDelay = gi.cfg.warmupDelay
		}
	}
	if hedgeDelay < gi.cfg.minDelay {
		hedgeDelay = gi.cfg.minDelay
	}

	primaryCtx, primaryCancel := context.WithCancel(ctx)
	defer primaryCancel()

	ch := make(chan grpcResult, 2)
	start := time.Now()

	primaryReply := allocReply(reply)
	go func() {
		err := invoker(primaryCtx, method, req, primaryReply, cc, opts...)
		ch <- grpcResult{primaryReply, err, time.Since(start), true}
	}()

	timer := time.NewTimer(hedgeDelay)
	defer timer.Stop()

	select {
	case res := <-ch:
		sk.Add(float64(res.elapsed))
		if res.err == nil {
			mergeReply(reply, res.reply)
		}
		return res.err
	case <-timer.C:
	}

	if !gi.budget.TryAcquire() {
		gi.stats.BudgetExhausted.Add(1)
		res := <-ch
		sk.Add(float64(res.elapsed))
		if res.err == nil {
			mergeReply(reply, res.reply)
		}
		return res.err
	}

	hedgeCtx, hedgeCancel := context.WithCancel(ctx)
	defer hedgeCancel()

	gi.stats.HedgedRequests.Add(1)

	hedgeReply := allocReply(reply)
	go func() {
		err := invoker(hedgeCtx, method, req, hedgeReply, cc, opts...)
		ch <- grpcResult{hedgeReply, err, time.Since(start), false}
	}()

	first := <-ch
	if first.primary {
		hedgeCancel()
	} else {
		primaryCancel()
	}

	go func() { <-ch }()

	sk.Add(float64(first.elapsed))
	if first.err == nil {
		mergeReply(reply, first.reply)
	}
	if first.primary {
		gi.stats.PrimaryWins.Add(1)
	} else {
		gi.stats.HedgeWins.Add(1)
	}
	return first.err
}
