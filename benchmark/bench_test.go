package benchmark_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bhope/hedge"
	"github.com/bhope/hedge/budget"
	"github.com/bhope/hedge/sketch"
)

func BenchmarkRoundTripBaseline(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := http.DefaultTransport

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
			resp, err := tr.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	})
}

func BenchmarkRoundTripNoHedge(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := hedge.New(http.DefaultTransport, hedge.WithMinDelay(time.Hour))

	for i := 0; i < 25; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, _ := tr.RoundTrip(req)
		resp.Body.Close()
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
			resp, err := tr.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	})
}

func BenchmarkRoundTripWithHedge(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	tr := hedge.New(http.DefaultTransport,
		hedge.WithMinDelay(time.Millisecond),
		hedge.WithPercentile(0.5),
		hedge.WithBudgetPercent(100),
	)

	for i := 0; i < 25; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, _ := tr.RoundTrip(req)
		resp.Body.Close()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, err := tr.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
	}
}

func BenchmarkDDSketchAdd(b *testing.B) {
	s := sketch.NewDDSketch(0.01)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Add(float64(i%1000+1) * 1e6)
	}
}

func BenchmarkTokenBucketTryAcquire(b *testing.B) {
	tb := budget.NewTokenBucket(10, 1000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tb.TryAcquire()
		}
	})
}
