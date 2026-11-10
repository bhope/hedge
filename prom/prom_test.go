package prom_test

import (
	"strings"
	"testing"

	"github.com/bhope/hedge"
	"github.com/bhope/hedge/prom"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
)

func TestStatsCollector(t *testing.T) {
	stats := &hedge.Stats{}
	stats.TotalRequests.Add(10)
	stats.HedgedRequests.Add(4)
	stats.HedgeWins.Add(1)
	stats.PrimaryWins.Add(9)
	stats.BudgetExhausted.Add(2)
	stats.WarmupRequests.Add(3)

	reg := prometheus.NewRegistry()
	if err := reg.Register(prom.NewStatsCollector(stats)); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	want := `
# HELP hedge_budget_exhausted_total Number of hedges suppressed because the hedging budget was exhausted.
# TYPE hedge_budget_exhausted_total counter
hedge_budget_exhausted_total 2
# HELP hedge_hedge_rate Ratio of hedged to total requests (HedgedRequests / TotalRequests).
# TYPE hedge_hedge_rate gauge
hedge_hedge_rate 0.4
# HELP hedge_hedge_wins_total Number of requests answered by the hedge request.
# TYPE hedge_hedge_wins_total counter
hedge_hedge_wins_total 1
# HELP hedge_hedged_requests_total Number of hedge (duplicate) requests issued.
# TYPE hedge_hedged_requests_total counter
hedge_hedged_requests_total 4
# HELP hedge_primary_wins_total Number of requests answered by the original request.
# TYPE hedge_primary_wins_total counter
hedge_primary_wins_total 9
# HELP hedge_requests_total Total number of requests processed by the hedged transport.
# TYPE hedge_requests_total counter
hedge_requests_total 10
# HELP hedge_warmup_requests_total Number of warmup requests seen before the sketch had enough samples.
# TYPE hedge_warmup_requests_total counter
hedge_warmup_requests_total 3
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestStatsCollectorZero(t *testing.T) {
	stats := &hedge.Stats{}

	reg := prometheus.NewRegistry()
	reg.MustRegister(prom.NewStatsCollector(stats))

	want := `
# HELP hedge_budget_exhausted_total Number of hedges suppressed because the hedging budget was exhausted.
# TYPE hedge_budget_exhausted_total counter
hedge_budget_exhausted_total 0
# HELP hedge_hedge_rate Ratio of hedged to total requests (HedgedRequests / TotalRequests).
# TYPE hedge_hedge_rate gauge
hedge_hedge_rate 0
# HELP hedge_hedge_wins_total Number of requests answered by the hedge request.
# TYPE hedge_hedge_wins_total counter
hedge_hedge_wins_total 0
# HELP hedge_hedged_requests_total Number of hedge (duplicate) requests issued.
# TYPE hedge_hedged_requests_total counter
hedge_hedged_requests_total 0
# HELP hedge_primary_wins_total Number of requests answered by the original request.
# TYPE hedge_primary_wins_total counter
hedge_primary_wins_total 0
# HELP hedge_requests_total Total number of requests processed by the hedged transport.
# TYPE hedge_requests_total counter
hedge_requests_total 0
# HELP hedge_warmup_requests_total Number of warmup requests seen before the sketch had enough samples.
# TYPE hedge_warmup_requests_total counter
hedge_warmup_requests_total 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestStatsCollectorMetricsAreWellFormed(t *testing.T) {
	stats := &hedge.Stats{}

	reg := prometheus.NewRegistry()
	reg.MustRegister(prom.NewStatsCollector(stats))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	problems, err := promlint.NewWithMetricFamilies(mfs).Lint()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("lint problems: %v", problems)
	}
}

func TestNewStatsCollectorNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil stats")
		}
	}()

	prom.NewStatsCollector(nil)
}
