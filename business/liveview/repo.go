package liveview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelLive "doexam/models/liveview"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// NotifyChannel is the PostgreSQL channel live-view long-polls listen on.
const NotifyChannel = "doexam_liveview"

var _ LiveViewStorer = (*LiveViewRepo)(nil)

// LiveViewRepo is the GORM adapter of LiveViewStorer.
type LiveViewRepo struct{ db *gorm.DB }

// NewLiveViewRepo creates the repository.
func NewLiveViewRepo(db *gorm.DB) *LiveViewRepo { return &LiveViewRepo{db: db} }

func (r *LiveViewRepo) FindSession(ctx context.Context, id string) (SessionRow, error) {
	var rows []SessionRow
	err := dbpg.Conn(ctx, r.db).Table("exam_sessions").Select("id, status, require_camera, require_screen").
		Where("id = ?", id).Limit(1).Scan(&rows).Error
	if err != nil {
		return SessionRow{}, fmt.Errorf("find session: %w", err)
	}
	if len(rows) == 0 {
		return SessionRow{}, errr.ErrNotFound
	}
	return rows[0], nil
}

func (r *LiveViewRepo) IsProctor(ctx context.Context, sessionID, userID string) (bool, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Table("exam_session_proctors").Where("session_id = ? AND user_id = ?", sessionID, userID).Count(&n).Error
	return n > 0, wrap(err, "is proctor")
}

func (r *LiveViewRepo) FindAttempt(ctx context.Context, id string) (AttemptRow, error) {
	var rows []AttemptRow
	err := dbpg.Conn(ctx, r.db).Table("attempts").Select("id, session_id, user_id, status").
		Where("id = ?", id).Limit(1).Scan(&rows).Error
	if err != nil {
		return AttemptRow{}, fmt.Errorf("find attempt: %w", err)
	}
	if len(rows) == 0 {
		return AttemptRow{}, errr.ErrNotFound
	}
	return rows[0], nil
}

func (r *LiveViewRepo) ActiveViews(ctx context.Context, attemptID string, now time.Time) ([]modelLive.ViewEntity, error) {
	var out []modelLive.ViewEntity
	err := dbpg.Conn(ctx, r.db).Where("attempt_id = ? AND ended_at IS NULL AND expires_at > ?", attemptID, now).
		Order("started_at").Find(&out).Error
	return out, wrap(err, "active views")
}

func (r *LiveViewRepo) CountProctorViews(ctx context.Context, proctorID string, now time.Time) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Model(&modelLive.ViewEntity{}).
		Where("proctor_id = ? AND ended_at IS NULL AND expires_at > ?", proctorID, now).Count(&n).Error
	return n, wrap(err, "count proctor views")
}

func (r *LiveViewRepo) CreateView(ctx context.Context, v *modelLive.ViewEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(v).Error, "create view")
}

func (r *LiveViewRepo) FindView(ctx context.Context, id string) (modelLive.ViewEntity, error) {
	var v modelLive.ViewEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&v).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return v, errr.ErrNotFound
		}
		return v, fmt.Errorf("find view: %w", err)
	}
	return v, nil
}

func (r *LiveViewRepo) EndView(ctx context.Context, id string, reason modelLive.EndReason, now time.Time) (bool, error) {
	res := dbpg.Conn(ctx, r.db).Model(&modelLive.ViewEntity{}).Where("id = ? AND ended_at IS NULL", id).
		Updates(map[string]any{"ended_at": now, "end_reason": reason})
	return res.RowsAffected > 0, wrap(res.Error, "end view")
}

func (r *LiveViewRepo) AddSignal(ctx context.Context, s *modelLive.SignalEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(s).Error, "add signal")
}

func (r *LiveViewRepo) Notify(ctx context.Context, key string) error {
	return wrap(dbpg.Conn(ctx, r.db).Exec("SELECT pg_notify(?, ?)", NotifyChannel, key).Error, "notify")
}

func (r *LiveViewRepo) ProctorSignals(ctx context.Context, viewID string, after int64, limit int) ([]modelLive.SignalEntity, error) {
	var out []modelLive.SignalEntity
	err := dbpg.Conn(ctx, r.db).Where("view_id = ? AND recipient = ? AND id > ?", viewID, modelLive.RecipientProctor, after).
		Order("id").Limit(limit).Find(&out).Error
	return out, wrap(err, "proctor signals")
}

func (r *LiveViewRepo) ParticipantSignals(ctx context.Context, attemptID string, after int64, now, since time.Time, limit int) ([]modelLive.SignalEntity, error) {
	var out []modelLive.SignalEntity
	err := dbpg.Conn(ctx, r.db).Raw(`
		SELECT s.* FROM live_views v
		JOIN live_view_signals s ON s.view_id = v.id AND s.recipient = ? AND s.id > ?
		WHERE v.attempt_id = ?
		  AND ((v.ended_at IS NULL AND v.expires_at > ?) OR s.created_at > ?)
		ORDER BY s.id LIMIT ?`, modelLive.RecipientParticipant, after, attemptID, now, since, limit).Scan(&out).Error
	return out, wrap(err, "participant signals")
}

func (r *LiveViewRepo) PurgeSignals(ctx context.Context, attemptID string, before time.Time) error {
	err := dbpg.Conn(ctx, r.db).Exec(`DELETE FROM live_view_signals s USING live_views v
		WHERE s.view_id = v.id AND v.attempt_id = ? AND s.created_at < ?`, attemptID, before).Error
	return wrap(err, "purge signals")
}

func (r *LiveViewRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error, "create audit log")
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
