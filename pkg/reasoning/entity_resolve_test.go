package reasoning

import (
	"context"
	"strings"
	"testing"
)

// graphWithEntities answers the two resolution queries over a fixed entity list.
func graphWithEntities(names ...string) mockGraph {
	return mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		switch {
		case strings.Contains(q, "lower(e.name) = $n"):
			want, _ := params["n"].(string)
			for _, n := range names {
				if strings.ToLower(n) == want {
					return []map[string]any{{"name": n}}, nil
				}
			}
			return nil, nil
		case strings.Contains(q, "CONTAINS $s"):
			seed, _ := params["s"].(string)
			var rows []map[string]any
			for _, n := range names {
				if strings.Contains(strings.ToLower(n), seed) {
					rows = append(rows, map[string]any{"name": n})
				}
			}
			return rows, nil
		}
		return nil, nil
	}}
}

// Graph entities are inconsistently cased; callers send the clinical form. Exact
// case-insensitive matching recovers these.
func TestResolveEntityMatchesCaseInsensitively(t *testing.T) {
	n := NewNativeReasoner(graphWithEntities("acromegaly", "Dent Disease"), nil, Config{})
	if got := n.resolveEntity(context.Background(), "Acromegaly"); got != "acromegaly" {
		t.Fatalf("want the stored spelling %q, got %q", "acromegaly", got)
	}
	if got := n.resolveEntity(context.Background(), "dent disease"); got != "Dent Disease" {
		t.Fatalf("want %q, got %q", "Dent Disease", got)
	}
}

// A parenthetical suffix is editorial, so the stripped form is tried — but only
// after the full string.
func TestResolveEntityStripsParentheticalSuffix(t *testing.T) {
	n := NewNativeReasoner(graphWithEntities("Single Umbilical Artery"), nil, Config{})
	got := n.resolveEntity(context.Background(), "Single Umbilical Artery (SUA)")
	if got != "Single Umbilical Artery" {
		t.Fatalf("want the stored name, got %q", got)
	}
}

// THE CRITICAL NEGATIVE CASE. "Hypothyroidism" and "Autoimmune hypothyroidism"
// are DIFFERENT clinical entities. Resolving one to the other would let the
// reasoner assert a verified claim about a condition the patient was never said
// to have. Token overlap is 1/2, below the 0.8 floor, so it must not resolve.
func TestResolveEntityRejectsMoreSpecificCondition(t *testing.T) {
	n := NewNativeReasoner(graphWithEntities("Autoimmune hypothyroidism", "hypothyroidism, congenital, nongoitrous"), nil, Config{})
	got := n.resolveEntity(context.Background(), "Hypothyroidism")
	if got != "Hypothyroidism" {
		t.Fatalf("must NOT resolve to a more specific condition; got %q", got)
	}
}

// An unknown entity passes through unchanged, which yields unsupported — the
// status quo, and the safe failure.
func TestResolveEntityPassesThroughUnknown(t *testing.T) {
	n := NewNativeReasoner(graphWithEntities("Dent Disease"), nil, Config{})
	if got := n.resolveEntity(context.Background(), "Completely Unrelated Thing"); got != "Completely Unrelated Thing" {
		t.Fatalf("unknown name must pass through unchanged, got %q", got)
	}
}

// Resolution costs graph queries and the same names recur across every claim in
// a differential, so results are memoized.
func TestResolveEntityMemoizes(t *testing.T) {
	var calls int
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "lower(e.name) = $n") {
			calls++
			return []map[string]any{{"name": "acromegaly"}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, nil, Config{})
	for i := 0; i < 5; i++ {
		if got := n.resolveEntity(context.Background(), "Acromegaly"); got != "acromegaly" {
			t.Fatalf("got %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("want 1 lookup for 5 identical resolutions, got %d", calls)
	}
}

// Ties must prefer the SHORTER name: it is the less qualified entity, and
// over-specifying asserts something the caller did not claim.
func TestNormalizeEntityTokensDropsPunctuationAndInitials(t *testing.T) {
	got := normalizeEntityTokens("hypothyroidism, congenital (a)")
	want := []string{"hypothyroidism", "congenital"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// DO NOT "fix" this by adding character-level fuzzy matching.
//
// A genuine typo is edit distance 1 ("hypothyrodism"). Clinically OPPOSITE
// conditions are edit distance 2 and 80-87% similar:
//
//	hypothyroidism / hyperthyroidism   edit 2, 87% similar
//	hypoglycemia   / hyperglycemia     edit 2, 85% similar
//	hypotension    / hypertension      edit 2, 83% similar
//	hypokalemia    / hyperkalemia      edit 2, 83% similar
//
// Any threshold loose enough to correct a typo is one step from silently
// rewriting hypoglycemia to hyperglycemia and returning it as logically
// verified, with a proof, about a patient. The deployed graphs are full of
// hypo-/hyper- entities, so these collisions are real.
//
// Resolution is token-based precisely so this cannot happen: hypo- and hyper-
// forms are different TOKENS, scoring far below the acceptance floor. Misspelled
// input resolves to nothing and the claim returns unsupported — the safe
// failure. Spelling correction belongs upstream, where there is surrounding
// clinical context to disambiguate with, or in a curated vocabulary's synonym
// set — not in a blind string match at the point of asserting proof.
func TestResolveEntityNeverConfusesClinicalOpposites(t *testing.T) {
	stored := []string{
		"hyperthyroidism", "hyperglycemia", "hypertension", "hyperkalemia", "hypertonia",
	}
	n := NewNativeReasoner(graphWithEntities(stored...), nil, Config{})

	for _, asked := range []string{
		"hypothyroidism", "hypoglycemia", "hypotension", "hypokalemia", "hypotonia",
	} {
		got := n.resolveEntity(context.Background(), asked)
		if got != asked {
			t.Fatalf("%q resolved to %q — a clinically OPPOSITE condition; resolution must never do this", asked, got)
		}
	}
}

// A misspelling resolves to nothing rather than to a near neighbour. This is a
// deliberate recall loss: see TestResolveEntityNeverConfusesClinicalOpposites.
func TestResolveEntityLeavesMisspellingsUnresolved(t *testing.T) {
	n := NewNativeReasoner(graphWithEntities("hypothyroidism", "Dementia"), nil, Config{})
	for _, typo := range []string{"hypothyrodism", "demntia"} {
		if got := n.resolveEntity(context.Background(), typo); got != typo {
			t.Fatalf("misspelling %q resolved to %q; it must fail closed", typo, got)
		}
	}
}
