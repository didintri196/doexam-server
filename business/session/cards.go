package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSession "doexam/models/session"
	"doexam/pkg/errr"
	"doexam/pkg/securetoken"
)

// cardPasswordLen keeps card passwords short enough to type from paper
// (8 characters of a 32-symbol alphabet ≈ 40 bits; logins are rate limited).
const cardPasswordLen = 8

// GenerateCardPasswords gives participants of a session new system passwords to print
// on their cards (session manager with user.manage). By decision of the product owner
// they are not "temporary": participants keep them (must_change_password = false).
// Passwords are returned once and never stored, logged or audited.
func (s *SessionService) GenerateCardPasswords(ctx context.Context, p modelAuth.Principal, sessionID string, in modelSession.CardPasswordsInput, client modelAudit.Client) (modelSession.CardPasswordsDTO, error) {
	ctx, span := s.tracer.Start(ctx, "SessionService.GenerateCardPasswords", trace.WithAttributes(attribute.String("session.id", sessionID)))
	defer span.End()

	if !p.Has(modelAuth.PermUserManage) {
		return modelSession.CardPasswordsDTO{}, fail(span, errr.ErrForbidden)
	}
	onlyNew := in.OnlyNew == nil || *in.OnlyNew

	var targets []string
	out := modelSession.CardPasswordsDTO{Passwords: []modelSession.CardPasswordDTO{}}
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if _, _, err := s.loadManaged(ctx, p, sessionID); err != nil {
			return err
		}
		cands, err := s.storer.CardCandidates(ctx, sessionID)
		if err != nil {
			return err
		}
		for _, c := range cands {
			if !c.Active || (onlyNew && c.SignedIn) {
				out.Skipped++
				continue
			}
			targets = append(targets, c.UserID)
		}
		return nil
	})
	if err != nil {
		return modelSession.CardPasswordsDTO{}, fail(span, err)
	}
	if len(targets) > modelSession.MaxCardPasswords {
		return modelSession.CardPasswordsDTO{}, fail(span, &errr.ValidationError{Fields: map[string]string{
			"session": fmt.Sprintf("max %d participants per request", modelSession.MaxCardPasswords)}})
	}
	if len(targets) == 0 {
		return out, nil
	}

	// Argon2 is slow on purpose: hash outside the transaction, in parallel (the hasher
	// bounds the concurrency itself).
	plain := make([]string, len(targets))
	hashes := make(map[string]string, len(targets))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for i, id := range targets {
		pw, err := securetoken.ReadablePassword(cardPasswordLen)
		if err != nil {
			return modelSession.CardPasswordsDTO{}, fail(span, err)
		}
		plain[i] = pw
		wg.Add(1)
		go func(id, pw string) {
			defer wg.Done()
			h, err := s.hasher.Hash(ctx, pw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			hashes[id] = h
		}(id, pw)
	}
	wg.Wait()
	if firstErr != nil {
		return modelSession.CardPasswordsDTO{}, fail(span, firstErr)
	}

	err = s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		if err := s.storer.SetCardPasswords(ctx, hashes); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionCardPasswords,
			EntityType: "exam_session", EntityID: sessionID, Client: client,
			After: map[string]any{"user_ids": targets, "only_new": onlyNew}, // never the passwords
		}))
	})
	if err != nil {
		return modelSession.CardPasswordsDTO{}, fail(span, err)
	}
	for i, id := range targets {
		out.Passwords = append(out.Passwords, modelSession.CardPasswordDTO{UserID: id, Password: plain[i]})
	}
	s.logger.InfoContext(ctx, "card passwords generated", slog.String("session_id", sessionID), slog.Int("count", len(targets)))
	return out, nil
}
