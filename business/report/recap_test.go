package report

import (
	"context"
	"log/slog"
	"testing"

	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
)

const recapExamID = "0192a000-0000-7000-8000-0000000000e1"

// recapStore fakes what Recap reads; other ReportStorer methods panic if called.
type recapStore struct {
	ReportStorer
	gotRows   modelReport.RecapFilter
	countsHit bool
}

func (f *recapStore) FindExam(context.Context, string) (modelExam.ExamEntity, error) {
	return modelExam.ExamEntity{ID: recapExamID, AttemptScoring: modelExam.ScoringHighest}, nil
}

func (f *recapStore) RecapRows(_ context.Context, _ string, filter modelReport.RecapFilter) ([]RecapAttempt, error) {
	f.gotRows = filter
	return []RecapAttempt{{UserID: "u1", FullName: "Ani"}}, nil // one page: one participant
}

func (f *recapStore) RecapCounts(context.Context, string, modelReport.RecapFilter) (map[string]int, error) {
	f.countsHit = true
	return map[string]int{modelReport.RecapNotStarted: 30, modelReport.RecapGraded: 12}, nil
}

type passTx struct{}

func (passTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func TestRecapPaging(t *testing.T) {
	p := modelAuth.Principal{Permissions: []string{modelAuth.PermTenantManage}}
	tests := []struct {
		name       string
		filter     modelReport.RecapFilter
		wantCounts map[string]int
		wantTotal  int
		wantSQL    bool // counts come from RecapCounts
	}{
		{"page: counts and total cover every participant", modelReport.RecapFilter{Page: 2, PerPage: 20},
			map[string]int{modelReport.RecapNotStarted: 30, modelReport.RecapGraded: 12}, 42, true},
		{"export: every row, counts folded from the rows", modelReport.RecapFilter{},
			map[string]int{modelReport.RecapNotStarted: 1}, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recapStore{}
			svc := NewReportService(store, passTx{}, slog.Default())
			out, total, err := svc.Recap(context.Background(), p, recapExamID, tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			if store.gotRows != tt.filter {
				t.Fatalf("RecapRows filter = %+v, want %+v", store.gotRows, tt.filter)
			}
			if store.countsHit != tt.wantSQL || total != tt.wantTotal || len(out.Rows) != 1 {
				t.Fatalf("countsHit=%v total=%d rows=%d", store.countsHit, total, len(out.Rows))
			}
			for k, v := range tt.wantCounts {
				if out.Counts[k] != v {
					t.Fatalf("counts = %v, want %v", out.Counts, tt.wantCounts)
				}
			}
		})
	}
}
