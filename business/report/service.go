package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAuth "doexam/models/auth"
	modelExam "doexam/models/exam"
	modelReport "doexam/models/report"
	"doexam/pkg/errr"
)

var (
	errExamNotFound    = errr.WithMessage(errr.ErrNotFound, "exam not found")
	errAttemptNotFound = errr.WithMessage(errr.ErrNotFound, "attempt not found")
	violationTypes     = []string{"FOCUS_LOST", "FULLSCREEN_EXIT", "DEVICE_CHANGED"}
)

// ReportService builds score recaps, individual reports and classical item analysis.
//
// Access: tenant.manage, or report.read together with being the exam's creator or holding
// grade.write. Other exams are reported as not found.
type ReportService struct {
	storer ReportStorer
	tx     TxManager
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewReportService returns a concrete ReportService.
func NewReportService(storer ReportStorer, tx TxManager, logger *slog.Logger) *ReportService {
	return &ReportService{storer: storer, tx: tx, now: time.Now, tracer: otel.Tracer("doexam.service.report"), logger: logger}
}

// Recap returns every participant of the exam (optionally one session or group) with the
// score that counts under the exam's attempt_scoring.
func (s *ReportService) Recap(ctx context.Context, p modelAuth.Principal, examID string, f modelReport.RecapFilter) (modelReport.RecapDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ReportService.Recap", trace.WithAttributes(attribute.String("exam.id", examID)))
	defer span.End()

	var out modelReport.RecapDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.reportExam(ctx, p, examID, f.SessionID)
		if err != nil {
			return err
		}
		if f.GroupID != "" && uuid.Validate(f.GroupID) != nil {
			return &errr.ValidationError{Fields: map[string]string{"group_id": "must be a UUID"}}
		}
		rows, err := s.storer.RecapRows(ctx, examID, f)
		if err != nil {
			return err
		}
		out = buildRecap(e, rows)
		return nil
	})
	if err != nil {
		return modelReport.RecapDTO{}, fail(span, err)
	}
	return out, nil
}

// buildRecap folds participant × attempt rows into one row per participant.
func buildRecap(e modelExam.ExamEntity, rows []RecapAttempt) modelReport.RecapDTO {
	out := modelReport.RecapDTO{ExamID: e.ID, ExamTitle: e.Title, AttemptScoring: string(e.AttemptScoring), PassingScore: e.PassingScore,
		Counts: map[string]int{}, Rows: []modelReport.RecapRow{}}
	type acc struct {
		row     modelReport.RecapRow
		scores  []float64
		pending bool
		running bool
	}
	order := []string{}
	byUser := map[string]*acc{}
	for _, r := range rows {
		a := byUser[r.UserID]
		if a == nil {
			a = &acc{row: modelReport.RecapRow{UserID: r.UserID, Username: r.Username, FullName: r.FullName, ExternalID: r.ExternalID}}
			byUser[r.UserID] = a
			order = append(order, r.UserID)
		}
		a.row.SessionName = r.SessionName // rows are ordered by session start: the latest wins
		if r.AttemptID == nil || r.Status == nil {
			continue
		}
		a.row.Attempts++
		switch *r.Status {
		case "in_progress", "locked":
			a.running = true
		case "submitted":
			a.pending = true
			fallthrough
		case "graded":
			if r.FinalScore != nil {
				a.scores = append(a.scores, *r.FinalScore)
			}
			if r.SubmittedAt != nil {
				a.row.SubmittedAt = r.SubmittedAt
			}
		}
	}
	for _, uid := range order {
		a := byUser[uid]
		switch {
		case a.row.Attempts == 0:
			a.row.Status = modelReport.RecapNotStarted
		case a.running && len(a.scores) == 0:
			a.row.Status = modelReport.RecapInProgress
		case a.pending:
			a.row.Status = modelReport.RecapPendingGrading
		default:
			a.row.Status = modelReport.RecapGraded
		}
		if len(a.scores) > 0 {
			sc := countedScore(a.scores, e.AttemptScoring)
			a.row.Score = &sc
			if e.PassingScore != nil && a.row.Status == modelReport.RecapGraded {
				passed := sc >= *e.PassingScore
				a.row.Passed = &passed
			}
		}
		out.Counts[a.row.Status]++
		out.Rows = append(out.Rows, a.row)
	}
	return out
}

// countedScore applies attempt_scoring to a participant's scores (in attempt order).
func countedScore(scores []float64, mode modelExam.AttemptScoring) float64 {
	switch mode {
	case modelExam.ScoringLatest:
		return scores[len(scores)-1]
	case modelExam.ScoringAverage:
		sum := 0.0
		for _, s := range scores {
			sum += s
		}
		return math.Round(sum/float64(len(scores))*100) / 100
	default:
		best := scores[0]
		for _, s := range scores[1:] {
			best = math.Max(best, s)
		}
		return best
	}
}

// Analysis returns the stored item analysis of the exam (or one session), computing it
// first when it does not exist yet or when recompute is true.
func (s *ReportService) Analysis(ctx context.Context, p modelAuth.Principal, examID, sessionID string, recompute bool) (modelReport.AnalysisDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ReportService.Analysis", trace.WithAttributes(attribute.String("exam.id", examID), attribute.Bool("recompute", recompute)))
	defer span.End()

	var out modelReport.AnalysisDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		e, err := s.reportExam(ctx, p, examID, sessionID)
		if err != nil {
			return err
		}
		st, found, err := s.storer.LoadStats(ctx, examID, sessionID)
		if err != nil {
			return err
		}
		var warnings []string
		if !found || recompute {
			attempts, items, err := s.storer.AnalysisData(ctx, examID, sessionID)
			if err != nil {
				return err
			}
			exam, itemStats, w := analyze(attempts, items, e.PassingScore)
			warnings = w
			// microseconds: what Postgres stores, so later reads return the same timestamp
			st = StoredStats{ComputedAt: s.now().UTC().Truncate(time.Microsecond), Exam: exam, Items: itemStats}
			if err := s.storer.SaveStats(ctx, p.TenantID, examID, sessionID, st); err != nil {
				return err
			}
		} else if st.Exam.NAttempts < smallSample {
			warnings = append(warnings, fmt.Sprintf("small_sample: %d graded attempts; statistics are unstable below %d", st.Exam.NAttempts, smallSample))
		}
		for i := range st.Items {
			interpret(&st.Items[i])
		}
		if warnings == nil {
			warnings = []string{}
		}
		if st.Items == nil {
			st.Items = []modelReport.ItemStat{}
		}
		out = modelReport.AnalysisDTO{ExamID: examID, ComputedAt: st.ComputedAt, Warnings: warnings, Exam: st.Exam, Items: st.Items}
		if sessionID != "" {
			out.SessionID = &sessionID
		}
		return nil
	})
	if err != nil {
		return modelReport.AnalysisDTO{}, fail(span, err)
	}
	return out, nil
}

// AttemptReport is the staff view of one attempt: scores per section and competency, and
// every item with response, key, time spent and visits.
func (s *ReportService) AttemptReport(ctx context.Context, p modelAuth.Principal, attemptID string) (modelReport.AttemptReportDTO, error) {
	ctx, span := s.tracer.Start(ctx, "ReportService.AttemptReport", trace.WithAttributes(attribute.String("attempt.id", attemptID)))
	defer span.End()

	var out modelReport.AttemptReportDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if uuid.Validate(attemptID) != nil {
			return errAttemptNotFound
		}
		a, err := s.storer.FindReportAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		if _, err := s.reportExam(ctx, p, a.ExamID, ""); err != nil {
			if errr.IsNotFound(err) || errors.Is(err, errr.ErrForbidden) {
				return errAttemptNotFound // do not reveal attempts of exams the caller cannot report on
			}
			return err
		}
		sections, err := s.storer.ReportSections(ctx, attemptID)
		if err != nil {
			return err
		}
		items, err := s.storer.ReportItems(ctx, attemptID)
		if err != nil {
			return err
		}
		violations, err := s.storer.EventCounts(ctx, attemptID, violationTypes)
		if err != nil {
			return err
		}
		out = buildAttemptReport(a, sections, items, violations)
		return nil
	})
	if err != nil {
		return modelReport.AttemptReportDTO{}, fail(span, err)
	}
	return out, nil
}

func buildAttemptReport(a ReportAttempt, sections []ReportSection, items []ReportItem, violations map[string]int) modelReport.AttemptReportDTO {
	var snap struct {
		ExamTitle string `json:"exam_title"`
		Sections  []struct {
			SectionID string `json:"section_id"`
			Title     string `json:"title"`
		} `json:"sections"`
	}
	_ = json.Unmarshal(a.ConfigSnapshot, &snap)
	out := modelReport.AttemptReportDTO{
		AttemptID: a.ID, ExamID: a.ExamID, ExamTitle: snap.ExamTitle, SessionName: a.SessionName, UserID: a.UserID,
		Username: a.Username, FullName: a.FullName, ExternalID: a.ExternalID, AttemptNo: a.AttemptNo, Status: a.Status,
		SubmitReason: a.SubmitReason, StartedAt: a.StartedAt, SubmittedAt: a.SubmittedAt, RawScore: a.RawScore,
		MaxScore: a.MaxScore, FinalScore: a.FinalScore, Passed: a.Passed, PendingManual: a.PendingManual, Violations: violations,
		Sections: []modelReport.SectionReport{}, Competencies: []modelReport.CompetencyReport{}, Items: []modelReport.ItemReport{},
	}
	if a.SubmittedAt != nil {
		d := int64(a.SubmittedAt.Sub(a.StartedAt).Seconds())
		out.DurationSec = &d
	}
	for _, sec := range sections {
		title := ""
		for _, ss := range snap.Sections {
			if ss.SectionID == sec.SectionID {
				title = ss.Title
			}
		}
		out.Sections = append(out.Sections, modelReport.SectionReport{Title: title, RawScore: sec.RawScore, MaxScore: sec.MaxScore, Passed: sec.Passed})
	}
	comp := map[string]*modelReport.CompetencyReport{}
	var compOrder []string
	for _, it := range items {
		resp, key := json.RawMessage("null"), json.RawMessage("null")
		if len(it.Response) > 0 {
			resp = it.Response
		}
		if len(it.AnswerKey) > 0 {
			key = it.AnswerKey
		}
		out.Items = append(out.Items, modelReport.ItemReport{
			Position: it.Position, ItemID: it.ItemID, Code: it.Code, Type: it.Type, StemPreview: preview(it.SearchText),
			Response: resp, AnswerKey: key, Score: it.Score, MaxPoints: it.MaxPoints, IsCorrect: it.IsCorrect,
			ScoreSource: it.ScoreSource, TimeSpentMs: it.TimeSpentMs, Visits: it.VisitCount, Flagged: it.Flagged,
		})
		if it.CompetencyID == nil || it.Score == nil {
			continue
		}
		c := comp[*it.CompetencyID]
		if c == nil {
			c = &modelReport.CompetencyReport{CompetencyID: *it.CompetencyID, Code: deref(it.CompetencyCode), Name: deref(it.CompetencyName)}
			comp[*it.CompetencyID] = c
			compOrder = append(compOrder, *it.CompetencyID)
		}
		c.Score += math.Max(0, *it.Score)
		c.MaxScore += it.MaxPoints
		c.Items++
	}
	for _, id := range compOrder {
		c := comp[id]
		if c.MaxScore > 0 {
			c.Percent = math.Round(c.Score/c.MaxScore*10000) / 100
		}
		out.Competencies = append(out.Competencies, *c)
	}
	return out
}

// reportExam loads an exam the caller may see reports of (and checks the optional session).
func (s *ReportService) reportExam(ctx context.Context, p modelAuth.Principal, examID, sessionID string) (modelExam.ExamEntity, error) {
	if uuid.Validate(examID) != nil {
		return modelExam.ExamEntity{}, errExamNotFound
	}
	e, err := s.storer.FindExam(ctx, examID)
	if err != nil {
		return e, err
	}
	creator := e.CreatedBy != nil && *e.CreatedBy == p.UserID
	allowed := p.Has(modelAuth.PermTenantManage) ||
		(p.Has(modelAuth.PermReportRead) && (creator || p.Has(modelAuth.PermGradeWrite)))
	if !allowed {
		return modelExam.ExamEntity{}, errr.WithMessage(errr.ErrForbidden, "needs report.read and being the exam's creator or a grader")
	}
	if sessionID != "" {
		ok := uuid.Validate(sessionID) == nil
		if ok {
			if ok, err = s.storer.SessionBelongs(ctx, sessionID, examID); err != nil {
				return e, err
			}
		}
		if !ok {
			return e, &errr.ValidationError{Fields: map[string]string{"session_id": "session not found for this exam"}}
		}
	}
	return e, nil
}

// RecapTable turns a recap into spreadsheet rows (header + values) for export.
func RecapTable(r modelReport.RecapDTO) ([]string, [][]string) {
	headers := []string{"no", "username", "nama", "nomor_induk", "sesi", "status", "percobaan", "nilai", "lulus", "dikumpulkan"}
	rows := make([][]string, 0, len(r.Rows))
	for i, row := range r.Rows {
		score, passed, submitted := "", "", ""
		if row.Score != nil {
			score = strings.Replace(fmt.Sprintf("%.2f", *row.Score), ".", ",", 1) // Indonesian decimal comma
		}
		if row.Passed != nil {
			passed = map[bool]string{true: "ya", false: "tidak"}[*row.Passed]
		}
		if row.SubmittedAt != nil {
			submitted = row.SubmittedAt.Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{fmt.Sprint(i + 1), row.Username, row.FullName, deref(row.ExternalID), row.SessionName,
			row.Status, fmt.Sprint(row.Attempts), score, passed, submitted})
	}
	return headers, rows
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}
