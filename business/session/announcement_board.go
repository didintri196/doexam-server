package session

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// boardTTL bounds how long a cached cursor is trusted before it is read again from the
// database (covers other instances writing announcements, until LISTEN/NOTIFY exists).
const boardTTL = 5 * time.Minute

// BoardStorer is what AnnouncementBoard reads and writes. Both methods run inside the
// caller's tenant transaction.
type BoardStorer interface {
	// LastAnnouncementChange is the latest send or retract time in the session (zero: none).
	LastAnnouncementChange(ctx context.Context, sessionID string) (time.Time, error)
	// MarkAnnouncementRead sets read_at once; it reports whether a row changed.
	MarkAnnouncementRead(ctx context.Context, announcementID, userID string, at time.Time) (bool, error)
}

// AnnouncementBoard tells exam clients cheaply whether a session's announcements changed.
// Every attempt heartbeat asks for the session's cursor; the answer comes from memory,
// so heartbeats add no query. Sending or retracting moves the cursor forward (Touch).
// It is shared by the session service (writer) and the attempt service (reader), which
// is why it is not part of SessionService (the attempt service is built first).
type AnnouncementBoard struct {
	storer BoardStorer
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]boardEntry
}

type boardEntry struct {
	changed time.Time // zero: no announcements
	checked time.Time // when it was last read from the database
}

// NewAnnouncementBoard returns a concrete AnnouncementBoard.
func NewAnnouncementBoard(storer BoardStorer) *AnnouncementBoard {
	return &AnnouncementBoard{storer: storer, now: time.Now, entries: map[string]boardEntry{}}
}

// Cursor returns an opaque value that changes whenever an announcement of the session is
// sent or retracted ("" = none yet). It runs inside the caller's tenant transaction.
func (b *AnnouncementBoard) Cursor(ctx context.Context, sessionID string) (string, error) {
	now := b.now()
	b.mu.Lock()
	e, ok := b.entries[sessionID]
	b.mu.Unlock()
	if !ok || now.Sub(e.checked) > boardTTL {
		changed, err := b.storer.LastAnnouncementChange(ctx, sessionID)
		if err != nil {
			return "", err
		}
		e = b.put(sessionID, changed, now)
	}
	return cursorOf(e.changed), nil
}

// Touch records a send or retract at t (after the transaction committed).
func (b *AnnouncementBoard) Touch(sessionID string, t time.Time) {
	b.put(sessionID, t, b.now())
}

// MarkRead records that userID confirmed an announcement. Unknown ids, announcements for
// someone else and repeated confirmations change nothing. It runs inside the caller's
// tenant transaction.
func (b *AnnouncementBoard) MarkRead(ctx context.Context, announcementID, userID string, at time.Time) error {
	if uuid.Validate(announcementID) != nil {
		return nil
	}
	_, err := b.storer.MarkAnnouncementRead(ctx, announcementID, userID, at)
	return err
}

// put keeps the newest change time: a slow database read finishing after a Touch must
// not move the cursor back.
func (b *AnnouncementBoard) put(sessionID string, changed, now time.Time) boardEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.entries[sessionID]; ok && old.changed.After(changed) {
		changed = old.changed
	}
	e := boardEntry{changed: changed, checked: now}
	// the id may point into a reused request buffer (Fiber params): keep a copy as the key
	b.entries[strings.Clone(sessionID)] = e
	if len(b.entries) > 10_000 {
		for id, old := range b.entries {
			if now.Sub(old.checked) > boardTTL {
				delete(b.entries, id)
			}
		}
	}
	return e
}

func cursorOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixMicro(), 36)
}
