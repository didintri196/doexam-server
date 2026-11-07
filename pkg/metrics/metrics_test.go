package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

func TestSetupServesPrometheus(t *testing.T) {
	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	m, err := Setup(context.Background(), Config{Addr: "127.0.0.1:0", Token: "s3cret", ServiceName: "test", Version: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	c, _ := otel.Meter("test").Int64Counter("doexam.test.things", metric.WithUnit("{thing}"))
	c.Add(context.Background(), 3)
	if err := RegisterDependencies(map[string]Check{
		"up":   func(context.Context) error { return nil },
		"down": func(context.Context) error { return errors.New("down") },
	}, time.Minute); err != nil {
		t.Fatal(err)
	}

	url := "http://" + m.Addr() + "/metrics"
	get := func(auth string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("without token: status %d, want 401", code)
	}
	if code, _ := get("Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d, want 401", code)
	}
	code, body := get("Bearer s3cret")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	for _, want := range []string{
		`doexam_build_info{go_version=`,
		`version="v9.9.9"`,
		`doexam_test_things_total 3`,
		`doexam_dependency_up{dependency="up"} 1`,
		`doexam_dependency_up{dependency="down"} 0`,
		`go_goroutines `,
		`go_sched_latencies_seconds`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output lacks %q", want)
		}
	}
	if strings.Contains(body, "otel_scope_name") {
		t.Error("scope labels should be disabled")
	}
}

func TestCached(t *testing.T) {
	calls := 0
	now := time.Unix(1000, 0)
	c := NewCached(30*time.Second, func(context.Context) (int, error) {
		calls++
		if calls == 3 {
			return 0, errors.New("db down")
		}
		return calls * 10, nil
	})
	c.now = func() time.Time { return now }
	ctx := context.Background()
	if v, _ := c.Get(ctx); v != 10 || calls != 1 {
		t.Fatalf("first get = %d (calls %d)", v, calls)
	}
	now = now.Add(10 * time.Second)
	if v, _ := c.Get(ctx); v != 10 || calls != 1 {
		t.Fatalf("within interval = %d (calls %d), want cached 10", v, calls)
	}
	now = now.Add(30 * time.Second)
	if v, _ := c.Get(ctx); v != 20 || calls != 2 {
		t.Fatalf("after interval = %d (calls %d), want 20", v, calls)
	}
	now = now.Add(31 * time.Second)
	if v, err := c.Get(ctx); err == nil || v != 20 {
		t.Fatalf("failed reload = %d, %v; want previous value 20 and the error", v, err)
	}
}

func TestDiskUsage(t *testing.T) {
	avail, total, ok := diskUsage(t.TempDir())
	if !ok {
		t.Skip("disk usage not supported on this platform")
	}
	if total == 0 || avail > total {
		t.Fatalf("avail %d, total %d", avail, total)
	}
}
