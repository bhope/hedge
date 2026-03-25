package hedge

import "sync/atomic"

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
	return StatsSnapshot{
		TotalRequests:   s.TotalRequests.Load(),
		HedgedRequests:  s.HedgedRequests.Load(),
		HedgeWins:       s.HedgeWins.Load(),
		PrimaryWins:     s.PrimaryWins.Load(),
		BudgetExhausted: s.BudgetExhausted.Load(),
		WarmupRequests:  s.WarmupRequests.Load(),
	}
}

func (s *Stats) HedgeRate() float64 {
	total := s.TotalRequests.Load()
	if total == 0 {
		return 0
	}
	return float64(s.HedgedRequests.Load()) / float64(total)
}
