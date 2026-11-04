package importer

import (
	"context"
	"fmt"
	"slices"
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

// userBatch is the outcome of planning a user import.
type userBatch struct {
	create []userPlan
	// importing into a group: existing accounts to add, and rows already members
	addExisting   []string // user ids
	alreadyMember int
	warnings      []modelImport.RowError
}

// valid is the number of rows that will be imported (created, added or skipped as members).
func (b userBatch) valid() int { return len(b.create) + len(b.addExisting) + b.alreadyMember }

// planUsers validates rows against each other and the database for the import
// target t (DOCS/PLAN/13-IMPOR-PER-MENU.md). It must run in a tenant transaction.
func (s *ImportService) planUsers(ctx context.Context, tenantID string, t modelImport.Target, rows []spreadsheet.Row) (userBatch, []modelImport.RowError, error) {
	var (
		batch userBatch
		errs  rowErrors
		warns rowErrors
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
		switch t.Audience {
		case modelImport.AudienceStaff:
			errs.check(len(it.roleCodes) > 0, r.Line, colRoles, "required: staff roles such as proctor or author")
			if len(it.groupCodes) > 0 {
				warns.add(r.Line, colGroups, "ignored: staff are not added to groups by this import")
				it.groupCodes = nil
			}
		case modelImport.AudienceParticipant:
			if len(it.roleCodes) > 0 {
				warns.add(r.Line, colRoles, "ignored: every row gets the participant role")
			}
			it.roleCodes = []string{modelAuth.RoleParticipant}
			if t.GroupID != "" && len(it.groupCodes) > 0 {
				warns.add(r.Line, colGroups, "ignored: every row goes to the chosen group")
				it.groupCodes = nil
			}
		default:
			if len(it.roleCodes) == 0 {
				it.roleCodes = []string{modelAuth.RoleParticipant}
			}
		}
		roleCodes = append(roleCodes, it.roleCodes...)
		groupCodes = append(groupCodes, it.groupCodes...)
		it.ok = len(errs) == before
		items = append(items, it)
	}

	// against the database
	takenUser, err := s.storer.ExistingUsernames(ctx, usernames)
	if err != nil {
		return userBatch{}, nil, err
	}
	takenEmail, err := s.storer.ExistingEmails(ctx, emails)
	if err != nil {
		return userBatch{}, nil, err
	}
	takenExt, err := s.storer.ExistingExternalIDs(ctx, extIDs)
	if err != nil {
		return userBatch{}, nil, err
	}
	roles, err := s.storer.RolesByCodes(ctx, uniq(roleCodes))
	if err != nil {
		return userBatch{}, nil, err
	}
	groups, err := s.storer.GroupsByCodes(ctx, uniq(groupCodes))
	if err != nil {
		return userBatch{}, nil, err
	}
	roleByCode := map[string]modelAuth.RoleEntity{}
	for _, r := range roles {
		roleByCode[r.Code] = r
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

	// importing into a group: rows naming an existing account join the group instead
	var (
		byUsername = map[string]modelAuth.UserEntity{}
		byExt      = map[string]modelAuth.UserEntity{}
		members    = map[string]bool{}
	)
	if t.GroupID != "" {
		existing, err := s.storer.UsersByLogin(ctx, usernames, extIDs)
		if err != nil {
			return userBatch{}, nil, err
		}
		ids := make([]string, 0, len(existing))
		for _, u := range existing {
			byUsername[strings.ToLower(u.Username)] = u
			if u.ExternalID != nil {
				byExt[*u.ExternalID] = u
			}
			ids = append(ids, u.ID)
		}
		memberIDs, err := s.storer.GroupMemberIDs(ctx, t.GroupID, ids)
		if err != nil {
			return userBatch{}, nil, err
		}
		for _, id := range memberIDs {
			members[id] = true
		}
	}

	for _, it := range items {
		before := len(errs)
		p, u := it.plan, it.plan.user
		if t.GroupID != "" && it.ok {
			found, matched := existingAccount(u, byUsername, byExt, &errs, p.line)
			if matched {
				if len(errs) > before {
					continue
				}
				switch {
				case found.Status != modelAuth.UserStatusActive:
					errs.add(p.line, colUsername, "account is suspended: activate it first")
				case members[found.ID]:
					batch.alreadyMember++
				default:
					batch.addExisting = append(batch.addExisting, found.ID)
					members[found.ID] = true // a second row for the same account is a duplicate member
				}
				if !strings.EqualFold(strings.TrimSpace(found.FullName), strings.TrimSpace(u.FullName)) {
					warns.add(p.line, colFullName, fmt.Sprintf("existing account %s is named %q; its data is not changed", found.Username, found.FullName))
				}
				continue
			}
		}
		errs.check(!userTaken[strings.ToLower(u.Username)], p.line, colUsername, "already in use")
		errs.check(u.Email == nil || !emailTaken[strings.ToLower(*u.Email)], p.line, colEmail, "already in use")
		errs.check(u.ExternalID == nil || !extTaken[*u.ExternalID], p.line, colExternalID, "already in use")
		for _, c := range it.roleCodes {
			r, ok := roleByCode[c]
			errs.check(ok, p.line, colRoles, "unknown role: "+c)
			if ok && t.Audience == modelImport.AudienceStaff {
				if !isStaffRole(r) {
					errs.add(p.line, colRoles, "participant role: import participants from the Peserta page")
					ok = false
				} else if slices.Contains(r.Permissions, modelAuth.PermTenantManage) {
					warns.add(p.line, colRoles, "creates an administrator account")
				}
			}
			p.roleIDs = appendUnique(p.roleIDs, r.ID, ok)
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
		if t.GroupID != "" {
			p.groupIDs = appendUnique(p.groupIDs, t.GroupID, true)
		}
		if it.ok && len(errs) == before {
			batch.create = append(batch.create, p)
		}
	}
	for i := range warns {
		warns[i].Warning = true
	}
	batch.warnings = warns
	return batch, errs, nil
}

// existingAccount finds the account a row names by username or external id. Both
// pointing at different accounts is an error on the row.
func existingAccount(u modelAuth.UserEntity, byUsername, byExt map[string]modelAuth.UserEntity, errs *rowErrors, line int) (modelAuth.UserEntity, bool) {
	a, okA := byUsername[strings.ToLower(u.Username)]
	var b modelAuth.UserEntity
	okB := false
	if u.ExternalID != nil {
		b, okB = byExt[*u.ExternalID]
	}
	switch {
	case okA && okB && a.ID != b.ID:
		errs.add(line, colExternalID, fmt.Sprintf("belongs to account %s, not %s", b.Username, a.Username))
		return a, true
	case okA:
		return a, true
	case okB:
		return b, true
	}
	return modelAuth.UserEntity{}, false
}

// isStaffRole: a role with any permission besides taking exams (the panel's rule).
func isStaffRole(r modelAuth.RoleEntity) bool {
	return slices.ContainsFunc(r.Permissions, func(p string) bool { return p != modelAuth.PermAttemptTake })
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
