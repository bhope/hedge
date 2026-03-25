package sketch

import (
	"math"
	"sync"
	"time"
)

const defaultWindowDuration = 30 * time.Second

// WindowedSketch maintains a sliding window over two DDSketches that rotate
// every windowDuration. Quantile queries merge both sketches, giving a window
// that spans 1x to 2x the configured duration. Add always writes to the
// current sketch.
//
// The rotation scheme:
//
//	t=0:  current=A, previous=empty
//	t=30: current=B, previous=A       (A covers [0,30))
//	t=60: current=C, previous=B       (A is dropped, B covers [30,60))
//
// At any point, Quantile sees at most 2 windows of data.
type WindowedSketch struct {
	mu               sync.RWMutex
	relativeAccuracy float64
	current          *DDSketch
	previous         *DDSketch
	stop             chan struct{}
	done             chan struct{}
}

// NewWindowedSketch creates a WindowedSketch that rotates every windowDuration.
// Pass 0 to use the default of 30s.
func NewWindowedSketch(relativeAccuracy float64, windowDuration time.Duration) *WindowedSketch {
	if windowDuration <= 0 {
		windowDuration = defaultWindowDuration
	}
	w := &WindowedSketch{
		relativeAccuracy: relativeAccuracy,
		current:          NewDDSketch(relativeAccuracy),
		previous:         NewDDSketch(relativeAccuracy),
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
	}
	go w.rotateLoop(windowDuration)
	return w
}

func (w *WindowedSketch) Add(value float64) {
	w.mu.Lock()
	w.current.Add(value)
	w.mu.Unlock()
}

// Quantile returns the estimated quantile q ∈ [0,1] over the sliding window.
// Returns math.NaN() if no data has been recorded.
func (w *WindowedSketch) Quantile(q float64) float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.current.Count() == 0 && w.previous.Count() == 0 {
		return math.NaN()
	}

	// Merge under the read lock so no concurrent Add can mutate current
	// while we are iterating its buckets.
	merged := NewDDSketch(w.relativeAccuracy)
	merged.Merge(w.previous)
	merged.Merge(w.current)
	return merged.Quantile(q)
}

// Stop shuts down the background rotation goroutine and waits for it to exit.
func (w *WindowedSketch) Stop() {
	close(w.stop)
	<-w.done
}

func (w *WindowedSketch) rotateLoop(d time.Duration) {
	defer close(w.done)
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.rotate()
		case <-w.stop:
			return
		}
	}
}

func (w *WindowedSketch) rotate() {
	w.mu.Lock()
	w.previous = w.current
	w.current = NewDDSketch(w.relativeAccuracy)
	w.mu.Unlock()
}
