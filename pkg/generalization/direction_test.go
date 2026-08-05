package generalization

import (
	"testing"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

// fakeDirections orients only the pairs it is told about, so a test can assert
// exactly which edges survive and which are dropped.
type fakeDirections struct {
	// parentOf maps "child|parent" (order-insensitive, lowercased) to the parent.
	parents map[string]string
}

func newFakeDirections(pairs map[[2]string]string) *fakeDirections {
	f := &fakeDirections{parents: map[string]string{}}
	for k, parent := range pairs {
		f.parents[fakeKey(k[0], k[1])] = parent
	}
	return f
}

func fakeKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return collapseSpace(a) + "|" + collapseSpace(b)
}

func (f *fakeDirections) Orient(a, b string) (parent, child string, ok bool) {
	p, ok := f.parents[fakeKey(a, b)]
	if !ok {
		return "", "", false
	}
	if namesEqual(p, a) {
		return a, b, true
	}
	return b, a, true
}

func row(child, parent string) taxonomyRow {
	return taxonomyRow{
		child:     EntityRef{ID: "id-" + child, Name: child},
		parent:    EntityRef{ID: "id-" + parent, Name: parent},
		predicate: "is_a",
		depth:     1,
	}
}

func TestApplyDirectionNilSourceIsPassThrough(t *testing.T) {
	in := []taxonomyRow{row("a", "b")}
	got, dropped, flipped := applyDirection(nil, in)
	if len(got) != 1 || dropped != 0 || flipped != 0 {
		t.Fatalf("got %d rows, dropped=%d flipped=%d; want passthrough", len(got), dropped, flipped)
	}
}

// The central requirement: an edge no signal can orient is dropped, not passed
// through in whatever orientation the graph happened to store.
func TestApplyDirectionDropsUnorientableEdges(t *testing.T) {
	src := newFakeDirections(map[[2]string]string{
		{"Gordon syndrome", "connective tissue disorder"}: "connective tissue disorder",
	})
	in := []taxonomyRow{
		row("Gordon syndrome", "connective tissue disorder"), // orientable
		row("alpha", "beta"),  // not
		row("gamma", "delta"), // not
	}
	got, dropped, flipped := applyDirection(src, in)
	if len(got) != 1 {
		t.Fatalf("kept %d rows, want 1", len(got))
	}
	if dropped != 2 {
		t.Errorf("dropped=%d, want 2", dropped)
	}
	if flipped != 0 {
		t.Errorf("flipped=%d, want 0: this row was already stored correctly", flipped)
	}
}

// The measured defect: the graph stores "AMP-thymidine kinase activity
// IS_PARENT_OF kinase activity", which is inverted. The row must come back with
// its endpoints swapped, not merely be dropped.
func TestApplyDirectionFlipsBackwardsEdge(t *testing.T) {
	src := newFakeDirections(map[[2]string]string{
		{"AMP-thymidine kinase activity", "kinase activity"}: "kinase activity",
	})
	// Stored backwards: the query labelled "kinase activity" as the child.
	in := []taxonomyRow{row("kinase activity", "AMP-thymidine kinase activity")}

	got, dropped, flipped := applyDirection(src, in)
	if len(got) != 1 || dropped != 0 {
		t.Fatalf("kept %d dropped %d, want 1/0", len(got), dropped)
	}
	if flipped != 1 {
		t.Errorf("flipped=%d, want 1", flipped)
	}
	if got[0].parent.Name != "kinase activity" {
		t.Errorf("parent=%q, want %q", got[0].parent.Name, "kinase activity")
	}
	if got[0].child.Name != "AMP-thymidine kinase activity" {
		t.Errorf("child=%q, want %q", got[0].child.Name, "AMP-thymidine kinase activity")
	}
	// The IDs must travel WITH their names: the flip swaps whole endpoints, so
	// each ID stays paired with the name it identifies. Asserting the opposite
	// would be asserting that the flip corrupts identity.
	if got[0].parent.ID != "id-kinase activity" {
		t.Errorf("parent ID = %q, want the ID paired with the parent's name", got[0].parent.ID)
	}
	if got[0].child.ID != "id-AMP-thymidine kinase activity" {
		t.Errorf("child ID = %q, want the ID paired with the child's name", got[0].child.ID)
	}
}

func TestApplyDirectionDropsBlankEndpoints(t *testing.T) {
	src := newFakeDirections(map[[2]string]string{{"a", "b"}: "b"})
	in := []taxonomyRow{
		{child: EntityRef{Name: ""}, parent: EntityRef{Name: "b"}},
		{child: EntityRef{Name: "a"}, parent: EntityRef{Name: "  "}},
	}
	got, dropped, _ := applyDirection(src, in)
	if len(got) != 0 || dropped != 2 {
		t.Errorf("kept=%d dropped=%d, want 0/2", len(got), dropped)
	}
}

// The resolver normalises names; the flip check must normalise identically, or a
// resolver answer differing only in case or spacing is read as "not the child"
// and the row is flipped the wrong way.
func TestApplyDirectionMatchesResolverNormalization(t *testing.T) {
	src := &fakeDirections{parents: map[string]string{
		fakeKey("Kinase  Activity", "protein kinase activity"): "KINASE ACTIVITY",
	}}
	in := []taxonomyRow{row("Kinase  Activity", "protein kinase activity")}

	got, dropped, flipped := applyDirection(src, in)
	if len(got) != 1 || dropped != 0 {
		t.Fatalf("kept %d dropped %d, want 1/0", len(got), dropped)
	}
	if flipped != 1 {
		t.Fatalf("flipped=%d, want 1: the resolver named the stored child as parent", flipped)
	}
	if !namesEqual(got[0].parent.Name, "Kinase Activity") {
		t.Errorf("parent=%q, want the resolver's choice", got[0].parent.Name)
	}
}

func medReg(t *testing.T) *reasoning.PredicateRegistry {
	t.Helper()
	return reasoning.DefaultMedicalRegistry()
}

func clinicalRow(src, srcTypes, pred, tgt, tgtTypes string) directRow {
	return directRow{
		source:    EntityRef{ID: src, Name: src, Labels: []string{srcTypes}},
		target:    EntityRef{ID: tgt, Name: tgt, Labels: []string{tgtTypes}},
		predicate: pred,
	}
}

// The measured defect: SYMPTOM -has_phenotype-> DISEASE is backwards. Read-time
// repair can only veto it; normalising at build time is what makes the fact
// findable in the orientation the reasoner queries.
func TestNormalizeRelationDirectionFlipsBackwardsClinicalEdge(t *testing.T) {
	row, flipped := normalizeRelationDirection(medReg(t),
		clinicalRow("Abnormality of the elbow", "SYMPTOM", "has_phenotype", "Pelviscapular dysplasia", "DISEASE"))
	if !flipped {
		t.Fatal("expected a backwards SYMPTOM->DISEASE has_phenotype edge to be flipped")
	}
	if row.source.Name != "Pelviscapular dysplasia" || row.target.Name != "Abnormality of the elbow" {
		t.Errorf("endpoints not swapped: %s -> %s", row.source.Name, row.target.Name)
	}
	if row.predicate != "has_phenotype" {
		t.Errorf("predicate = %q, want the canonical spelling retained", row.predicate)
	}
}

func TestNormalizeRelationDirectionLeavesCorrectEdgeAlone(t *testing.T) {
	_, flipped := normalizeRelationDirection(medReg(t),
		clinicalRow("Pelviscapular dysplasia", "DISEASE", "has_phenotype", "Abnormality of the elbow", "SYMPTOM"))
	if flipped {
		t.Error("a correctly oriented edge must not be flipped")
	}
}

// Fail closed: absent or ambiguous types must not produce a guess, since a
// wrongly flipped clinical edge asserts something the graph does not contain.
func TestNormalizeRelationDirectionFailsClosed(t *testing.T) {
	reg := medReg(t)
	cases := map[string]directRow{
		"no source types": {source: EntityRef{Name: "a"}, target: EntityRef{Name: "b", Labels: []string{"DISEASE"}}, predicate: "has_phenotype"},
		"no target types": {source: EntityRef{Name: "a", Labels: []string{"SYMPTOM"}}, target: EntityRef{Name: "b"}, predicate: "has_phenotype"},
		"same type both":  clinicalRow("a", "DISEASE", "has_phenotype", "b", "DISEASE"),
		"unknown pred":    clinicalRow("a", "SYMPTOM", "expresses", "b", "DISEASE"),
	}
	for name, row := range cases {
		if _, flipped := normalizeRelationDirection(reg, row); flipped {
			t.Errorf("%s: flipped, want left alone", name)
		}
	}
	if _, flipped := normalizeRelationDirection(nil, clinicalRow("a", "SYMPTOM", "has_phenotype", "b", "DISEASE")); flipped {
		t.Error("nil registry: flipped, want left alone")
	}
}

func TestApplyRelationDirectionCountsFlips(t *testing.T) {
	rows := []directRow{
		clinicalRow("s1", "SYMPTOM", "has_phenotype", "d1", "DISEASE"), // backwards
		clinicalRow("d2", "DISEASE", "has_phenotype", "s2", "SYMPTOM"), // correct
		clinicalRow("s3", "SYMPTOM", "has_phenotype", "d3", "DISEASE"), // backwards
	}
	out, n := applyRelationDirection(medReg(t), rows)
	if n != 2 {
		t.Fatalf("flipped=%d, want 2", n)
	}
	if out[0].source.Name != "d1" || out[2].source.Name != "d3" {
		t.Errorf("wrong rows flipped: %+v", out)
	}
	if out[1].source.Name != "d2" {
		t.Error("the already-correct row was modified")
	}
}
