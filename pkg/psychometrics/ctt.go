// Package psychometrics holds measurement math with no knowledge of the domain:
// classical test theory now, IRT for CAT later (DOCS/PLAN/03-CBT-CAT.md).
package psychometrics

import (
	"math"
	"slices"
)

// Summary describes a sample.
type Summary struct {
	N      int
	Mean   float64
	SD     float64 // sample standard deviation (n-1); 0 when N < 2
	Min    float64
	Max    float64
	Median float64
}

// Describe summarizes xs. A zero Summary is returned for an empty sample.
func Describe(xs []float64) Summary {
	if len(xs) == 0 {
		return Summary{}
	}
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	s := Summary{N: len(xs), Min: sorted[0], Max: sorted[len(sorted)-1], Mean: Mean(xs), SD: SD(xs)}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		s.Median = sorted[mid]
	} else {
		s.Median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return s
}

// Mean of xs (0 when empty).
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// Variance is the sample variance (n-1); 0 when fewer than 2 values.
func Variance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := Mean(xs)
	ss := 0.0
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return ss / float64(len(xs)-1)
}

// SD is the sample standard deviation.
func SD(xs []float64) float64 { return math.Sqrt(Variance(xs)) }

// Pearson returns the correlation of x and y, or ok=false when it is undefined
// (fewer than 2 pairs, different lengths, or a constant variable). With a 0/1 x it is
// the point-biserial correlation.
func Pearson(x, y []float64) (r float64, ok bool) {
	if len(x) != len(y) || len(x) < 2 {
		return 0, false
	}
	mx, my := Mean(x), Mean(y)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// Alpha returns Cronbach's alpha for a persons × items score matrix (KR-20 when every
// item is scored 0/1). ok=false when undefined: fewer than 2 items or persons, ragged
// rows, or zero total variance.
func Alpha(scores [][]float64) (alpha float64, ok bool) {
	n := len(scores)
	if n < 2 || len(scores[0]) < 2 {
		return 0, false
	}
	k := len(scores[0])
	totals := make([]float64, n)
	itemVar := 0.0
	col := make([]float64, n)
	for j := range k {
		for i := range n {
			if len(scores[i]) != k {
				return 0, false
			}
			col[i] = scores[i][j]
			totals[i] += scores[i][j]
		}
		itemVar += Variance(col)
	}
	totalVar := Variance(totals)
	if totalVar == 0 {
		return 0, false
	}
	return float64(k) / float64(k-1) * (1 - itemVar/totalVar), true
}

// Histogram counts values in equal-width bins over [lo, hi]; the top edge falls in the last bin.
func Histogram(xs []float64, lo, hi float64, bins int) []int {
	out := make([]int, bins)
	width := (hi - lo) / float64(bins)
	for _, x := range xs {
		i := int((x - lo) / width)
		out[min(max(i, 0), bins-1)]++
	}
	return out
}
