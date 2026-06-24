package reasoning

import (
	"fmt"
	"strings"
)

// DomainRangeCheck performs a pure advisory check of a predicate's soft
// domain/range against supplied head and tail entity labels. Empty declarations
// always pass. This helper is intentionally not wired into reasoning verdicts.
func DomainRangeCheck(reg *PredicateRegistry, predicate string, headTypes, tailTypes []string) (ok bool, reason string) {
	if reg == nil {
		return true, ""
	}
	meta, declared := reg.Canonical(predicate)
	if !declared {
		return true, ""
	}

	var reasons []string
	if !typeListSatisfies(reg, headTypes, meta.Domain) {
		reasons = append(reasons, fmt.Sprintf("head types %v do not satisfy domain %v for predicate %q", trimmedStrings(headTypes), meta.Domain, meta.Canonical))
	}
	if !typeListSatisfies(reg, tailTypes, meta.Range) {
		reasons = append(reasons, fmt.Sprintf("tail types %v do not satisfy range %v for predicate %q", trimmedStrings(tailTypes), meta.Range, meta.Canonical))
	}
	if len(reasons) > 0 {
		return false, strings.Join(reasons, "; ")
	}
	return true, ""
}

func typeListSatisfies(reg *PredicateRegistry, actual []string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, got := range trimmedStrings(actual) {
		for _, want := range allowed {
			if typeMatches(reg, got, want) {
				return true
			}
		}
	}
	return false
}

func typeMatches(reg *PredicateRegistry, actual string, allowed string) bool {
	if normKey(actual) == "" || normKey(allowed) == "" {
		return false
	}
	if normKey(actual) == normKey(allowed) {
		return true
	}
	return reg != nil && reg.types != nil && reg.types.IsA(actual, allowed)
}
