package generalization

import "testing"

// Source entity types must survive into the derived graph. Emitting only the
// NodeKind labelled every entity "scope", which silently disabled the
// type-based direction repair in pkg/reasoning/orientation.go and left the
// ~42% of inverted has_phenotype edges uncorrectable at query time — making a
// derived graph strictly LESS usable than the source it came from.
func TestEmitLabelsPreservesSourceTypes(t *testing.T) {
	got := emitLabels(Node{Labels: []string{"DISEASE", "SYMPTOM"}, Kind: NodeScope})
	want := []string{"DISEASE", "SYMPTOM", "scope"}
	if len(got) != len(want) {
		t.Fatalf("emitLabels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emitLabels = %v, want %v", got, want)
		}
	}
}

func TestEmitLabelsHandlesMissingAndDuplicateTypes(t *testing.T) {
	// No source types: the kind alone is still emitted, so behaviour on a graph
	// without labels is unchanged.
	if got := emitLabels(Node{Kind: NodeConcept}); len(got) != 1 || got[0] != "concept" {
		t.Errorf("emitLabels with no source labels = %v, want [concept]", got)
	}
	// A source label equal to the kind must not be duplicated.
	if got := emitLabels(Node{Labels: []string{"scope"}, Kind: NodeScope}); len(got) != 1 {
		t.Errorf("emitLabels = %v, want no duplicate", got)
	}
	// Empty strings are dropped rather than emitted as blank labels.
	if got := emitLabels(Node{Labels: []string{"", "DRUG"}, Kind: NodeEndpoint}); len(got) != 2 {
		t.Errorf("emitLabels = %v, want [DRUG endpoint]", got)
	}
}
