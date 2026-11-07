//go:build integration

// Run against a development database (and optionally Redis):
//
//	DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//	REDIS_URL=redis://localhost:6379/0 go test -tags integration ./business/attempt -run Integration -v
package attempt

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	modelAttempt "doexam/models/attempt"
	"doexam/pkg/bytecache"
	dbpg "doexam/pkg/db-pg"
)

// legacyItems is the join query items() replaced, kept here as the reference.
func legacyItems(ctx context.Context, r *AttemptRepo, attemptID string) ([]ItemRow, error) {
	out := []ItemRow{}
	err := dbpg.Conn(ctx, r.db).Table("attempt_items ai").
		Select(`ai.*, iv.type, iv.content, iv.answer_key, iv.scoring, iv.explanation,
			st.id AS stimulus_id, st.title AS stimulus_title, sv.content AS stimulus_content`).
		Joins("JOIN item_versions iv ON iv.id = ai.item_version_id").
		Joins("LEFT JOIN stimulus_versions sv ON sv.id = iv.stimulus_version_id").
		Joins("LEFT JOIN stimuli st ON st.id = sv.stimulus_id").
		Where("ai.attempt_id = ?", attemptID).Order("ai.position").Scan(&out).Error
	return out, err
}

func TestIntegrationItemsMatchJoinQuery(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "development"})
	if err != nil {
		t.Fatal(err)
	}
	defer dbpg.Close(db)

	// development connects as the table owner (no RLS), so any tenant's attempts are visible here
	var attempts []modelAttempt.AttemptEntity
	if err := db.Order("started_at DESC").Limit(20).Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Skip("no attempts in the database")
	}

	caches := map[string]ContentCache{"memory": bytecache.NewMemory(64 << 20)}
	if url := os.Getenv("REDIS_URL"); url != "" {
		client, err := bytecache.OpenRedis(context.Background(), url)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		caches["redis"] = bytecache.NewRedis(client, "doexam-test:", 0)
	}

	repo := NewAttemptRepo(db)
	tx := dbpg.NewTxManager(db)
	for name, cache := range caches {
		svc := NewAttemptService(repo, tx, nil, nil, cache, slog.New(slog.NewTextHandler(io.Discard, nil)))
		for _, a := range attempts {
			for pass := range 2 { // first pass fills the cache, second reads it
				err := tx.WithTenant(context.Background(), a.TenantID, func(ctx context.Context) error {
					want, err := legacyItems(ctx, repo, a.ID)
					if err != nil {
						return err
					}
					got, err := svc.items(ctx, a)
					if err != nil {
						return err
					}
					if len(got) != len(want) {
						t.Fatalf("%s pass %d attempt %s: %d items, want %d", name, pass, a.ID, len(got), len(want))
					}
					for i := range want {
						g, w := got[i], want[i]
						if g.ID != w.ID || g.Position != w.Position || g.Type != w.Type ||
							string(g.Content) != string(w.Content) || string(g.AnswerKey) != string(w.AnswerKey) ||
							string(g.Scoring) != string(w.Scoring) || string(g.Explanation) != string(w.Explanation) ||
							string(g.StimulusContent) != string(w.StimulusContent) ||
							!samePtr(g.StimulusID, w.StimulusID) || !samePtr(g.StimulusTitle, w.StimulusTitle) {
							t.Fatalf("%s pass %d attempt %s item %d differs from the join query", name, pass, a.ID, i)
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		}

		// warm-up lists every version of each exam: a superset of what its attempts use
		for _, a := range attempts {
			err := tx.WithTenant(context.Background(), a.TenantID, func(ctx context.Context) error {
				ids, err := repo.ExamVersionIDs(ctx, a.ExamID)
				if err != nil {
					return err
				}
				rows, err := repo.ListAttemptItems(ctx, a.ID)
				if err != nil {
					return err
				}
				for _, r := range rows {
					if !slices.Contains(ids, r.ItemVersionID) {
						t.Fatalf("exam %s: version %s used by attempt %s is not in ExamVersionIDs", a.ExamID, r.ItemVersionID, a.ID)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s: %d attempts match the join query", name, len(attempts))
	}
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
