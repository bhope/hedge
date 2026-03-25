package sketch

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

// exactQuantile computes the exact quantile from a dataset by sorting.
// Uses the same ceil-rank convention as DDSketch.Quantile.
func exactQuantile(data []float64, q float64) float64 {
	if len(data) == 0 {
		return math.NaN()
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	n := len(sorted)
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	// 0-indexed: the rank-th element (1-indexed) is at sorted[rank-1].
	rank := int(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// checkAccuracy verifies that every tested quantile is within ±relativeAccuracy
// of the exact value. Skips quantiles where the true value is zero.
func checkAccuracy(t *testing.T, s *DDSketch, data []float64, relativeAccuracy float64) {
	t.Helper()
	quantiles := []float64{0.1, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99, 0.999}
	for _, q := range quantiles {
		est := s.Quantile(q)
		trueVal := exactQuantile(data, q)
		if trueVal == 0 {
			continue
		}
		relErr := math.Abs(est-trueVal) / math.Abs(trueVal)
		// Allow a tiny floating-point epsilon above the theoretical bound.
		if relErr > relativeAccuracy+1e-9 {
			t.Errorf("Quantile(%.3f): est=%.6g, true=%.6g, relErr=%.4f%% > %.4f%%",
				q, est, trueVal, relErr*100, relativeAccuracy*100)
		}
	}
}

// --- Distribution helpers ---

func uniformSamples(n int, lo, hi float64) []float64 {
	data := make([]float64, n)
	for i := range data {
		data[i] = lo + rand.Float64()*(hi-lo)
	}
	return data
}

// normalPositive generates samples from a normal distribution shifted to be
// strictly positive: N(mean, stddev) + offset.
func normalPositiveSamples(n int, mean, stddev float64) []float64 {
	data := make([]float64, n)
	for i := range data {
		data[i] = mean + rand.NormFloat64()*stddev
	}
	return data
}

// lognormalSamples generates samples from LogNormal(mu, sigma).
// All values are strictly positive and the distribution is heavy-tailed.
func lognormalSamples(n int, mu, sigma float64) []float64 {
	data := make([]float64, n)
	for i := range data {
		data[i] = math.Exp(mu + rand.NormFloat64()*sigma)
	}
	return data
}

// --- Correctness tests ---

func TestQuantileAccuracy_Uniform(t *testing.T) {
	const (
		n                = 10_000
		relativeAccuracy = 0.01
	)
	data := uniformSamples(n, 1, 1000)
	s := NewDDSketch(relativeAccuracy)
	for _, v := range data {
		s.Add(v)
	}
	if s.Count() != int64(n) {
		t.Fatalf("Count() = %d, want %d", s.Count(), n)
	}
	checkAccuracy(t, s, data, relativeAccuracy)
}

func TestQuantileAccuracy_Normal(t *testing.T) {
	// Normal distribution centered at 500ms, stddev 100ms.
	// Adding a floor ensures all values are positive.
	const (
		n                = 10_000
		relativeAccuracy = 0.01
		mean             = 500.0
		stddev           = 100.0
	)
	data := normalPositiveSamples(n, mean, stddev)
	// Clamp to positive — DDSketch relative-error applies to non-zero values.
	for i, v := range data {
		if v <= 0 {
			data[i] = 1.0
		}
	}
	s := NewDDSketch(relativeAccuracy)
	for _, v := range data {
		s.Add(v)
	}
	checkAccuracy(t, s, data, relativeAccuracy)
}

func TestQuantileAccuracy_Lognormal(t *testing.T) {
	// LogNormal(0, 1): median ≈ 1, mean ≈ 1.65, p99 ≈ 10.2.
	// Heavy tail makes this the most demanding test for tail accuracy.
	const (
		n                = 10_000
		relativeAccuracy = 0.01
	)
	data := lognormalSamples(n, 0, 1)
	s := NewDDSketch(relativeAccuracy)
	for _, v := range data {
		s.Add(v)
	}
	checkAccuracy(t, s, data, relativeAccuracy)
}

func TestQuantileAccuracy_NegativeValues(t *testing.T) {
	// All negative values (e.g., temperature deviations below zero).
	const (
		n                = 10_000
		relativeAccuracy = 0.01
	)
	data := make([]float64, n)
	for i := range data {
		// Uniform in [−1000, −1].
		data[i] = -(1 + rand.Float64()*999)
	}
	s := NewDDSketch(relativeAccuracy)
	for _, v := range data {
		s.Add(v)
	}
	checkAccuracy(t, s, data, relativeAccuracy)
}

func TestQuantileAccuracy_MixedSigns(t *testing.T) {
	// Mix of negative, zero, and positive values.
	const (
		n                = 10_000
		relativeAccuracy = 0.01
	)
	data := make([]float64, n)
	for i := range data {
		data[i] = (rand.Float64() - 0.5) * 200 // uniform in [−100, 100]
	}
	s := NewDDSketch(relativeAccuracy)
	for _, v := range data {
		s.Add(v)
	}
	checkAccuracy(t, s, data, relativeAccuracy)
}

// --- Boundary / edge-case tests ---

func TestQuantile_Empty(t *testing.T) {
	s := NewDDSketch(0.01)
	if v := s.Quantile(0.5); !math.IsNaN(v) {
		t.Errorf("Quantile on empty sketch: got %v, want NaN", v)
	}
}

func TestQuantile_SingleValue(t *testing.T) {
	s := NewDDSketch(0.01)
	s.Add(42.0)
	for _, q := range []float64{0, 0.1, 0.5, 0.9, 1.0} {
		v := s.Quantile(q)
		relErr := math.Abs(v-42.0) / 42.0
		if relErr > 0.01+1e-9 {
			t.Errorf("Quantile(%v) = %v, want ≈42 (relErr %.4f%%)", q, v, relErr*100)
		}
	}
}

func TestQuantile_MinMax(t *testing.T) {
	s := NewDDSketch(0.01)
	data := uniformSamples(1000, 1, 100)
	for _, v := range data {
		s.Add(v)
	}
	if got := s.Quantile(0); got != s.min {
		t.Errorf("Quantile(0) = %v, want min %v", got, s.min)
	}
	if got := s.Quantile(1); got != s.max {
		t.Errorf("Quantile(1) = %v, want max %v", got, s.max)
	}
}

func TestQuantile_IgnoresNaNInf(t *testing.T) {
	s := NewDDSketch(0.01)
	s.Add(1.0)
	s.Add(math.NaN())
	s.Add(math.Inf(1))
	s.Add(math.Inf(-1))
	if s.Count() != 1 {
		t.Errorf("Count() = %d after adding NaN/Inf, want 1", s.Count())
	}
}

// --- Merge tests ---

func TestMerge_SameAsUnified(t *testing.T) {
	// Split 10k lognormal samples across two sketches. After merging, every
	// quantile should match a single sketch built from all samples.
	const (
		n                = 10_000
		relativeAccuracy = 0.01
	)
	data := lognormalSamples(n, 0, 1)

	unified := NewDDSketch(relativeAccuracy)
	sA := NewDDSketch(relativeAccuracy)
	sB := NewDDSketch(relativeAccuracy)

	for i, v := range data {
		unified.Add(v)
		if i < n/2 {
			sA.Add(v)
		} else {
			sB.Add(v)
		}
	}
	sA.Merge(sB)

	if sA.Count() != unified.Count() {
		t.Fatalf("merged Count()=%d, want %d", sA.Count(), unified.Count())
	}

	for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 0.999} {
		merged := sA.Quantile(q)
		single := unified.Quantile(q)
		// Both estimates are within ±α of the true value, so they should
		// agree within 2α of each other (triangle inequality).
		if single == 0 {
			continue
		}
		relDiff := math.Abs(merged-single) / math.Abs(single)
		if relDiff > 2*relativeAccuracy+1e-9 {
			t.Errorf("Quantile(%.3f): merged=%.6g, single=%.6g, relDiff=%.4f%%",
				q, merged, single, relDiff*100)
		}
	}
}

func TestMerge_WithEmpty(t *testing.T) {
	s := NewDDSketch(0.01)
	data := uniformSamples(1000, 1, 100)
	for _, v := range data {
		s.Add(v)
	}
	empty := NewDDSketch(0.01)

	// Merge empty into s: s must be unchanged.
	before := s.Quantile(0.99)
	s.Merge(empty)
	after := s.Quantile(0.99)
	if before != after {
		t.Errorf("merge with empty changed p99: %v → %v", before, after)
	}

	// Merge s into empty: empty must equal s.
	empty.Merge(s)
	if empty.Count() != s.Count() {
		t.Errorf("empty after merge Count()=%d, want %d", empty.Count(), s.Count())
	}
}

func TestMerge_NegativeAndPositive(t *testing.T) {
	// Merge a sketch of negatives with a sketch of positives.
	neg := NewDDSketch(0.01)
	pos := NewDDSketch(0.01)
	all := NewDDSketch(0.01)

	for i := 1; i <= 1000; i++ {
		neg.Add(-float64(i))
		pos.Add(float64(i))
		all.Add(-float64(i))
		all.Add(float64(i))
	}
	neg.Merge(pos)

	if neg.Count() != all.Count() {
		t.Fatalf("merged Count()=%d, want %d", neg.Count(), all.Count())
	}
	// p50 of symmetric ±[1..1000] should be near 0.
	p50 := neg.Quantile(0.5)
	if math.Abs(p50) > 2 { // within a couple of units
		t.Errorf("p50 of symmetric ±[1..1000] = %v, expected near 0", p50)
	}
}

// --- Reset tests ---

func TestReset(t *testing.T) {
	s := NewDDSketch(0.01)
	for _, v := range uniformSamples(1000, 1, 100) {
		s.Add(v)
	}
	s.Reset()

	if s.Count() != 0 {
		t.Errorf("Count() after Reset() = %d, want 0", s.Count())
	}
	if v := s.Quantile(0.5); !math.IsNaN(v) {
		t.Errorf("Quantile(0.5) after Reset() = %v, want NaN", v)
	}
	if len(s.positive.bins) != 0 {
		t.Errorf("positive.bins not cleared after Reset()")
	}
	if len(s.negative.bins) != 0 {
		t.Errorf("negative.bins not cleared after Reset()")
	}
	if s.zeroCount != 0 {
		t.Errorf("zeroCount = %v after Reset(), want 0", s.zeroCount)
	}
	if s.min != math.MaxFloat64 {
		t.Errorf("min not reset: %v", s.min)
	}
	if s.max != -math.MaxFloat64 {
		t.Errorf("max not reset: %v", s.max)
	}
}

func TestReset_ThenReuse(t *testing.T) {
	s := NewDDSketch(0.01)
	data1 := uniformSamples(1000, 1, 100)
	for _, v := range data1 {
		s.Add(v)
	}
	s.Reset()

	data2 := lognormalSamples(10_000, 0, 1)
	for _, v := range data2 {
		s.Add(v)
	}
	if s.Count() != int64(len(data2)) {
		t.Fatalf("Count() = %d after reset+reuse, want %d", s.Count(), len(data2))
	}
	checkAccuracy(t, s, data2, 0.01)
}

// --- Constructor validation ---

func TestNewDDSketch_PanicOnInvalidAccuracy(t *testing.T) {
	for _, bad := range []float64{0, -0.1, 1.0, 1.5} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("NewDDSketch(%v) should panic", bad)
				}
			}()
			NewDDSketch(bad)
		}()
	}
}

// --- Benchmarks ---

func BenchmarkAdd(b *testing.B) {
	s := NewDDSketch(0.01)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Vary values over a wide range to exercise the bucket map.
		s.Add(float64(i%10_000)*0.01 + 1)
	}
}

func BenchmarkAdd_Lognormal(b *testing.B) {
	s := NewDDSketch(0.01)
	samples := lognormalSamples(b.N+1, 0, 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Add(samples[i])
	}
}

func BenchmarkQuantile(b *testing.B) {
	s := NewDDSketch(0.01)
	for _, v := range lognormalSamples(10_000, 0, 1) {
		s.Add(v)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Quantile(0.99)
	}
}

func BenchmarkQuantile_P50(b *testing.B) {
	s := NewDDSketch(0.01)
	for _, v := range uniformSamples(10_000, 1, 1000) {
		s.Add(v)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Quantile(0.50)
	}
}

func BenchmarkMerge(b *testing.B) {
	base := NewDDSketch(0.01)
	for _, v := range lognormalSamples(10_000, 0, 1) {
		base.Add(v)
	}
	other := NewDDSketch(0.01)
	for _, v := range lognormalSamples(10_000, 0, 1) {
		other.Add(v)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst := NewDDSketch(0.01)
		dst.Merge(base)
		dst.Merge(other)
	}
}
