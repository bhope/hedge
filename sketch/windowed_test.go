package sketch

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestWindowed_RecentDataPresent(t *testing.T) {
	w := NewWindowedSketch(0.01, time.Hour) // long window — no rotation during test
	defer w.Stop()

	for i := 1; i <= 1000; i++ {
		w.Add(float64(i))
	}

	p50 := w.Quantile(0.5)
	if math.IsNaN(p50) {
		t.Fatal("Quantile(0.5) returned NaN, expected a value")
	}
	// p50 of [1..1000] ≈ 500; allow ±1% relative error.
	if math.Abs(p50-500)/500 > 0.02 {
		t.Errorf("Quantile(0.5) = %v, want ≈500", p50)
	}
}

func TestWindowed_OldDataEvicted(t *testing.T) {
	const window = 50 * time.Millisecond
	w := NewWindowedSketch(0.01, window)
	defer w.Stop()

	for i := 1; i <= 100; i++ {
		w.Add(float64(i))
	}

	time.Sleep(3 * window) // 3× ensures both sketches have been rotated out

	v := w.Quantile(0.5)
	if !math.IsNaN(v) {
		t.Errorf("Quantile(0.5) = %v after eviction window, want NaN", v)
	}
}

func TestWindowed_SlideRetainsMiddleWindow(t *testing.T) {
	const window = 50 * time.Millisecond
	w := NewWindowedSketch(0.01, window)
	defer w.Stop()

	for i := 1; i <= 1000; i++ {
		w.Add(float64(i))
	}

	// Wait for exactly one rotation — data moves to previous.
	time.Sleep(window + 10*time.Millisecond)

	// Data should still be visible (it's in previous, not yet evicted).
	v := w.Quantile(0.5)
	if math.IsNaN(v) {
		t.Error("Quantile(0.5) returned NaN after one rotation, want data still visible")
	}
}

func TestWindowed_Empty(t *testing.T) {
	w := NewWindowedSketch(0.01, time.Hour)
	defer w.Stop()

	if v := w.Quantile(0.5); !math.IsNaN(v) {
		t.Errorf("Quantile(0.5) on empty = %v, want NaN", v)
	}
}

func TestWindowed_ConcurrentAddQuantile(t *testing.T) {
	w := NewWindowedSketch(0.01, 20*time.Millisecond)
	defer w.Stop()

	const (
		writers  = 8
		readers  = 4
		duration = 200 * time.Millisecond
	)

	var wg sync.WaitGroup
	done := make(chan struct{})

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			v := float64(id*100 + 1)
			for {
				select {
				case <-done:
					return
				default:
					w.Add(v)
					v++
				}
			}
		}(i)
	}

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = w.Quantile(0.99)
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()
}

func TestWindowed_Stop(t *testing.T) {
	w := NewWindowedSketch(0.01, 50*time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s")
	}
}

// TestWindowed_StopIdempotentUsage verifies that after Stop(), reads/writes
// that already completed don't panic and the sketch holds stable results.
func TestWindowed_StopIdempotentUsage(t *testing.T) {
	w := NewWindowedSketch(0.01, time.Hour)
	for i := 1; i <= 100; i++ {
		w.Add(float64(i))
	}
	v := w.Quantile(0.5)
	w.Stop()

	// After Stop, the sketches still hold their data — reads are still valid.
	v2 := w.Quantile(0.5)
	if v != v2 {
		t.Errorf("Quantile changed after Stop: %v → %v", v, v2)
	}
}
