package hedge

import (
	"net/http"
	"time"
)

type config struct {
	percentile     float64
	maxHedges      int
	budgetPercent  float64
	budgetRPS      float64
	minDelay       time.Duration
	warmupRequests int
	warmupDelay    time.Duration
	windowDuration time.Duration
	httpKeyFunc    func(*http.Request) string
	grpcKeyFunc    func(GRPCRequestData) string
	stats          **Stats
}

func defaults() config {
	return config{
		percentile:     0.90,
		maxHedges:      1,
		budgetPercent:  10.0,
		budgetRPS:      100,
		minDelay:       1 * time.Millisecond,
		warmupRequests: 20,
		warmupDelay:    10 * time.Millisecond,
		httpKeyFunc:    defaultHTTPKeyFunc,
		grpcKeyFunc:    defaultGRPCKeyFunc,
		windowDuration: 30 * time.Second,
	}
}

type Option func(*config)

func WithPercentile(p float64) Option {
	return func(c *config) { c.percentile = p }
}

func WithMaxHedges(n int) Option {
	return func(c *config) { c.maxHedges = n }
}

func WithBudgetPercent(pct float64) Option {
	return func(c *config) { c.budgetPercent = pct }
}

func WithMinDelay(d time.Duration) Option {
	return func(c *config) { c.minDelay = d }
}

func WithStats(s **Stats) Option {
	return func(c *config) { c.stats = s }
}

func WithEstimatedRPS(rps float64) Option {
	return func(c *config) { c.budgetRPS = rps }
}

// WithHTTPKeyFunc customizes how HTTP request latency metrics are bucketed.
// The default is to bucket metrics by hostname (e.g. "example.com").
//
// Clients may use this method to group requests into finer-grained buckets,
// e.g. by factoring in request attributes like method, path, or query
// parameters.
//
// Use this option when latency characteristics vary significantly across
// different endpoints on the same host, e.g. "POST example.com/api" vs
// "GET example.com/static".
func WithHTTPKeyFunc(f func(r *http.Request) string) Option {
	return func(c *config) {
		c.httpKeyFunc = f
	}
}

func defaultHTTPKeyFunc(r *http.Request) string {
	return r.URL.Host
}

// WithGRPCKeyFunc customizes how gRPC request latency metrics are bucketed.
// The default is to bucket metrics by hostname (e.g. "example.com").
//
// Clients may use this method to group requests into finer-grained buckets,
// e.g. by factoring in gRPC request attributes such as method (RPC) name,
// or properties of the request object.
//
// Use this option when latency characteristics vary significantly across
// different endpoints on the same host, e.g. "example.com/api.Service/MethodA"
// vs "example.com/api.Service/MethodB".
func WithGRPCKeyFunc(f func(d GRPCRequestData) string) Option {
	return func(c *config) {
		c.grpcKeyFunc = f
	}
}

func defaultGRPCKeyFunc(d GRPCRequestData) string {
	return d.Hostname
}
