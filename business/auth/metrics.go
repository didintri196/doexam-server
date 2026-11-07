package auth

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"doexam/pkg/errr"
	"doexam/pkg/metrics"
)

// Login results (doexam_auth_logins_total{result}). Every failure but "error" is answered
// with the same "invalid credentials" to the client; the label is for operators only.
const (
	loginOK             = "ok"
	loginInvalidInput   = "invalid_input"
	loginUnknownAccount = "unknown_account" // tenant or username unknown, inactive tenant, no password
	loginWrongPassword  = "wrong_password"
	loginInactiveUser   = "inactive_user"
	loginError          = "error"
)

// authMetrics holds the instruments of logins and token resolution.
type authMetrics struct {
	logins metric.Int64Counter
	cache  metric.Int64Counter
}

func newAuthMetrics(principals *principalCache) *authMetrics {
	m := otel.Meter("doexam.service.auth")
	am := &authMetrics{}
	var err1, err2 error
	am.logins, err1 = m.Int64Counter("doexam.auth.logins", metric.WithUnit("{login}"),
		metric.WithDescription("Login attempts by result."))
	am.cache, err2 = m.Int64Counter("doexam.auth.cache.requests", metric.WithUnit("{request}"),
		metric.WithDescription("Token resolutions answered by the principal cache (hit) or the database (miss)."))
	metrics.Report(err1, err2)
	if principals != nil {
		entries, err := m.Int64ObservableGauge("doexam.auth.cache.entries", metric.WithUnit("{entry}"),
			metric.WithDescription("Principals in the token cache of this node."))
		metrics.Report(err)
		_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveInt64(entries, int64(principals.len()))
			return nil
		}, entries)
		metrics.Report(err)
	}
	return am
}

// loginResult labels a login outcome when login did not set one.
func loginResult(err error, result string) string {
	var ve *errr.ValidationError
	switch {
	case err == nil:
		return loginOK
	case result != "":
		return result
	case errors.As(err, &ve):
		return loginInvalidInput
	}
	return loginError
}

func (m *authMetrics) login(ctx context.Context, result string) {
	if m != nil {
		m.logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}

func (m *authMetrics) cacheLookup(ctx context.Context, hit bool) {
	if m == nil {
		return
	}
	r := "miss"
	if hit {
		r = "hit"
	}
	m.cache.Add(ctx, 1, metric.WithAttributes(attribute.String("result", r)))
}
