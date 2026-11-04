package user

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	"doexam/pkg/errr"
	"doexam/pkg/spreadsheet"
)

// resolvePerms may resolve identifiers: the lists are used to add users to groups and
// session participants, so group and session managers need it too, not only user admins.
var resolvePerms = []string{modelAuth.PermUserManage, modelAuth.PermGroupManage, modelAuth.PermSessionManage}

// fileColumns are the headers ResolveFile looks for, in order. Without any of them the
// first column is used.
var fileColumns = []string{"username", "external_id", "id_peserta", "email"}

// Resolve finds the users behind usernames, external ids or e-mails (case-insensitive).
func (s *UserService) Resolve(ctx context.Context, p modelAuth.Principal, in modelUser.ResolveInput) (modelUser.ResolveResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.Resolve", trace.WithAttributes(attribute.Int("identifiers", len(in.Identifiers))))
	defer span.End()

	idents := cleanIdentifiers(in.Identifiers)
	v := errr.Validation{}
	v.Check(len(idents) > 0 && len(idents) <= modelUser.MaxResolveIdentifiers, "identifiers",
		fmt.Sprintf("1-%d identifiers", modelUser.MaxResolveIdentifiers))
	if err := v.Err(); err != nil {
		return modelUser.ResolveResultDTO{}, fail(span, err)
	}
	return s.resolve(ctx, span, p, idents)
}

// ResolveFile reads identifiers from an .xlsx/.csv file (column username, external_id,
// id_peserta or email, else the first column) and resolves them like Resolve.
func (s *UserService) ResolveFile(ctx context.Context, p modelAuth.Principal, filename string, data []byte) (modelUser.ResolveResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "UserService.ResolveFile")
	defer span.End()

	if !slices.ContainsFunc(resolvePerms, p.Has) {
		return modelUser.ResolveResultDTO{}, fail(span, errr.ErrForbidden)
	}
	if len(data) > modelUser.MaxResolveFileBytes {
		return modelUser.ResolveResultDTO{}, fail(span, fileError("max 5 MB"))
	}
	format, err := spreadsheet.DetectFormat(filename, data)
	if err != nil {
		return modelUser.ResolveResultDTO{}, fail(span, fileError("use .xlsx or .csv"))
	}
	sheet, err := spreadsheet.Read(data, format, modelUser.MaxResolveIdentifiers)
	switch {
	case errors.Is(err, spreadsheet.ErrTooManyRows):
		return modelUser.ResolveResultDTO{}, fail(span, fileError(fmt.Sprintf("max %d rows", modelUser.MaxResolveIdentifiers)))
	case errors.Is(err, spreadsheet.ErrNoHeader):
		return modelUser.ResolveResultDTO{}, fail(span, fileError("file is empty"))
	case err != nil:
		return modelUser.ResolveResultDTO{}, fail(span, fileError("cannot read file"))
	}

	col := sheet.Headers[0]
	for _, c := range fileColumns {
		if slices.Contains(sheet.Headers, c) {
			col = c
			break
		}
	}
	raw := make([]string, 0, len(sheet.Rows))
	for _, r := range sheet.Rows {
		raw = append(raw, r.Get(col))
	}
	idents := cleanIdentifiers(raw)
	if len(idents) == 0 {
		return modelUser.ResolveResultDTO{}, fail(span, fileError("no identifiers in column "+col))
	}
	return s.resolve(ctx, span, p, idents)
}

func (s *UserService) resolve(ctx context.Context, span trace.Span, p modelAuth.Principal, idents []string) (modelUser.ResolveResultDTO, error) {
	if !slices.ContainsFunc(resolvePerms, p.Has) {
		return modelUser.ResolveResultDTO{}, fail(span, errr.ErrForbidden)
	}
	keys := make([]string, len(idents))
	for i, id := range idents {
		keys[i] = strings.ToLower(id)
	}
	var out modelUser.ResolveResultDTO
	err := s.tx.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		users, err := s.storer.FindByIdentifiers(ctx, keys)
		if err != nil {
			return err
		}
		out = matchIdentifiers(idents, users)
		return nil
	})
	if err != nil {
		return modelUser.ResolveResultDTO{}, fail(span, err)
	}
	span.SetAttributes(attribute.Int("found", len(out.Found)), attribute.Int("not_found", len(out.NotFound)))
	return out, nil
}

// matchIdentifiers maps each identifier to the users whose username, external_id or
// e-mail equals it (case-insensitive). A user found by several identifiers is listed once.
func matchIdentifiers(idents []string, users []modelAuth.UserEntity) modelUser.ResolveResultDTO {
	type hit struct {
		user  modelAuth.UserEntity
		field modelUser.MatchField
	}
	byKey := map[string][]hit{}
	add := func(key string, u modelAuth.UserEntity, f modelUser.MatchField) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return
		}
		for _, h := range byKey[key] {
			if h.user.ID == u.ID {
				return // same user via another field: keep the first (username wins)
			}
		}
		byKey[key] = append(byKey[key], hit{u, f})
	}
	for _, u := range users {
		add(u.Username, u, modelUser.MatchUsername)
		if u.ExternalID != nil {
			add(*u.ExternalID, u, modelUser.MatchExternalID)
		}
		if u.Email != nil {
			add(*u.Email, u, modelUser.MatchEmail)
		}
	}

	out := modelUser.ResolveResultDTO{Found: []modelUser.ResolvedUserDTO{}, NotFound: []string{}, Ambiguous: []string{}}
	seen := map[string]bool{}
	for _, id := range idents {
		hits := byKey[strings.ToLower(id)]
		switch {
		case len(hits) == 0:
			out.NotFound = append(out.NotFound, id)
		case len(hits) > 1:
			out.Ambiguous = append(out.Ambiguous, id)
		case !seen[hits[0].user.ID]:
			u := hits[0].user
			seen[u.ID] = true
			out.Found = append(out.Found, modelUser.ResolvedUserDTO{
				ID: u.ID, Username: u.Username, FullName: u.FullName, ExternalID: u.ExternalID,
				Status: u.Status, Input: id, MatchedBy: hits[0].field,
			})
		}
	}
	return out
}

// cleanIdentifiers trims identifiers, drops empty ones and case-insensitive duplicates.
func cleanIdentifiers(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

func fileError(msg string) error {
	return &errr.ValidationError{Fields: map[string]string{"file": msg}}
}
