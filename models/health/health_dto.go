package health

// Status is the state of the service or one of its dependencies.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

// CheckDTO is the result of checking one dependency (database, cache, ...).
// Error details are logged, never returned to the client.
type CheckDTO struct {
	Status    Status `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

// HealthDTO is the response payload for /healthz and /readyz.
type HealthDTO struct {
	Status        Status              `json:"status"`
	Version       string              `json:"version"`
	UptimeSeconds int64               `json:"uptime_seconds"`
	Checks        map[string]CheckDTO `json:"checks,omitempty"`
}
