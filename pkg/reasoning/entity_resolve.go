package reasoning

import (
	"context"
	"strings"
)

// Entity-name resolution.
//
// REASON_ENTAILS anchors a claim on EXACT entity names. Graphs store the source
// vocabulary's spelling — specific and inconsistently cased ("Autoimmune
// hypothyroidism", "hypothyroidism, congenital, nongoitrous", lowercase
// "acromegaly") — while callers ask about the generic clinical form
// ("Hypothyroidism"). Nothing matches, so every claim returns unsupported no
// matter how sound the traversal or how complete the predicate registry.
//
// This resolves server-side, where the graph is actually available, so every
// client benefits rather than only those that happen to embed their own
// resolver. It is deliberately conservative: a WRONG match would manufacture a
// "logically verified" claim about a patient, which is far worse than failing to
// prove a true one. Every strategy is exact or high-threshold, and an unresolved
// name is passed through unchanged (yielding unsupported, the status quo).
const (
	// entityResolveMinJaccard is the token-overlap floor for accepting a fuzzy
	// match. High on purpose: "Hypothyroidism" vs "Autoimmune hypothyroidism" is
	// 1/2, which must NOT pass — those are different clinical entities.
	entityResolveMinJaccard = 0.8
	// entityResolveMinSeed is the shortest token worth seeding a CONTAINS scan
	// with; shorter seeds scan large unrelated swathes of the graph.
	entityResolveMinSeed = 4
	// entityResolveScanLimit bounds the candidate scan per lookup.
	entityResolveScanLimit = 200
	// entityResolveCacheMax bounds the per-reasoner memo.
	entityResolveCacheMax = 4096
)

// resolveEntity maps a caller-supplied entity name to the exact name stored in
// the graph, or returns it unchanged when nothing clears the bar.
func (n *NativeReasoner) resolveEntity(ctx context.Context, name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || n.g == nil {
		return name
	}
	key := strings.ToLower(trimmed)
	if v, ok := n.entityCache.Load(key); ok {
		s, _ := v.(string)
		return s
	}

	// Candidates in decreasing specificity. A parenthetical suffix is editorial
	// ("Single Umbilical Artery (SUA)"), so the stripped form is worth trying —
	// but only AFTER the full string, never instead of it.
	cands := []string{trimmed}
	if i := strings.IndexByte(trimmed, '('); i > 0 {
		if base := strings.TrimSpace(trimmed[:i]); base != "" && base != trimmed {
			cands = append(cands, base)
		}
	}

	resolved := name
	if hit := n.exactEntity(ctx, cands); hit != "" {
		resolved = hit
	} else if hit := n.fuzzyEntity(ctx, cands[len(cands)-1]); hit != "" {
		resolved = hit
	}

	n.storeEntity(key, resolved)
	return resolved
}

func (n *NativeReasoner) storeEntity(key, resolved string) {
	if n.entityCount.Load() >= entityResolveCacheMax {
		n.entityCache.Range(func(k, _ any) bool { n.entityCache.Delete(k); return true })
		n.entityCount.Store(0)
	}
	if _, loaded := n.entityCache.LoadOrStore(key, resolved); !loaded {
		n.entityCount.Add(1)
	}
}

// exactEntity matches case-insensitively on the whole name. Graph entities are
// inconsistently cased, so this alone recovers a large share of lookups.
func (n *NativeReasoner) exactEntity(ctx context.Context, cands []string) string {
	for _, c := range cands {
		rows, err := n.g.Query(ctx,
			"MATCH (e:Entity) WHERE lower(e.name) = $n RETURN e.name AS name LIMIT 1",
			map[string]any{"n": strings.ToLower(c)})
		if err != nil || len(rows) == 0 {
			continue
		}
		if s := asString(rows[0]["name"]); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// fuzzyEntity accepts the best normalized-token match above a high threshold.
// Seeded by the longest content token so the scan stays bounded.
func (n *NativeReasoner) fuzzyEntity(ctx context.Context, name string) string {
	want := normalizeEntityTokens(name)
	if len(want) == 0 {
		return ""
	}
	seed := ""
	for _, t := range want {
		if len(t) > len(seed) {
			seed = t
		}
	}
	if len(seed) < entityResolveMinSeed {
		return ""
	}
	rows, err := n.g.Query(ctx,
		"MATCH (e:Entity) WHERE lower(e.name) CONTAINS $s RETURN e.name AS name LIMIT "+itoa(entityResolveScanLimit),
		map[string]any{"s": seed})
	if err != nil {
		return ""
	}

	wantSet := make(map[string]bool, len(want))
	for _, t := range want {
		wantSet[t] = true
	}
	best, bestScore := "", 0.0
	for _, r := range rows {
		cand := asString(r["name"])
		got := normalizeEntityTokens(cand)
		if len(got) == 0 {
			continue
		}
		inter := 0
		gotSet := make(map[string]bool, len(got))
		for _, t := range got {
			gotSet[t] = true
		}
		for t := range wantSet {
			if gotSet[t] {
				inter++
			}
		}
		union := len(wantSet) + len(gotSet) - inter
		if union == 0 {
			continue
		}
		score := float64(inter) / float64(union)
		// Ties go to the shorter name: it is the less qualified, more general
		// entity, and over-specifying ("Autoimmune hypothyroidism" for
		// "Hypothyroidism") asserts something the caller did not claim.
		if score > bestScore || (score == bestScore && best != "" && len(cand) < len(best)) {
			best, bestScore = cand, score
		}
	}
	if bestScore < entityResolveMinJaccard {
		return ""
	}
	return best
}

// normalizeEntityTokens lowercases and splits on non-alphanumerics, dropping
// single characters so punctuation and initials do not dominate the overlap.
func normalizeEntityTokens(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 1 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
