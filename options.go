package hedge

import "time"

type config struct {
	percentile      float64
	maxHedges       int
	budgetPercent   float64
	minDelay        time.Duration
	warmupRequests  int
	warmupDelay     time.Duration
	windowDuration  time.Duration
	stats           **Stats
}

func defaults() config {
	return config{
		percentile:     0.95,
		maxHedges:      1,
		budgetPercent:  5.0,
		minDelay:       1 * time.Millisecond,
		warmupRequests: 20,
		warmupDelay:    10 * time.Millisecond,
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
