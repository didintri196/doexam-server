package importer

import (
	"context"
	"fmt"
	"strings"

	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	modelUser "doexam/models/user"
	"doexam/pkg/spreadsheet"
)

// userPlan is one validated user row, ready to insert (password not hashed yet).
type userPlan struct {
	line       int
	user       modelAuth.UserEntity
	password   string // empty → generate
	mustChange bool
	roleIDs    []string
	groupIDs   []string
}

// planUsers validates rows against each other and the database. It must run in a tenant transaction.
func (s *ImportService) planUsers(ctx context.Context, tenantID string, rows []spreadsheet.Row) ([]userPlan, []modelImport.RowError, error) {
	var (
		plans []userPlan
		errs  rowErrors
	)
	type parsed struct {
		plan       userPlan
		roleCodes  []string
		groupCodes []string
		ok         bool
	}
	items := make([]parsed, 0, len(rows))
	seenUser, seenEmail, seenExt := map[string]int{}, map[string]int{}, map[string]int{}
	var usernames, emails, extIDs, roleCodes, groupCodes []string

	for _, r := range rows {
		before := len(errs)
		p := userPlan{line: r.Line, mustChange: true}
		u := &p.user
		u.TenantID = tenantID
		u.Status = modelAuth.UserStatusActive
		u.Username = r.Get(colUsername)
		u.FullName = r.Get(colFullName)
		u.Email = optional(r.Get(colEmail))
		u.ExternalID = optional(r.Get(colExternalID))
		p.password = r.Get(colPassword)

		errs.check(modelAuth.ValidUsername(u.Username), r.Line, colUsername, modelAuth.UsernameRule)
		errs.check(u.FullName != "" && len(u.FullName) <= 200, r.Line, colFullName, "required, max 200 chars")
		errs.check(u.Email == nil || modelUser.ValidEmail(*u.Email), r.Line, colEmail, "must be a valid e-mail address")
		errs.check(u.ExternalID == nil || len(*u.ExternalID) <= 100, r.Line, colExternalID, "max 100 chars")
		errs.check(p.password == "" || modelAuth.ValidPassword(p.password), r.Line, colPassword,
			fmt.Sprintf("must be %d-%d characters", modelAuth.PasswordMinLen, modelAuth.PasswordMaxBytes))
		if v := r.Get(colMustChange); v != "" {
			b, ok := parseBool(v)
			errs.check(ok, r.Line, colMustChange, "must be ya or tidak")
			p.mustChange = b || !ok
		}

		// duplicates inside the file (username & email are case-insensitive, like citext)
		if u.Username != "" {
			key := strings.ToLower(u.Username)
			if first, dup := seenUser[key]; dup {
				errs.add(r.Line, colUsername, fmt.Sprintf("duplicate of row %d", first))
			} else {
				seenUser[key] = r.Line
				usernames = append(usernames, u.Username)
			}
		}
		if u.Email != nil {
			key := strings.ToLower(*u.Email)
			if first, dup := seenEmail[key]; dup {
				errs.add(r.Line, colEmail, fmt.Sprintf("duplicate of row %d", first))
			} else {
				seenEmail[key] = r.Line
				emails = append(emails, *u.Email)
			}
		}
		if u.ExternalID != nil {
			if first, dup := seenExt[*u.ExternalID]; dup {
				errs.add(r.Line, colExternalID, fmt.Sprintf("duplicate of row %d", first))
			} else {
				seenExt[*u.ExternalID] = r.Line
				extIDs = append(extIDs, *u.ExternalID)
			}
		}

		it := parsed{plan: p, roleCodes: lowerAll(splitList(r.Get(colRoles))), groupCodes: splitList(r.Get(colGroups))}
		if len(it.roleCodes) == 0 {
			it.roleCodes = []string{modelAuth.RoleParticipant}
		}
		roleCodes = append(roleCodes, it.roleCodes...)
		groupCodes = append(groupCodes, it.groupCodes...)
		it.ok = len(errs) == before
		items = append(items, it)
	}

	// against the database
	takenUser, err := s.storer.ExistingUsernames(ctx, usernames)
	if err != nil {
		return nil, nil, err
	}
	takenEmail, err := s.storer.ExistingEmails(ctx, emails)
	if err != nil {
		return nil, nil, err
	}
	takenExt, err := s.storer.ExistingExternalIDs(ctx, extIDs)
	if err != nil {
		return nil, nil, err
	}
	roles, err := s.storer.RolesByCodes(ctx, uniq(roleCodes))
	if err != nil {
		return nil, nil, err
	}
	groups, err := s.storer.GroupsByCodes(ctx, uniq(groupCodes))
	if err != nil {
		return nil, nil, err
	}
	roleByCode := map[string]string{}
	for _, r := range roles {
		roleByCode[r.Code] = r.ID
	}
	groupsByCode := map[string][]modelGroup.GroupEntity{}
	for _, g := range groups {
		groupsByCode[*g.Code] = append(groupsByCode[*g.Code], g)
	}
	userTaken := lowerSet(takenUser)
	emailTaken := lowerSet(takenEmail)
	extTaken := map[string]bool{}
	for _, v := range takenExt {
		extTaken[v] = true
	}

	for _, it := range items {
		before := len(errs)
		p, u := it.plan, it.plan.user
		errs.check(!userTaken[strings.ToLower(u.Username)], p.line, colUsername, "already in use")
		errs.check(u.Email == nil || !emailTaken[strings.ToLower(*u.Email)], p.line, colEmail, "already in use")
		errs.check(u.ExternalID == nil || !extTaken[*u.ExternalID], p.line, colExternalID, "already in use")
		for _, c := range it.roleCodes {
			id, ok := roleByCode[c]
			errs.check(ok, p.line, colRoles, "unknown role: "+c)
			p.roleIDs = appendUnique(p.roleIDs, id, ok)
		}
		for _, c := range it.groupCodes {
			switch matches := groupsByCode[c]; len(matches) {
			case 0:
				errs.add(p.line, colGroups, "unknown group: "+c)
			case 1:
				p.groupIDs = appendUnique(p.groupIDs, matches[0].ID, true)
			default:
				errs.add(p.line, colGroups, "ambiguous group code (used in several periods): "+c)
			}
		}
		if it.ok && len(errs) == before {
			plans = append(plans, p)
		}
	}
	return plans, errs, nil
}

func lowerAll(in []string) []string {
	for i := range in {
		in[i] = strings.ToLower(in[i])
	}
	return in
}

func lowerSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, v := range in {
		out[strings.ToLower(v)] = true
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func appendUnique(list []string, v string, ok bool) []string {
	if !ok {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
