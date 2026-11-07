package dbpg

import (
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is a PostgreSQL unique_violation (23505).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsForeignKeyViolation reports whether err is a PostgreSQL foreign_key_violation (23503).
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// ContainsPattern turns user input into an ILIKE pattern matching it anywhere.
// % _ and \ are escaped, so the input is matched literally (default escape char is \).
func ContainsPattern(q string) string {
	return "%" + likeEscaper.Replace(q) + "%"
}

// PrefixPattern turns user input into an ILIKE pattern matching values that start with it
// (escaped like ContainsPattern).
func PrefixPattern(q string) string {
	return likeEscaper.Replace(q) + "%"
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ConstraintName returns the violated constraint/index name of a PostgreSQL error, or "".
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// PrefixTSQuery turns free text into a to_tsquery expression that matches every
// word as a prefix ("pecah bia" → "pecah:* & bia:*"). Only letters and digits
// survive, so the result is always a valid, injection-free tsquery. Empty when no word.
func PrefixTSQuery(q string) string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > 8 {
		words = words[:8]
	}
	for i, w := range words {
		words[i] = w + ":*"
	}
	return strings.Join(words, " & ")
}
