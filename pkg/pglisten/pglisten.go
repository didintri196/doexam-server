// Package pglisten wakes local waiters when a PostgreSQL NOTIFY arrives on a channel, so a
// long-poll held by any server replica returns as soon as a row it waits for is committed on
// any replica (DOCS/PLAN/04-ARSITEKTUR.md: pub/sub over LISTEN/NOTIFY, no Redis needed).
//
// Notifications only carry a key; waiters re-read the database when woken. A lost
// notification (reconnect) therefore costs at most one poll timeout, and every waiter is woken
// after a reconnect so none waits on a notification that was missed.
package pglisten

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Listener holds one dedicated connection that LISTENs on a channel.
type Listener struct {
	dsn     string
	channel string
	logger  *slog.Logger

	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// New creates a listener; call Run to connect.
func New(dsn, channel string, logger *slog.Logger) *Listener {
	return &Listener{dsn: dsn, channel: channel, logger: logger, subs: map[string]map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives (without blocking the listener) whenever key is
// notified, and a function to unsubscribe. Subscribe before reading the database, so a
// notification committed between the read and the wait is not missed.
func (l *Listener) Subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	if l.subs[key] == nil {
		l.subs[key] = map[chan struct{}]struct{}{}
	}
	l.subs[key][ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs[key], ch)
		if len(l.subs[key]) == 0 {
			delete(l.subs, key)
		}
		l.mu.Unlock()
	}
}

// Wake signals the subscribers of key on this process.
func (l *Listener) Wake(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.subs[key] {
		signal(ch)
	}
}

func (l *Listener) wakeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, set := range l.subs {
		for ch := range set {
			signal(ch)
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default: // already pending
	}
}

// Run keeps a LISTEN connection open until ctx ends, reconnecting with backoff.
func (l *Listener) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := l.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		l.logger.WarnContext(ctx, "pg listener disconnected, reconnecting",
			slog.String("channel", l.channel), slog.String("error", errString(err)), slog.Duration("backoff", backoff))
		l.wakeAll() // notifications may have been lost while disconnected
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (l *Listener) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, l.dsn)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx) // best effort; the connection is discarded either way
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{l.channel}.Sanitize()); err != nil {
		return err
	}
	l.logger.InfoContext(ctx, "pg listener ready", slog.String("channel", l.channel))
	l.wakeAll() // anything committed before LISTEN took effect is re-read
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		l.Wake(n.Payload)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
