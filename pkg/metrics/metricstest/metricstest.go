// Package metricstest installs an in-memory meter provider for tests and reads back
// what was recorded.
package metricstest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Reader collects the metrics recorded after Install.
type Reader struct {
	t      testing.TB
	reader *sdkmetric.ManualReader
}

// Install sets a fresh global meter provider for the test and restores the previous
// one afterwards. Create the instruments under test after calling it. Tests using it
// must not run in parallel.
func Install(t testing.TB) *Reader {
	t.Helper()
	prev := otel.GetMeterProvider()
	r := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(r)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	return &Reader{t: t, reader: r}
}

// Point is one data point: its attributes and value (sum, gauge value or histogram count).
type Point struct {
	Attrs map[string]string
	Value float64
}

// Points returns the data points of the named instrument.
func (r *Reader) Points(name string) []Point {
	r.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		r.t.Fatalf("collect metrics: %v", err)
	}
	var out []Point
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{attrs(p.Attributes), float64(p.Value)})
				}
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{attrs(p.Attributes), p.Value})
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{attrs(p.Attributes), float64(p.Value)})
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{attrs(p.Attributes), float64(p.Count)})
				}
			case metricdata.Histogram[int64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{attrs(p.Attributes), float64(p.Count)})
				}
			}
		}
	}
	return out
}

// Value sums the points of name whose attributes include every pair in match.
func (r *Reader) Value(name string, match map[string]string) float64 {
	r.t.Helper()
	var total float64
	for _, p := range r.Points(name) {
		ok := true
		for k, v := range match {
			if p.Attrs[k] != v {
				ok = false
				break
			}
		}
		if ok {
			total += p.Value
		}
	}
	return total
}

func attrs(s attribute.Set) map[string]string {
	out := map[string]string{}
	for _, kv := range s.ToSlice() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}
