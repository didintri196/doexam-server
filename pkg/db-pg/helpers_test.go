package dbpg

import "testing"

func TestContainsPattern(t *testing.T) {
	if got := ContainsPattern(`50%_a\b`); got != `%50\%\_a\\b%` {
		t.Fatalf("ContainsPattern = %q", got)
	}
}

func TestPrefixTSQuery(t *testing.T) {
	if got := PrefixTSQuery(`Pecahan  bia'); DROP--`); got != "pecahan:* & bia:* & drop:*" {
		t.Fatalf("PrefixTSQuery = %q", got)
	}
	if got := PrefixTSQuery(" ;; "); got != "" {
		t.Fatalf("PrefixTSQuery = %q, want empty", got)
	}
}

func TestPrefixPattern(t *testing.T) {
	if got := PrefixPattern(`A-1_%`); got != `A-1\_\%%` {
		t.Fatalf("PrefixPattern = %q", got)
	}
}
