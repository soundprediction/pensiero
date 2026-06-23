package reasoning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Fingerprint returns a stable hash of the registry semantics used for reasoning.
func (r *PredicateRegistry) Fingerprint() string {
	var b strings.Builder
	if r != nil {
		for _, canon := range r.AllCanonical() {
			meta := r.byCanon[normKey(canon)]
			parents := append([]string{}, meta.SubPropertyOf...)
			sort.Slice(parents, func(i, j int) bool {
				return normKey(parents[i]) < normKey(parents[j])
			})
			fmt.Fprintf(&b, "p\t%s\t%d\t%s", meta.Canonical, meta.Chars, meta.InverseOf)
			for _, parent := range parents {
				fmt.Fprintf(&b, "\t%s", parent)
			}
			b.WriteByte('\n')
		}

		rawKeys := make([]string, 0, len(r.byRaw))
		for raw := range r.byRaw {
			rawKeys = append(rawKeys, raw)
		}
		sort.Strings(rawKeys)
		for _, raw := range rawKeys {
			meta := r.byRaw[raw]
			fmt.Fprintf(&b, "r\t%s\t%s\n", raw, meta.Canonical)
		}

		comps := append([]CompositionRule{}, r.comps...)
		sort.Slice(comps, func(i, j int) bool {
			return compositionKey(comps[i]) < compositionKey(comps[j])
		})
		for _, comp := range comps {
			fmt.Fprintf(&b, "c\t%s\t%s\t%s\n", comp.First, comp.Second, comp.Result)
		}

		disjoints := append([]DisjointPair{}, r.disjoint...)
		sort.Slice(disjoints, func(i, j int) bool {
			return disjointKey(disjoints[i]) < disjointKey(disjoints[j])
		})
		for _, disjoint := range disjoints {
			a, c := canonicalDisjointPair(disjoint)
			fmt.Fprintf(&b, "d\t%s\t%s\n", a, c)
		}
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func canonicalDisjointPair(pair DisjointPair) (string, string) {
	a, b := strings.TrimSpace(pair.A), strings.TrimSpace(pair.B)
	if normKey(b) < normKey(a) {
		return b, a
	}
	return a, b
}
