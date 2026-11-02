// Package pgtype holds column types shared by several models.
package pgtype

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// TextArray maps a PostgreSQL text[] column.
// pgx through database/sql returns arrays as their text literal, so we parse it here.
type TextArray []string

// Value encodes the array as a PostgreSQL array literal.
func (a TextArray) Value() (driver.Value, error) {
	if a == nil {
		return "{}", nil
	}
	quoted := make([]string, len(a))
	for i, s := range a {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		quoted[i] = `"` + s + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}", nil
}

// Scan decodes a one-dimensional PostgreSQL array literal.
func (a *TextArray) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("TextArray: unsupported type %T", src)
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return fmt.Errorf("TextArray: invalid literal %q", s)
	}
	body := s[1 : len(s)-1]
	out := TextArray{}
	if body == "" {
		*a = out
		return nil
	}

	var cur strings.Builder
	inQuotes, escaped, quotedElem := false, false, false
	flush := func() {
		elem := cur.String()
		if !quotedElem && elem == "NULL" {
			elem = ""
		}
		out = append(out, elem)
		cur.Reset()
		quotedElem = false
	}
	for _, r := range body {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
			quotedElem = true
		case r == ',' && !inQuotes:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	*a = out
	return nil
}
