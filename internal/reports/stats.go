package reports

import (
	"math"
	"slices"
)

// Stat summarises a set of durations in seconds. Count is zero when nothing was measured, in
// which case Median and P90 carry no meaning.
type Stat struct {
	Count  int
	Median float64
	P90    float64
}

// Summarize computes the median and 90th percentile of values. Percentiles interpolate linearly
// between the two closest ranks, which is what PostgreSQL's percentile_cont does.
func Summarize(values []float64) Stat {
	if len(values) == 0 {
		return Stat{}
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return Stat{Count: len(sorted), Median: percentile(sorted, 0.5), P90: percentile(sorted, 0.9)}
}

func percentile(sorted []float64, p float64) float64 {
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// Rate is a count of met targets out of those that could be judged.
type Rate struct {
	Met, Total int
}

// Percent is Met/Total as a percentage, or false when nothing could be judged.
func (r Rate) Percent() (float64, bool) {
	if r.Total == 0 {
		return 0, false
	}
	return 100 * float64(r.Met) / float64(r.Total), true
}
