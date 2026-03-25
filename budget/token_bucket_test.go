package budget

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimit(t *testing.T) {
	tb := NewTokenBucket(5, 100) // rate = 5/sec, maxBurst = 10

	for tb.TryAcquire() {
	}

	time.Sleep(time.Second)

	allowed := 0
	for i := 0; i < 20; i++ {
		if tb.TryAcquire() {
			allowed++
		}
	}

	if allowed < 4 || allowed > 7 {
		t.Errorf("expected ~5 hedges/sec, got %d", allowed)
	}
}

func TestBurst(t *testing.T) {
	tb := NewTokenBucket(5, 100) // maxBurst = 10

	allowed := 0
	for i := 0; i < 20; i++ {
		if tb.TryAcquire() {
			allowed++
		}
	}

	if allowed != 10 {
		t.Errorf("expected 10 burst tokens, got %d", allowed)
	}
	if tb.TryAcquire() {
		t.Error("expected rejection after burst exhausted")
	}
}

func TestRefill(t *testing.T) {
	tb := NewTokenBucket(5, 100)
	for tb.TryAcquire() {
	} // drain

	time.Sleep(400 * time.Millisecond) // expect ~2 tokens

	got := 0
	for tb.TryAcquire() {
		got++
	}
	if got < 1 || got > 4 {
		t.Errorf("expected 1-4 refilled tokens after 400ms, got %d", got)
	}
}

func TestConcurrent(t *testing.T) {
	tb := NewTokenBucket(50, 1000) // generous budget for the test

	var wg sync.WaitGroup
	var allowed atomic.Int64

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tb.TryAcquire() {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if allowed.Load() == 0 {
		t.Error("expected at least one hedge to be allowed")
	}
}

func BenchmarkTryAcquire(b *testing.B) {
	tb := NewTokenBucket(50, float64(b.N)*10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.TryAcquire()
	}
}
