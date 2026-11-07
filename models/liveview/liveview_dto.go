package liveview

import (
	"encoding/json"
	"time"
)

// OpenInput is the body of POST /api/v1/sessions/:id/live-views.
type OpenInput struct {
	AttemptID string `json:"attempt_id"`
}

// SignalInput is the body of POST …/live-views/:vid/signal (proctor: answer, ice) and
// POST /api/v1/attempts/:id/signal (participant: offer, ice, end; view_id required).
type SignalInput struct {
	ViewID  string          `json:"view_id"`
	Kind    Kind            `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ViewDTO answers POST /sessions/:id/live-views.
type ViewDTO struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	AttemptID string    `json:"attempt_id"`
	Camera    bool      `json:"camera"`
	Screen    bool      `json:"screen"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// ICEServers is the RTCPeerConnection iceServers list (ICE_SERVERS; empty = LAN only).
	ICEServers json.RawMessage `json:"ice_servers"`
}

// SignalDTO is one signaling message. ID is the long-poll cursor.
type SignalDTO struct {
	ID        int64           `json:"id"`
	ViewID    string          `json:"view_id"`
	Kind      Kind            `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// PollDTO answers a long-poll: the messages after the cursor and the next cursor.
// Ended (proctor polls only) tells the proctor the view is over.
type PollDTO struct {
	Signals []SignalDTO `json:"signals"`
	Cursor  int64       `json:"cursor"`
	Ended   bool        `json:"ended"`
}

// ToSignalDTO converts a stored signal.
func ToSignalDTO(e SignalEntity) SignalDTO {
	return SignalDTO{ID: e.ID, ViewID: e.ViewID, Kind: e.Kind, Payload: e.Payload, CreatedAt: e.CreatedAt.UTC()}
}
