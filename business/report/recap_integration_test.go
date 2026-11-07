//go:build integration

// DATABASE_URL=postgres://doexam:secret@localhost:5433/doexam?sslmode=disable \
//
//	go test -tags integration ./business/report -run Integration -v
package report

import (
	"context"
	"os"
	"testing"

	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
	dbpg "doexam/pkg/db-pg"
)

// TestIntegrationRecapCountsMatchRows checks, on every exam with participants, that the
// SQL status counts equal buildRecap's over all rows, and that paging in SQL returns each
// participant exactly once, whole (never split across pages), in name order.
func TestIntegrationRecapCountsMatchRows(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := dbpg.OpenDB(dbpg.Config{DSN: dsn, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	defer dbpg.Close(db)
	repo := NewReportRepo(db)
	ctx := context.Background()

	var exams []modelExam.ExamEntity
	err = db.Raw(`SELECT e.* FROM exams e WHERE EXISTS (SELECT 1 FROM exam_sessions es
		JOIN exam_session_participants sp ON sp.session_id = es.id WHERE es.exam_id = e.id)`).Scan(&exams).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(exams) == 0 {
		t.Skip("no exam with participants in the database")
	}
	for _, e := range exams {
		t.Run(e.Title, func(t *testing.T) {
			all, err := repo.RecapRows(ctx, e.ID, modelReport.RecapFilter{})
			if err != nil {
				t.Fatal(err)
			}
			want := buildRecap(e, all)
			counts, err := repo.RecapCounts(ctx, e.ID, modelReport.RecapFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(counts) != len(want.Counts) {
				t.Fatalf("SQL counts %v, Go counts %v", counts, want.Counts)
			}
			for k, v := range want.Counts {
				if counts[k] != v {
					t.Fatalf("SQL counts %v, Go counts %v", counts, want.Counts)
				}
			}

			var paged []modelReport.RecapRow
			for page := 1; ; page++ {
				rows, err := repo.RecapRows(ctx, e.ID, modelReport.RecapFilter{Page: page, PerPage: 3})
				if err != nil {
					t.Fatal(err)
				}
				got := buildRecap(e, rows).Rows
				if len(got) == 0 {
					break
				}
				if len(got) > 3 {
					t.Fatalf("page %d has %d participants", page, len(got))
				}
				paged = append(paged, got...)
			}
			if len(paged) != len(want.Rows) {
				t.Fatalf("paged %d participants, want %d", len(paged), len(want.Rows))
			}
			for i := range paged {
				if !sameRow(paged[i], want.Rows[i]) {
					t.Fatalf("row %d: paged %+v, want %+v", i, paged[i], want.Rows[i])
				}
			}
		})
	}
}

// sameRow compares rows by value (pointer fields differ between the two builds).
func sameRow(a, b modelReport.RecapRow) bool {
	eqF := func(x, y *float64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqB := func(x, y *bool) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqT := (a.SubmittedAt == nil) == (b.SubmittedAt == nil) && (a.SubmittedAt == nil || a.SubmittedAt.Equal(*b.SubmittedAt))
	return a.UserID == b.UserID && a.Status == b.Status && a.Attempts == b.Attempts && a.SessionName == b.SessionName &&
		eqF(a.Score, b.Score) && eqB(a.Passed, b.Passed) && eqT
}
