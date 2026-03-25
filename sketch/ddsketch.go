// Package sketch implements a DDSketch streaming quantile estimator.
//
// Based on Masson et al., "DDSketch: A fast and fully-mergeable quantile sketch
// with relative-error guarantees", VLDB 2019.
package sketch

import (
	"math"
	"sort"
)

// LogMapping maps values to bucket indices using logarithmic scaling.
//
// For a given relative accuracy α, γ = (1+α)/(1-α).
// A positive value x maps to bucket index ⌈log_γ(x)⌉ = ⌈ln(x)/ln(γ)⌉.
//
// The guarantee: any value in bucket i is within a factor of γ^0.5 of the
// bucket's representative value, giving relative error ≤ α.
type LogMapping struct {
	gamma      float64 // (1+α)/(1-α)
	multiplier float64 // 1/ln(γ), precomputed for fast index calculation
}

func newLogMapping(relativeAccuracy float64) LogMapping {
	gamma := (1 + relativeAccuracy) / (1 - relativeAccuracy)
	return LogMapping{
		gamma:      gamma,
		multiplier: 1.0 / math.Log(gamma),
	}
}

// index returns the bucket index for a strictly positive value x.
func (m *LogMapping) index(x float64) int {
	return int(math.Ceil(math.Log(x) * m.multiplier))
}

// value returns the representative value for a bucket at the given index.
// Returns γ^(index−0.5), the geometric midpoint of the bucket boundaries
// [γ^(index−1), γ^index].
func (m *LogMapping) value(index int) float64 {
	return math.Exp((float64(index) - 0.5) / m.multiplier)
}

// Store is a sparse map of bucket indices to cumulative counts.
type Store struct {
	bins  map[int]float64
	count float64
}

func newStore() *Store {
	return &Store{bins: make(map[int]float64)}
}

func (s *Store) add(index int) {
	s.bins[index]++
	s.count++
}

func (s *Store) merge(other *Store) {
	for idx, cnt := range other.bins {
		s.bins[idx] += cnt
	}
	s.count += other.count
}

func (s *Store) reset() {
	s.bins = make(map[int]float64)
	s.count = 0
}

// DDSketch implements a streaming quantile sketch with relative-error guarantees.
//
// Positive and negative values are stored in separate sparse bucket maps.
// Zero values are counted separately. Min and max are tracked exactly for
// boundary queries.
//
// Property: for any quantile q, the returned estimate satisfies
//
//	|estimate − trueValue| / |trueValue| ≤ relativeAccuracy
type DDSketch struct {
	mapping   LogMapping
	positive  *Store
	negative  *Store
	zeroCount float64
	count     int64
	min, max  float64
}

// NewDDSketch creates a sketch with the given relative accuracy.
// α = 0.01 means quantile estimates are within ±1% of the true value.
// Panics if relativeAccuracy is not in (0, 1).
func NewDDSketch(relativeAccuracy float64) *DDSketch {
	if relativeAccuracy <= 0 || relativeAccuracy >= 1 {
		panic("relativeAccuracy must be in (0, 1)")
	}
	return &DDSketch{
		mapping:  newLogMapping(relativeAccuracy),
		positive: newStore(),
		negative: newStore(),
		min:      math.MaxFloat64,
		max:      -math.MaxFloat64,
	}
}

// Add records a single value. O(1) per insert.
// NaN and infinite values are silently ignored.
func (s *DDSketch) Add(value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	if value > 0 {
		s.positive.add(s.mapping.index(value))
	} else if value < 0 {
		s.negative.add(s.mapping.index(-value))
	} else {
		s.zeroCount++
	}
	s.count++
	if value < s.min {
		s.min = value
	}
	if value > s.max {
		s.max = value
	}
}

// Quantile returns the estimated value at quantile q ∈ [0, 1].
// Returns math.NaN() if the sketch is empty.
//
// The estimate satisfies the relative-error guarantee:
//
//	|estimate − trueValue| / |trueValue| ≤ relativeAccuracy
func (s *DDSketch) Quantile(q float64) float64 {
	if s.count == 0 {
		return math.NaN()
	}
	if q <= 0 {
		return s.min
	}
	if q >= 1 {
		return s.max
	}

	// rank is 1-indexed: we want the rank-th smallest value.
	rank := math.Ceil(q * float64(s.count))

	// Negative values: higher bucket index ↔ larger absolute value ↔ more negative.
	// Iterate descending (most negative → least negative).
	if s.negative.count > 0 {
		keys := sortedKeys(s.negative.bins, true /* descending */)
		var cumulative float64
		for _, idx := range keys {
			cumulative += s.negative.bins[idx]
			if cumulative >= rank {
				return -s.mapping.value(idx)
			}
		}
		rank -= s.negative.count
	}

	if s.zeroCount > 0 {
		rank -= s.zeroCount
		if rank <= 0 {
			return 0
		}
	}

	// Positive values: lower bucket index ↔ smaller value.
	// Iterate ascending (least positive → most positive).
	if s.positive.count > 0 {
		keys := sortedKeys(s.positive.bins, false /* ascending */)
		var cumulative float64
		for _, idx := range keys {
			cumulative += s.positive.bins[idx]
			if cumulative >= rank {
				return s.mapping.value(idx)
			}
		}
	}

	return s.max
}

// Merge combines other into s. The merge is exact: no additional error accumulates
// regardless of how many sketches are merged.
func (s *DDSketch) Merge(other *DDSketch) {
	s.positive.merge(other.positive)
	s.negative.merge(other.negative)
	s.zeroCount += other.zeroCount
	s.count += other.count
	if other.count > 0 {
		if other.min < s.min {
			s.min = other.min
		}
		if other.max > s.max {
			s.max = other.max
		}
	}
}

func (s *DDSketch) Count() int64 {
	return s.count
}

// Reset clears all state. Used for windowed / tumbling-window decay.
func (s *DDSketch) Reset() {
	s.positive.reset()
	s.negative.reset()
	s.zeroCount = 0
	s.count = 0
	s.min = math.MaxFloat64
	s.max = -math.MaxFloat64
}

// sortedKeys returns the keys of m sorted ascending or descending.
func sortedKeys(m map[int]float64, descending bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if descending {
		sort.Sort(sort.Reverse(sort.IntSlice(keys)))
	} else {
		sort.Ints(keys)
	}
	return keys
}
