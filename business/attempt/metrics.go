package attempt

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	modelAttempt "doexam/models/attempt"
	"doexam/pkg/errr"
	"doexam/pkg/metrics"
)

const (
	// staleAfter: a running attempt without a sync for longer is "stale" (the exam client
	// sends a heartbeat every ~15 s, so this is four missed heartbeats).
	staleAfter = 60 * time.Second
	// forgetAfter drops attempts that never came back (bounded memory).
	forgetAfter = 6 * time.Hour
)

// attemptMetrics holds the exam delivery instruments (doexam_attempt_*).
type attemptMetrics struct {
	started    metric.Int64Counter
	finished   metric.Int64Counter
	events     metric.Int64Counter
	rejected   metric.Int64Counter
	batch      metric.Int64Histogram
	lag        metric.Float64Histogram
	violations metric.Int64Counter
	timedOut   metric.Int64Counter
	proctor    metric.Int64Counter
	presence   *presence
}

func newAttemptMetrics(now func() time.Time) *attemptMetrics {
	m := otel.Meter("doexam.service.attempt")
	am := &attemptMetrics{presence: &presence{seen: map[string]time.Time{}}}
	var errs [11]error
	am.started, errs[0] = m.Int64Counter("doexam.attempt.starts", metric.WithUnit("{request}"),
		metric.WithDescription("Start/resume requests by result (started, resumed or why refused)."))
	am.finished, errs[1] = m.Int64Counter("doexam.attempt.finished", metric.WithUnit("{attempt}"),
		metric.WithDescription("Submitted attempts by reason (participant, timeout, proctor, session_closed)."))
	am.events, errs[2] = m.Int64Counter("doexam.attempt.events", metric.WithUnit("{event}"),
		metric.WithDescription("Client events received by sync, by type and outcome (accepted, duplicate, rejected)."))
	am.rejected, errs[3] = m.Int64Counter("doexam.attempt.events.rejected", metric.WithUnit("{event}"),
		metric.WithDescription("Client events rejected by sync, by type and reason."))
	am.batch, errs[4] = m.Int64Histogram("doexam.attempt.sync.batch.size", metric.WithUnit("{event}"),
		metric.WithDescription("Events per sync request (0 = heartbeat)."),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 5, 10, 25, 50, 100, 200))
	am.lag, errs[5] = m.Float64Histogram("doexam.attempt.sync.client_lag", metric.WithUnit("s"),
		metric.WithDescription("Time from an event being recorded on the client to the server accepting it."),
		metric.WithExplicitBucketBoundaries(0.5, 1, 2, 5, 15, 30, 60, 120, 300, 600, 1800))
	am.violations, errs[6] = m.Int64Counter("doexam.attempt.violations", metric.WithUnit("{event}"),
		metric.WithDescription("Accepted violation events (focus lost, fullscreen exit, device changed)."))
	am.timedOut, errs[7] = m.Int64Counter("doexam.attempt.sections.timed_out", metric.WithUnit("{section}"),
		metric.WithDescription("Sections closed by the clock (sequential mode)."))
	am.proctor, errs[8] = m.Int64Counter("doexam.attempt.proctor_actions", metric.WithUnit("{action}"),
		metric.WithDescription("Successful proctor actions on attempts."))
	connected, err9 := m.Int64ObservableGauge("doexam.attempts.connected", metric.WithUnit("{attempt}"),
		metric.WithDescription("Running attempts that synced with this node within the last 60 s."))
	stale, err10 := m.Int64ObservableGauge("doexam.attempts.stale", metric.WithUnit("{attempt}"),
		metric.WithDescription("Running attempts whose last sync with this node is older than 60 s."))
	errs[9], errs[10] = err9, err10
	metrics.Report(errs[:]...)
	_, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		c, s := am.presence.count(now())
		o.ObserveInt64(connected, int64(c))
		o.ObserveInt64(stale, int64(s))
		return nil
	}, connected, stale)
	metrics.Report(err)
	return am
}

func attrs(kv ...attribute.KeyValue) metric.MeasurementOption { return metric.WithAttributes(kv...) }

// startResult labels the outcome of Start.
func startResult(err error, resumed bool) string {
	var ve *errr.ValidationError
	switch {
	case err == nil && resumed:
		return "resumed"
	case err == nil:
		return "started"
	case errors.Is(err, errBadToken):
		return "bad_token"
	case errors.Is(err, errNotParticipant):
		return "not_participant"
	case errors.Is(err, errDeviceLocked):
		return "device_locked"
	case errors.As(err, &ve), errors.Is(err, errr.ErrInvalidInput):
		return "invalid"
	case errors.Is(err, errr.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errr.ErrNotFound):
		return "not_found"
	case errors.Is(err, errr.ErrConflict):
		return "conflict" // session not open, late entry closed, no attempts left, unpublished
	}
	return "error"
}

// eventType keeps the type label bounded to the known client event types.
func eventType(t string) string {
	if slices.Contains(modelAttempt.ClientEventTypes, t) {
		return t
	}
	return "other"
}

// rejectReason maps a rejection message to a bounded label.
func rejectReason(reason string) string {
	switch reason {
	case "attempt_closed", "after_deadline", "section_closed", "locked", "forward_only":
		return reason
	case "unknown event type":
		return "unknown_type"
	case "unknown attempt_item_id":
		return "unknown_item"
	}
	if strings.HasPrefix(reason, "client_event_id") {
		return "invalid_id"
	}
	return "invalid_payload"
}

// syncTally is what one sync batch did, recorded after the transaction commits.
type syncTally struct {
	accepted, duplicates map[string]int64
	rejected             map[[2]string]int64
	lags                 []float64
	violations           map[string]int64
}

func newSyncTally() *syncTally {
	return &syncTally{accepted: map[string]int64{}, duplicates: map[string]int64{}, rejected: map[[2]string]int64{}, violations: map[string]int64{}}
}

func (t *syncTally) reject(typ, reason string) {
	t.rejected[[2]string{eventType(typ), rejectReason(reason)}]++
}

func (t *syncTally) accept(ev modelAttempt.ClientEvent, now time.Time) {
	t.accepted[eventType(ev.Type)]++
	if ev.ClientTS != nil {
		t.lags = append(t.lags, max(now.Sub(*ev.ClientTS).Seconds(), 0))
	}
	if slices.Contains(modelAttempt.ViolationEventTypes, ev.Type) {
		t.violations[ev.Type]++
	}
}

func (m *attemptMetrics) recordSync(ctx context.Context, batch int, t *syncTally) {
	m.batch.Record(ctx, int64(batch))
	for typ, n := range t.accepted {
		m.events.Add(ctx, n, attrs(attribute.String("type", typ), attribute.String("outcome", "accepted")))
	}
	for typ, n := range t.duplicates {
		m.events.Add(ctx, n, attrs(attribute.String("type", typ), attribute.String("outcome", "duplicate")))
	}
	for k, n := range t.rejected {
		m.events.Add(ctx, n, attrs(attribute.String("type", k[0]), attribute.String("outcome", "rejected")))
		m.rejected.Add(ctx, n, attrs(attribute.String("type", k[0]), attribute.String("reason", k[1])))
	}
	for _, l := range t.lags {
		m.lag.Record(ctx, l)
	}
	for typ, n := range t.violations {
		m.violations.Add(ctx, n, attrs(attribute.String("type", typ)))
	}
}

// presence tracks when each running attempt last synced with this node. In a cluster
// without sticky sessions an attempt may look stale on a node it no longer talks to,
// so sum "connected" across nodes and read "stale" per node.
type presence struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (p *presence) touch(id string, at time.Time) {
	p.mu.Lock()
	p.seen[id] = at
	p.mu.Unlock()
}

func (p *presence) forget(id string) {
	p.mu.Lock()
	delete(p.seen, id)
	p.mu.Unlock()
}

// count returns attempts seen within staleAfter and older ones, forgetting abandoned ones.
func (p *presence) count(now time.Time) (connected, stale int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, at := range p.seen {
		switch age := now.Sub(at); {
		case age > forgetAfter:
			delete(p.seen, id)
		case age > staleAfter:
			stale++
		default:
			connected++
		}
	}
	return connected, stale
}

// running reports whether an attempt still accepts answers (so its presence counts).
func running(s modelAttempt.Status) bool {
	return s == modelAttempt.StatusInProgress || s == modelAttempt.StatusLocked
}

// The methods below are nil-safe: services built in tests without a constructor have no metrics.

func (m *attemptMetrics) start(ctx context.Context, err error, resumed bool) {
	if m != nil {
		m.started.Add(ctx, 1, attrs(attribute.String("result", startResult(err, resumed))))
	}
}

func (m *attemptMetrics) finish(ctx context.Context, id string, reason modelAttempt.SubmitReason) {
	if m != nil {
		m.finished.Add(ctx, 1, attrs(attribute.String("reason", string(reason))))
		m.presence.forget(id)
	}
}

func (m *attemptMetrics) sectionTimedOut(ctx context.Context) {
	if m != nil {
		m.timedOut.Add(ctx, 1)
	}
}

func (m *attemptMetrics) serverViolation(ctx context.Context, typ string) {
	if m != nil && slices.Contains(modelAttempt.ViolationEventTypes, typ) {
		m.violations.Add(ctx, 1, attrs(attribute.String("type", typ)))
	}
}

func (m *attemptMetrics) proctorAction(ctx context.Context, action string) {
	if m != nil {
		m.proctor.Add(ctx, 1, attrs(attribute.String("action", action)))
	}
}

// seen records that an attempt talked to this node, or forgets it once it is over.
func (m *attemptMetrics) seen(id string, status modelAttempt.Status, at time.Time) {
	if m == nil || id == "" {
		return
	}
	if running(status) {
		m.presence.touch(id, at)
	} else {
		m.presence.forget(id)
	}
}

func (m *attemptMetrics) sync(ctx context.Context, batch int, t *syncTally) {
	if m != nil {
		m.recordSync(ctx, batch, t)
	}
}
