package importer

import (
	"context"
	"fmt"

	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	"doexam/pkg/spreadsheet"
)

// groupRow is one parsed group row.
type groupRow struct {
	line       int
	group      modelGroup.GroupEntity
	parentCode string
	parentRow  *groupRow // parent defined in the same file
}

// planGroups validates rows and returns the groups in insertion order (parents first).
// It must run in a tenant transaction.
func (s *ImportService) planGroups(ctx context.Context, tenantID string, rows []spreadsheet.Row) ([]modelGroup.GroupEntity, []modelImport.RowError, error) {
	var errs rowErrors
	parsed := make([]*groupRow, 0, len(rows))
	seen := map[string]int{} // code|period → line
	var codes []string

	for _, r := range rows {
		g := modelGroup.GroupEntity{
			ID: newID(), TenantID: tenantID, Kind: modelGroup.Kind(r.Get(colKind)),
			Code: optional(r.Get(colCode)), Name: r.Get(colName), Period: optional(r.Get(colPeriod)),
		}
		gr := &groupRow{line: r.Line, group: g, parentCode: r.Get(colParentCode)}
		errs.check(g.Code != nil && len(*g.Code) <= 50, r.Line, colCode, "required, max 50 chars")
		errs.check(g.Name != "" && len(g.Name) <= 200, r.Line, colName, "required, max 200 chars")
		errs.check(modelGroup.ValidKind(g.Kind), r.Line, colKind, "must be level, class, department, team, batch or other")
		errs.check(g.Period == nil || len(*g.Period) <= 50, r.Line, colPeriod, "max 50 chars")
		if g.Code != nil {
			key := *g.Code + "|" + deref(g.Period)
			if first, dup := seen[key]; dup {
				errs.add(r.Line, colCode, fmt.Sprintf("duplicate of row %d (same code and period)", first))
			} else {
				seen[key] = r.Line
			}
			codes = append(codes, *g.Code)
		}
		if gr.parentCode != "" {
			codes = append(codes, gr.parentCode)
		}
		parsed = append(parsed, gr)
	}

	existing, err := s.storer.GroupsByCodes(ctx, uniq(codes))
	if err != nil {
		return nil, nil, err
	}
	dbByCode := map[string][]modelGroup.GroupEntity{}
	for _, g := range existing {
		dbByCode[*g.Code] = append(dbByCode[*g.Code], g)
	}
	fileByCode := map[string][]*groupRow{}
	for _, gr := range parsed {
		if gr.group.Code != nil {
			fileByCode[*gr.group.Code] = append(fileByCode[*gr.group.Code], gr)
		}
	}

	for _, gr := range parsed {
		g := &gr.group
		if g.Code != nil {
			for _, e := range dbByCode[*g.Code] {
				if deref(e.Period) == deref(g.Period) {
					errs.add(gr.line, colCode, "group with this code and period already exists")
					break
				}
			}
		}
		if gr.parentCode == "" {
			continue
		}
		// candidates: rows in this file, then existing groups; prefer the same period
		var fileCands []*groupRow
		var dbCands []modelGroup.GroupEntity
		for _, c := range fileByCode[gr.parentCode] {
			if c != gr {
				fileCands = append(fileCands, c)
			}
		}
		dbCands = dbByCode[gr.parentCode]
		several := len(fileCands)+len(dbCands) > 1
		if several {
			fileCands = filter(fileCands, func(c *groupRow) bool { return deref(c.group.Period) == deref(g.Period) })
			dbCands = filter(dbCands, func(c modelGroup.GroupEntity) bool { return deref(c.Period) == deref(g.Period) })
		}
		switch {
		case len(fileCands)+len(dbCands) == 0 && !several:
			errs.add(gr.line, colParentCode, "parent group not found: "+gr.parentCode)
		case len(fileCands)+len(dbCands) != 1:
			errs.add(gr.line, colParentCode, "ambiguous parent code: "+gr.parentCode)
		case len(fileCands) == 1:
			gr.parentRow = fileCands[0]
			g.ParentID = &fileCands[0].group.ID
		default:
			g.ParentID = &dbCands[0].ID
		}
	}

	// order parents before children; a cycle inside the file is an error
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[*groupRow]int{}
	var ordered []modelGroup.GroupEntity
	var visit func(gr *groupRow) bool
	visit = func(gr *groupRow) bool {
		switch state[gr] {
		case visiting:
			return false
		case done:
			return true
		}
		state[gr] = visiting
		if gr.parentRow != nil && !visit(gr.parentRow) {
			return false
		}
		state[gr] = done
		ordered = append(ordered, gr.group)
		return true
	}
	for _, gr := range parsed {
		if !visit(gr) {
			errs.add(gr.line, colParentCode, "parent chain forms a cycle")
		}
	}
	if len(errs) > 0 {
		return nil, errs, nil
	}
	return ordered, nil, nil
}

func filter[T any](in []T, keep func(T) bool) []T {
	var out []T
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
