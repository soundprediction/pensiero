package reasoning

import (
	"regexp"
	"strings"
)

var (
	entityParenGroup = regexp.MustCompile(`\([^)]*\)`)
	entityNonAlnum   = regexp.MustCompile(`[^a-z0-9]+`)
)

// normalizeEntityForMatch produces a loose, formatting-insensitive key for
// matching conditional-rule entities against claim / assumed-fact entities. It is
// used ONLY in the rule layer (NOT for graph node lookups): it lowercases, drops
// parenthetical qualifiers (so "Pulmonary Embolism (PE)" -> "pulmonary embolism"),
// and collapses punctuation/whitespace (so "second-trimester pregnant woman"
// matches "Second trimester pregnant woman"). It does NOT do synonym or concept
// resolution — that is a deeper layer (e.g. resolving to OMOP concepts).
func normalizeEntityForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = entityParenGroup.ReplaceAllString(s, " ")
	s = entityNonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// entityMatches reports whether two entity strings denote the same entity for
// rule-matching purposes, after formatting normalization.
func entityMatches(a, b string) bool {
	na := normalizeEntityForMatch(a)
	return na != "" && na == normalizeEntityForMatch(b)
}
