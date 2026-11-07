package pglisten

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSubscribeWake(t *testing.T) {
	l := New("", "ch", quiet())
	a, cancelA := l.Subscribe("k1")
	b, cancelB := l.Subscribe("k2")
	defer cancelB()

	l.Wake("k1")
	l.Wake("k1") // coalesced, never blocks
	select {
	case <-a:
	default:
		t.Fatal("k1 subscriber not woken")
	}
	select {
	case <-b:
		t.Fatal("k2 subscriber woken by k1")
	default:
	}

	cancelA()
	l.Wake("k1") // no subscribers left: no panic, no leak
	if len(l.subs) != 1 {
		t.Fatalf("subs %v", l.subs)
	}
	l.wakeAll()
	select {
	case <-b:
	default:
		t.Fatal("wakeAll missed k2")
	}
}

// TestListenNotify needs a database: DATABASE_URL=... go test ./pkg/pglisten -run Notify
func TestListenNotify(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l := New(dsn, "doexam_pglisten_test", quiet())
	ch, unsub := l.Subscribe("key-1")
	defer unsub()
	go l.Run(ctx)
	<-ch // woken once when LISTEN is ready

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT pg_notify('doexam_pglisten_test', 'key-1')"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal("notification not delivered")
	}
}
