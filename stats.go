package hedge

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

type Stats struct {
	TotalRequests   atomic.Int64
	HedgedRequests  atomic.Int64
	HedgeWins       atomic.Int64
	PrimaryWins     atomic.Int64
	BudgetExhausted atomic.Int64
	WarmupRequests  atomic.Int64
}

type StatsSnapshot struct {
	TotalRequests   int64
	HedgedRequests  int64
	HedgeWins       int64
	PrimaryWins     int64
	BudgetExhausted int64
	WarmupRequests  int64
}

func (s *Stats) Snapshot() StatsSnapshot {
	if s == nil {
		return StatsSnapshot{}
	}
	return StatsSnapshot{
		TotalRequests:   s.TotalRequests.Load(),
		HedgedRequests:  s.HedgedRequests.Load(),
		HedgeWins:       s.HedgeWins.Load(),
		PrimaryWins:     s.PrimaryWins.Load(),
		BudgetExhausted: s.BudgetExhausted.Load(),
		WarmupRequests:  s.WarmupRequests.Load(),
	}
}

func (snap StatsSnapshot) HedgeRate() float64 {
	if snap.TotalRequests == 0 {
		return 0
	}
	return float64(snap.HedgedRequests) / float64(snap.TotalRequests)
}

func (s *Stats) HedgeRate() float64 {
	if s == nil {
		return 0
	}
	return s.Snapshot().HedgeRate()
}

// WritePrometheus writes the current stats in Prometheus text exposition format.
func (s *Stats) WritePrometheus(w io.Writer) error {
	snap := s.Snapshot()
	rate := snap.HedgeRate()

	metrics := []struct {
		name string
		typ  string
		help string
		val  any
	}{
		{
			name: "hedge_total_requests_total",
			typ:  "counter",
			help: "Total requests seen by the hedged transport.",
			val:  snap.TotalRequests,
		},
		{
			name: "hedge_hedged_requests_total",
			typ:  "counter",
			help: "Requests that launched at least one hedge.",
			val:  snap.HedgedRequests,
		},
		{
			name: "hedge_hedge_wins_total",
			typ:  "counter",
			help: "Races won by the hedge request.",
			val:  snap.HedgeWins,
		},
		{
			name: "hedge_primary_wins_total",
			typ:  "counter",
			help: "Races won by the primary request after a hedge launched.",
			val:  snap.PrimaryWins,
		},
		{
			name: "hedge_budget_exhausted_total",
			typ:  "counter",
			help: "Requests that could not hedge because the budget was exhausted.",
			val:  snap.BudgetExhausted,
		},
		{
			name: "hedge_warmup_requests_total",
			typ:  "counter",
			help: "Requests served while latency estimates were still warming up.",
			val:  snap.WarmupRequests,
		},
		{
			name: "hedge_hedge_rate",
			typ:  "gauge",
			help: "Fraction of requests that launched a hedge.",
			val:  rate,
		},
	}

	for _, metric := range metrics {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n",
			metric.name, metric.help,
			metric.name, metric.typ,
			metric.name, metric.val,
		); err != nil {
			return err
		}
	}
	return nil
}

// ServeHTTP exposes the current stats in Prometheus text exposition format so
// callers can mount stats directly at /metrics.
func (s *Stats) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_ = s.WritePrometheus(w)
}
