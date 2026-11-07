//go:build integration

// DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//
//	go test -tags integration ./pkg/dbmigrate -v
//
// The test creates its own throw-away database next to DATABASE_URL's and drops it.
package dbmigrate_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"doexam/migrations"
	"doexam/pkg/dbmigrate"
)

func TestIntegrationUpDownUp(t *testing.T) {
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// cleanups run last-in first-out: drop the database, then close the admin connection
	t.Cleanup(func() { admin.Close(context.Background()) })
	name := fmt.Sprintf("doexam_migtest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	m, err := dbmigrate.New(migrations.FS, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() }) // closed before the drop above

	st, err := m.Status()
	if err != nil || !st.None {
		t.Fatalf("fresh database: status %+v, err %v", st, err)
	}
	changed, err := m.Up()
	if err != nil || !changed {
		t.Fatalf("up: changed=%v err=%v", changed, err)
	}
	top, err := m.Status()
	if err != nil || top.None || top.Dirty {
		t.Fatalf("after up: %+v %v", top, err)
	}
	if changed, err := m.Up(); err != nil || changed {
		t.Fatalf("second up must be a no-op: changed=%v err=%v", changed, err)
	}
	// every down migration reverses its up: all the way down, then up again
	n, err := m.Down(int(top.Version) + 5)
	if err != nil || n != int(top.Version) {
		t.Fatalf("down all: rolled back %d, want %d (err %v)", n, top.Version, err)
	}
	if st, _ := m.Status(); !st.None {
		t.Fatalf("after down all: %+v", st)
	}
	if _, err := m.Up(); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if st, _ := m.Status(); st.Version != top.Version {
		t.Fatalf("version %d after re-up, want %d", st.Version, top.Version)
	}
}
