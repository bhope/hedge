// Package prom provides a Prometheus collector for hedge.Stats.
//
// The collector is read-only: on every scrape it reads the atomic counters
// of the wrapped [hedge.Stats] and reports them as Prometheus metrics.
// It keeps the hedge module itself free of Prometheus dependencies;
// import this package only if you want to export metrics.
package prom

import (
	"github.com/bhope/hedge"
	"github.com/prometheus/client_golang/prometheus"
)

type statsCollector struct {
	stats *hedge.Stats

	totalRequests   *prometheus.Desc
	hedgedRequests  *prometheus.Desc
	hedgeWins       *prometheus.Desc
	primaryWins     *prometheus.Desc
	budgetExhausted *prometheus.Desc
	warmupRequests  *prometheus.Desc
	hedgeRate       *prometheus.Desc
}

// NewStatsCollector returns a prometheus.Collector that reports the given
// hedge stats. The returned collector implements prometheus.Collector and
// must be registered with a prometheus.Registerer, e.g.:
//
//	var stats *hedge.Stats
//	tr := hedge.New(http.DefaultTransport, hedge.WithStats(&stats))
//	prometheus.MustRegister(prom.NewStatsCollector(stats))
func NewStatsCollector(s *hedge.Stats) prometheus.Collector {
	if s == nil {
		panic("prom.NewStatsCollector: nil hedge.Stats")
	}

	return &statsCollector{
		stats: s,
		totalRequests: prometheus.NewDesc(
			"hedge_requests_total",
			"Total number of requests processed by the hedged transport.",
			nil, nil,
		),
		hedgedRequests: prometheus.NewDesc(
			"hedge_hedged_requests_total",
			"Number of hedge (duplicate) requests issued.",
			nil, nil,
		),
		hedgeWins: prometheus.NewDesc(
			"hedge_hedge_wins_total",
			"Number of requests answered by the hedge request.",
			nil, nil,
		),
		primaryWins: prometheus.NewDesc(
			"hedge_primary_wins_total",
			"Number of requests answered by the original request.",
			nil, nil,
		),
		budgetExhausted: prometheus.NewDesc(
			"hedge_budget_exhausted_total",
			"Number of hedges suppressed because the hedging budget was exhausted.",
			nil, nil,
		),
		warmupRequests: prometheus.NewDesc(
			"hedge_warmup_requests_total",
			"Number of warmup requests seen before the sketch had enough samples.",
			nil, nil,
		),
		hedgeRate: prometheus.NewDesc(
			"hedge_hedge_rate",
			"Ratio of hedged to total requests (HedgedRequests / TotalRequests).",
			nil, nil,
		),
	}
}

func (c *statsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalRequests
	ch <- c.hedgedRequests
	ch <- c.hedgeWins
	ch <- c.primaryWins
	ch <- c.budgetExhausted
	ch <- c.warmupRequests
	ch <- c.hedgeRate
}

func (c *statsCollector) Collect(ch chan<- prometheus.Metric) {
	snap := c.stats.Snapshot()

	ch <- prometheus.MustNewConstMetric(c.totalRequests, prometheus.CounterValue, float64(snap.TotalRequests))
	ch <- prometheus.MustNewConstMetric(c.hedgedRequests, prometheus.CounterValue, float64(snap.HedgedRequests))
	ch <- prometheus.MustNewConstMetric(c.hedgeWins, prometheus.CounterValue, float64(snap.HedgeWins))
	ch <- prometheus.MustNewConstMetric(c.primaryWins, prometheus.CounterValue, float64(snap.PrimaryWins))
	ch <- prometheus.MustNewConstMetric(c.budgetExhausted, prometheus.CounterValue, float64(snap.BudgetExhausted))
	ch <- prometheus.MustNewConstMetric(c.warmupRequests, prometheus.CounterValue, float64(snap.WarmupRequests))
	ch <- prometheus.MustNewConstMetric(c.hedgeRate, prometheus.GaugeValue, c.stats.HedgeRate())
}
