package budget

import (
	"sync"
	"time"
)

// TokenBucket controls hedge request rate using a token bucket algorithm.
type TokenBucket struct {
	mu            sync.Mutex
	rate          float64 // tokens per second
	tokens        float64
	maxBurst      float64
	lastRefill    time.Time
	budgetPercent float64
}

// NewTokenBucket creates a bucket allowing budgetPercent% of estimatedRPS as hedges.
func NewTokenBucket(budgetPercent float64, estimatedRPS float64) *TokenBucket {
	rate := estimatedRPS * (budgetPercent / 100)
	maxBurst := rate * 2
	if maxBurst < 1 {
		maxBurst = 1
	}
	return &TokenBucket{
		rate:          rate,
		tokens:        maxBurst,
		maxBurst:      maxBurst,
		lastRefill:    time.Now(),
		budgetPercent: budgetPercent,
	}
}

// TryAcquire returns true if a hedge token is available, false if over budget.
func (tb *TokenBucket) TryAcquire() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.maxBurst {
		tb.tokens = tb.maxBurst
	}

	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

// SetRPS updates the hedge rate as traffic changes, preserving the budget percentage.
func (tb *TokenBucket) SetRPS(rps float64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.rate = rps * (tb.budgetPercent / 100)
	maxBurst := tb.rate * 2
	if maxBurst < 1 {
		maxBurst = 1
	}
	tb.maxBurst = maxBurst
	if tb.tokens > tb.maxBurst {
		tb.tokens = tb.maxBurst
	}
}
