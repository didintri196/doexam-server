package psychometrics

import (
	"math"
	"slices"
	"testing"
)

// Reference values computed independently with Python's statistics module.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

func TestAlphaKR20(t *testing.T) {
	scores := [][]float64{{1, 1, 1, 0}, {1, 1, 0, 0}, {1, 0, 0, 0}, {1, 1, 1, 1}, {0, 0, 0, 0}, {1, 1, 1, 0}}
	a, ok := Alpha(scores)
	if !ok || !near(a, 0.779487) {
		t.Fatalf("alpha = %v, %v; want 0.779487", a, ok)
	}
	if _, ok := Alpha([][]float64{{1}, {0}}); ok {
		t.Fatal("one item: undefined")
	}
	if _, ok := Alpha([][]float64{{1, 1}, {1, 1}}); ok {
		t.Fatal("zero variance: undefined")
	}
	if _, ok := Alpha([][]float64{{1, 0}, {1}}); ok {
		t.Fatal("ragged rows: undefined")
	}
}

func TestPearson(t *testing.T) {
	r, ok := Pearson([]float64{1, 1, 0, 1, 0, 1}, []float64{3, 2, 1, 4, 0, 3})
	if !ok || !near(r, 0.877058) {
		t.Fatalf("r = %v, %v; want 0.877058", r, ok)
	}
	if _, ok := Pearson([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Fatal("constant x: undefined")
	}
	if _, ok := Pearson([]float64{1}, []float64{1}); ok {
		t.Fatal("one pair: undefined")
	}
}

func TestDescribeAndHistogram(t *testing.T) {
	s := Describe([]float64{55, 70, 82, 90, 61})
	if s.N != 5 || s.Median != 70 || s.Min != 55 || s.Max != 90 || !near(s.Mean, 71.6) || !near(s.SD, 14.467204) {
		t.Fatalf("summary = %+v", s)
	}
	if Describe([]float64{1, 2, 3, 4}).Median != 2.5 {
		t.Fatal("even median")
	}
	if got := Histogram([]float64{0, 9.9, 10, 55, 100}, 0, 100, 10); !slices.Equal(got, []int{2, 1, 0, 0, 0, 1, 0, 0, 0, 1}) {
		t.Fatalf("histogram = %v", got)
	}
}
