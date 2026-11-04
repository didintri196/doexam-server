package session

import (
	"context"
	modelTenant "doexam/models/tenant"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAudit "doexam/models/audit"
	modelSession "doexam/models/session"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: SessionRepo must satisfy SessionStorer.
var _ SessionStorer = (*SessionRepo)(nil)

// SessionRepo is the GORM implementation of SessionStorer.
type SessionRepo struct {
	db *gorm.DB
}

// NewSessionRepo returns a concrete SessionRepo.
func NewSessionRepo(db *gorm.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

// totalMinutesSQL is the exam duration: the exam's own, or the sum of section
// durations when every section has one.
const totalMinutesSQL = `coalesce(e.duration_minutes, (
	SELECT CASE WHEN count(*) > 0 AND count(*) = count(s.duration_minutes) THEN sum(s.duration_minutes) END
	FROM exam_sections s WHERE s.exam_id = e.id))`

func (r *SessionRepo) ListSessions(ctx context.Context, f modelSession.ListFilter, userID string, all bool) ([]modelSession.SessionSummaryDTO, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("exam_sessions es").Joins("JOIN exams e ON e.id = es.exam_id")
	if !all {
		q = q.Where("EXISTS (SELECT 1 FROM exam_session_proctors pr WHERE pr.session_id = es.id AND pr.user_id = ?)", userID)
	}
	if f.ExamID != "" {
		q = q.Where("es.exam_id = ?", f.ExamID)
	}
	if f.Status != "" {
		q = q.Where("es.status = ?", f.Status)
	}
	if f.From != nil {
		q = q.Where("es.ends_at > ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("es.starts_at < ?", *f.To)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count sessions: %w", err)
	}
	rows := []modelSession.SessionSummaryDTO{}
	err := q.Session(&gorm.Session{}).
		Select(`es.id, es.exam_id, e.title AS exam_title, es.name, es.room, es.starts_at, es.ends_at, es.status, es.is_makeup,
			(SELECT count(*) FROM exam_session_participants sp WHERE sp.session_id = es.id) AS participant_count`).
		Order("es.starts_at, es.name, es.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&rows).Error
	return rows, total, wrap(err, "list sessions")
}

func (r *SessionRepo) FindSession(ctx context.Context, id string, forUpdate bool) (modelSession.SessionEntity, error) {
	var s modelSession.SessionEntity
	q := dbpg.Conn(ctx, r.db).Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s, errSessionNotFound
		}
		return s, fmt.Errorf("find session %s: %w", id, err)
	}
	return s, nil
}

func (r *SessionRepo) CreateSession(ctx context.Context, s *modelSession.SessionEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Create(s).Error, "create session")
}

func (r *SessionRepo) UpdateSession(ctx context.Context, s *modelSession.SessionEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(s).Select("exam_id", "name", "room", "starts_at", "ends_at", "late_entry_minutes",
		"token_required", "token_secret", "token_rotation_minutes", "status", "is_makeup").Updates(s).Error
	return wrap(err, "update session")
}

func (r *SessionRepo) DeleteSession(ctx context.Context, id string) error {
	return wrap(dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelSession.SessionEntity{}).Error, "delete session")
}

func (r *SessionRepo) ExamRef(ctx context.Context, examID string) (modelSession.ExamRefDTO, error) {
	var rows []modelSession.ExamRefDTO
	err := dbpg.Conn(ctx, r.db).Table("exams e").
		Select("e.id, e.title, e.status, "+totalMinutesSQL+" AS total_minutes").
		Where("e.id = ? AND e.deleted_at IS NULL", examID).Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return modelSession.ExamRefDTO{}, wrap(err, "exam ref")
	}
	return rows[0], nil
}

func (r *SessionRepo) CountAttempts(ctx context.Context, sessionID, userID string) (int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("attempts").Where("session_id = ?", sessionID)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	var n int64
	return n, wrap(q.Count(&n).Error, "count attempts")
}

func (r *SessionRepo) IsProctor(ctx context.Context, sessionID, userID string) (bool, error) {
	var ok bool
	err := dbpg.Conn(ctx, r.db).Raw("SELECT EXISTS (SELECT 1 FROM exam_session_proctors WHERE session_id = ? AND user_id = ?)",
		sessionID, userID).Scan(&ok).Error
	return ok, wrap(err, "is proctor")
}

func (r *SessionRepo) ListProctors(ctx context.Context, sessionID string) ([]modelSession.PersonDTO, error) {
	out := []modelSession.PersonDTO{}
	err := dbpg.Conn(ctx, r.db).Table("exam_session_proctors pr").
		Select("pr.user_id, u.username, u.full_name").
		Joins("JOIN users u ON u.id = pr.user_id AND u.deleted_at IS NULL").
		Where("pr.session_id = ?", sessionID).Order("u.full_name").Scan(&out).Error
	return out, wrap(err, "list proctors")
}

func (r *SessionRepo) ReplaceProctors(ctx context.Context, tenantID, sessionID string, userIDs []string) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Where("session_id = ?", sessionID).Delete(&modelSession.ProctorEntity{}).Error; err != nil {
		return fmt.Errorf("clear proctors: %w", err)
	}
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]modelSession.ProctorEntity, len(userIDs))
	for i, id := range userIDs {
		rows[i] = modelSession.ProctorEntity{SessionID: sessionID, UserID: id, TenantID: tenantID}
	}
	return wrap(conn.Create(&rows).Error, "set proctors")
}

func (r *SessionRepo) UsersWithPermission(ctx context.Context, ids []string, perm string) ([]string, error) {
	out := []string{}
	if len(ids) == 0 {
		return out, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("users u").Distinct("u.id").
		Joins("JOIN user_roles ur ON ur.user_id = u.id").
		Joins("JOIN roles ro ON ro.id = ur.role_id").
		Where("u.id IN ? AND u.deleted_at IS NULL AND u.status = 'active' AND ? = ANY(ro.permissions)", ids, perm).
		Pluck("u.id", &out).Error
	return out, wrap(err, "users with permission")
}

func (r *SessionRepo) ListSessionGroups(ctx context.Context, sessionID string) ([]modelSession.GroupRefDTO, error) {
	out := []modelSession.GroupRefDTO{}
	err := dbpg.Conn(ctx, r.db).Table("exam_session_groups sg").Select("g.id, g.name, g.period").
		Joins("JOIN groups g ON g.id = sg.group_id").Where("sg.session_id = ?", sessionID).
		Order("g.name").Scan(&out).Error
	return out, wrap(err, "list session groups")
}

func (r *SessionRepo) AttendanceCounts(ctx context.Context, sessionID string) (map[string]int64, error) {
	var rows []struct {
		Attendance string
		N          int64
	}
	err := dbpg.Conn(ctx, r.db).Table("exam_session_participants").Select("attendance, count(*) AS n").
		Where("session_id = ?", sessionID).Group("attendance").Scan(&rows).Error
	out := map[string]int64{}
	for _, row := range rows {
		out[row.Attendance] = row.N
	}
	return out, wrap(err, "attendance counts")
}

func (r *SessionRepo) ActiveUserIDs(ctx context.Context, ids []string) ([]string, error) {
	out := []string{}
	if len(ids) == 0 {
		return out, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("users").Where("id IN ? AND deleted_at IS NULL AND status = 'active'", ids).
		Pluck("id", &out).Error
	return out, wrap(err, "active users")
}

func (r *SessionRepo) ExistingGroupIDs(ctx context.Context, ids []string) ([]string, error) {
	out := []string{}
	if len(ids) == 0 {
		return out, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("groups").Where("id IN ? AND deleted_at IS NULL", ids).Pluck("id", &out).Error
	return out, wrap(err, "existing groups")
}

func (r *SessionRepo) GroupMemberIDs(ctx context.Context, groupIDs []string) ([]string, error) {
	out := []string{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("group_members gm").Distinct("gm.user_id").
		Joins("JOIN users u ON u.id = gm.user_id AND u.deleted_at IS NULL AND u.status = 'active'").
		Where("gm.group_id IN ? AND gm.member_role = 'member'", groupIDs).Pluck("gm.user_id", &out).Error
	return out, wrap(err, "group members")
}

func (r *SessionRepo) AddParticipants(ctx context.Context, rows []modelSession.ParticipantEntity) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	res := dbpg.Conn(ctx, r.db).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 500)
	return res.RowsAffected, wrap(res.Error, "add participants")
}

func (r *SessionRepo) AddSessionGroups(ctx context.Context, rows []modelSession.SessionGroupEntity) error {
	if len(rows) == 0 {
		return nil
	}
	return wrap(dbpg.Conn(ctx, r.db).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error, "add session groups")
}

func (r *SessionRepo) ListParticipants(ctx context.Context, sessionID string, f modelSession.ParticipantFilter) ([]modelSession.ParticipantDTO, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("exam_session_participants sp").
		Joins("JOIN users u ON u.id = sp.user_id").Where("sp.session_id = ?", sessionID)
	if f.Attendance != "" {
		q = q.Where("sp.attendance = ?", f.Attendance)
	}
	if f.UserID != "" {
		q = q.Where("sp.user_id = ?", f.UserID)
	}
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		q = q.Where("(u.username ILIKE ? OR u.full_name ILIKE ? OR u.external_id ILIKE ?)", p, p, p)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count participants: %w", err)
	}
	out := []modelSession.ParticipantDTO{}
	err := q.Session(&gorm.Session{}).
		Select("sp.user_id, u.username, u.full_name, u.external_id, sp.extra_time_minutes, sp.attendance, sp.seat, sp.notes, sp.added_at").
		Order("u.full_name, u.id").Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Scan(&out).Error
	return out, total, wrap(err, "list participants")
}

func (r *SessionRepo) PrintParticipants(ctx context.Context, sessionID string) ([]modelSession.PrintParticipantDTO, error) {
	out := []modelSession.PrintParticipantDTO{}
	err := dbpg.Conn(ctx, r.db).Table("exam_session_participants sp").
		Joins("JOIN users u ON u.id = sp.user_id").Where("sp.session_id = ?", sessionID).
		Select(`sp.user_id, u.username, u.full_name, u.external_id, sp.seat, sp.attendance,
			(SELECT a.status FROM attempts a WHERE a.session_id = sp.session_id AND a.user_id = sp.user_id
			 ORDER BY a.started_at DESC LIMIT 1) AS attempt_status`).
		// seats sort naturally ("2" before "10"); participants without a seat come last
		Order("sp.seat IS NULL, length(sp.seat), sp.seat, u.full_name, u.id").
		Scan(&out).Error
	return out, wrap(err, "print participants")
}

func (r *SessionRepo) FindTenant(ctx context.Context, id string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&t).Error
	return t, wrap(err, "find tenant")
}

func (r *SessionRepo) FindParticipant(ctx context.Context, sessionID, userID string) (modelSession.ParticipantEntity, error) {
	var p modelSession.ParticipantEntity
	err := dbpg.Conn(ctx, r.db).Where("session_id = ? AND user_id = ?", sessionID, userID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, errParticipantNotFound
	}
	return p, wrap(err, "find participant")
}

func (r *SessionRepo) UpdateParticipant(ctx context.Context, p *modelSession.ParticipantEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelSession.ParticipantEntity{}).
		Where("session_id = ? AND user_id = ?", p.SessionID, p.UserID).
		Updates(map[string]any{"extra_time_minutes": p.ExtraTimeMinutes, "attendance": p.Attendance, "seat": p.Seat, "notes": p.Notes}).Error
	return wrap(err, "update participant")
}

func (r *SessionRepo) DeleteParticipant(ctx context.Context, sessionID, userID string) error {
	err := dbpg.Conn(ctx, r.db).Where("session_id = ? AND user_id = ?", sessionID, userID).Delete(&modelSession.ParticipantEntity{}).Error
	return wrap(err, "delete participant")
}

func (r *SessionRepo) MySessions(ctx context.Context, userID string, since time.Time) ([]modelSession.MySessionDTO, error) {
	out := []modelSession.MySessionDTO{}
	err := dbpg.Conn(ctx, r.db).Table("exam_session_participants sp").
		Select(`es.id AS session_id, es.exam_id, e.title AS exam_title, es.name, es.room, es.starts_at, es.ends_at, es.status,
			es.token_required, `+totalMinutesSQL+` AS total_minutes, sp.extra_time_minutes, sp.seat`).
		Joins("JOIN exam_sessions es ON es.id = sp.session_id").
		Joins("JOIN exams e ON e.id = es.exam_id").
		Where("sp.user_id = ? AND es.ends_at > ? AND es.status <> 'cancelled'", userID, since).
		Order("es.starts_at").Limit(100).Scan(&out).Error
	return out, wrap(err, "my sessions")
}

func (r *SessionRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	return wrap(dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error, "create audit log")
}

func wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	if dbpg.IsUniqueViolation(err) {
		return fmt.Errorf("%s: %w", op, errr.ErrConflict)
	}
	return fmt.Errorf("%s: %w", op, err)
}
