package taxonomy

import (
	"strings"
	"testing"
)

// fixtureOBO is a miniature ontology exercising a 3-level is_a chain, an
// unrelated branch, an EXACT synonym, a NARROW synonym that must be ignored, and
// an obsolete term that must not be indexed.
const fixtureOBO = `format-version: 1.2

[Term]
id: GO:0000001
name: kinase activity

[Term]
id: GO:0000002
name: protein kinase activity
synonym: "protein phosphokinase activity" EXACT []
is_a: GO:0000001 ! kinase activity

[Term]
id: GO:0000003
name: AMP-thymidine kinase activity
synonym: "AMP kinase" NARROW []
is_a: GO:0000002 ! protein kinase activity

[Term]
id: GO:0000004
name: transaminase activity

[Term]
id: GO:0000009
name: kinase activity
is_obsolete: true

[Typedef]
id: part_of
name: part of
`

func loadFixture(t *testing.T) *Ontology {
	t.Helper()
	o := NewOntology()
	if err := o.LoadOBO(strings.NewReader(fixtureOBO)); err != nil {
		t.Fatalf("LoadOBO: %v", err)
	}
	return o
}

func TestOntologyOrientsChainInBothArgumentOrders(t *testing.T) {
	d := NewDeriver(loadFixture(t))

	// Direct parent, and a transitive grandparent, in both argument orders.
	cases := []struct{ a, b, wantParent string }{
		{"kinase activity", "protein kinase activity", "kinase activity"},
		{"protein kinase activity", "kinase activity", "kinase activity"},
		{"kinase activity", "AMP-thymidine kinase activity", "kinase activity"},
		{"AMP-thymidine kinase activity", "kinase activity", "kinase activity"},
	}
	for _, c := range cases {
		got := d.Derive(Pair{A: c.a, B: c.b, AToB: true})
		if got.Tier != TierOriented || got.Signal != SignalOntology {
			t.Errorf("Derive(%q,%q) tier=%s signal=%s, want oriented/ontology", c.a, c.b, got.Tier, got.Signal)
			continue
		}
		if got.Parent != c.wantParent {
			t.Errorf("Derive(%q,%q) parent=%q, want %q", c.a, c.b, got.Parent, c.wantParent)
		}
	}
}

// The ontology's answer must beat the graph's stored orientation. This is the
// real measured defect: the deployed graph stores
// "AMP-thymidine kinase activity IS_PARENT_OF kinase activity", which is
// inverted, and lifting it as stored walks the hierarchy backwards.
func TestOntologyOverridesBackwardsStoredOrientation(t *testing.T) {
	d := NewDeriver(loadFixture(t))

	// Stored ONLY as child -> parent.
	got := d.Derive(Pair{A: "AMP-thymidine kinase activity", B: "kinase activity", AToB: true})
	if got.Parent != "kinase activity" {
		t.Fatalf("parent=%q, want %q — stored orientation must not win", got.Parent, "kinase activity")
	}
	if !got.ContradictsStored {
		t.Error("ContradictsStored=false, want true: the graph stores only the backwards edge")
	}
}

func TestBothOrientationsStoredCollapseToOne(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	got := d.Derive(Pair{A: "protein kinase activity", B: "kinase activity", AToB: true, BToA: true})
	if got.Tier != TierOriented || got.Parent != "kinase activity" {
		t.Fatalf("tier=%s parent=%q, want oriented/%q", got.Tier, got.Parent, "kinase activity")
	}
	// Both directions present means neither is "the" stored one, so nothing is
	// contradicted — there is simply no stored signal to contradict.
	if got.ContradictsStored {
		t.Error("ContradictsStored=true, want false when both orientations are stored")
	}
}

// Both endpoints are known terms but the ontology places them on separate
// branches: it positively asserts they are NOT in a hierarchy relation.
func TestOntologyKnownButUnrelatedIsUndetermined(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	got := d.Derive(Pair{A: "kinase activity", B: "transaminase activity", AToB: true})
	if got.Tier != TierUndetermined {
		t.Errorf("tier=%s, want undetermined for unrelated known terms", got.Tier)
	}
}

// The core requirement: a unidirectional stored edge is NOT evidence. Measured
// at 51.8% correct on the deployed corpus, which is a coin flip.
func TestStoredDirectionAloneNeverOrients(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	got := d.Derive(Pair{A: "hereditary connective tissue disorder", B: "Gordon syndrome", AToB: true})
	if got.Tier != TierUndetermined {
		t.Fatalf("tier=%s, want undetermined: unidirectional storage is not evidence", got.Tier)
	}
	if got.Parent != "" {
		t.Errorf("parent=%q, want empty", got.Parent)
	}
}

func TestUnmatchedNameDoesNotFuzzyMatch(t *testing.T) {
	o := loadFixture(t)
	if id, ok := o.lookup("kinase activityyy"); ok {
		t.Errorf("lookup matched %q to %q; ontology matching must be exact", "kinase activityyy", id)
	}
	if _, ok := o.lookup("protein phosphokinase activity"); !ok {
		t.Error("EXACT synonym should resolve")
	}
	if _, ok := o.lookup("AMP kinase"); ok {
		t.Error("NARROW synonym must NOT be indexed: it names a different concept than the term")
	}
}

func TestObsoleteTermIsNotIndexed(t *testing.T) {
	o := loadFixture(t)
	id, ok := o.lookup("kinase activity")
	if !ok {
		t.Fatal("live term should resolve")
	}
	if id != "GO:0000001" {
		t.Errorf("resolved to %q; the obsolete GO:0000009 must not shadow the live term", id)
	}
}

func TestSelfLoopIsUndetermined(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	for _, p := range []Pair{
		{A: "kinase activity", B: "kinase activity", AToB: true},
		{A: "Kinase Activity", B: "kinase activity ", AToB: true},
	} {
		if got := d.Derive(p); got.Tier != TierUndetermined {
			t.Errorf("Derive(%q,%q) tier=%s, want undetermined", p.A, p.B, got.Tier)
		}
	}
}

// Substring containment fails on ontology naming because qualifiers are inserted
// mid-string; token-subset is what catches it. Measured: substring resolved 476
// bidirectional edges, token-subset 611.
func TestLexicalHandlesMidStringQualifier(t *testing.T) {
	const (
		parent = "regulation of T cell selection"
		child  = "regulation of positive thymic T cell selection"
	)
	if strings.Contains(child, parent) {
		t.Fatal("fixture invalid: this pair is supposed to defeat substring containment")
	}
	d := NewDeriver(nil)
	d.EnableLexical(true)

	got := d.Derive(Pair{A: child, B: parent, AToB: true})
	if got.Tier != TierOriented || got.Signal != SignalLexical {
		t.Fatalf("tier=%s signal=%s, want oriented/lexical", got.Tier, got.Signal)
	}
	if got.Parent != parent {
		t.Errorf("parent=%q, want %q", got.Parent, parent)
	}
}

// "Autoimmune hypothyroidism" IS a kind of "Hypothyroidism", so the lexical
// signal SHOULD orient this pair. This is deliberately distinct from entity
// RESOLUTION, where the same two names must never be conflated into one entity
// (see entityResolveMinJaccard in pkg/reasoning/entity_resolve.go). Subsumption
// and identity are different questions, and the same name pair answers them
// differently.
func TestLexicalOrientsSubsumptionWithoutConflatingIdentity(t *testing.T) {
	d := NewDeriver(nil)
	d.EnableLexical(true)
	got := d.Derive(Pair{A: "Autoimmune hypothyroidism", B: "Hypothyroidism", AToB: true})
	if got.Tier != TierOriented {
		t.Fatalf("tier=%s, want oriented", got.Tier)
	}
	if got.Parent != "Hypothyroidism" {
		t.Errorf("parent=%q, want %q", got.Parent, "Hypothyroidism")
	}
}

func TestLexicalSiblingsAndEqualTokensAreUndetermined(t *testing.T) {
	d := NewDeriver(nil)
	d.EnableLexical(true)
	cases := [][2]string{
		// Siblings: neither token set contains the other.
		{"regulation of positive thymic T cell selection", "regulation of T cell differentiation in thymus"},
		// Same tokens, different surface form.
		{"T cell selection", "T-cell selection"},
	}
	for _, c := range cases {
		if got := d.Derive(Pair{A: c[0], B: c[1], AToB: true}); got.Tier != TierUndetermined {
			t.Errorf("Derive(%q,%q) tier=%s parent=%q, want undetermined", c[0], c[1], got.Tier, got.Parent)
		}
	}
}

func TestLexicalDisabledByDefault(t *testing.T) {
	d := NewDeriver(nil)
	got := d.Derive(Pair{A: "protein kinase activity", B: "kinase activity", AToB: true})
	if got.Tier != TierUndetermined {
		t.Errorf("tier=%s, want undetermined: lexical must be off until its precision is measured", got.Tier)
	}
}

// Ontology outranks lexical, and the disagreement is counted rather than hidden.
func TestOntologyWinsOverLexicalAndDisagreementIsCounted(t *testing.T) {
	// "kinase activity" ⊂ "AMP-thymidine kinase activity" lexically, which agrees
	// with the ontology; to force a disagreement we need an ontology that
	// contradicts the token containment.
	const inverted = `[Term]
id: X:1
name: alpha beta

[Term]
id: X:2
name: beta
is_a: X:1 ! alpha beta
`
	o := NewOntology()
	if err := o.LoadOBO(strings.NewReader(inverted)); err != nil {
		t.Fatal(err)
	}
	d := NewDeriver(o)
	d.EnableLexical(true)

	// Lexical says "beta" (fewer tokens) is the parent; the ontology says
	// "alpha beta" is.
	got := d.Derive(Pair{A: "alpha beta", B: "beta", AToB: true})
	if got.Signal != SignalOntology {
		t.Fatalf("signal=%s, want ontology to win", got.Signal)
	}
	if got.Parent != "alpha beta" {
		t.Errorf("parent=%q, want %q", got.Parent, "alpha beta")
	}
	if !got.SignalsDisagreed {
		t.Error("SignalsDisagreed=false, want true")
	}
}

func TestReportAccountsForEveryPair(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	pairs := []Pair{
		{A: "kinase activity", B: "protein kinase activity", AToB: true},
		{A: "AMP-thymidine kinase activity", B: "kinase activity", AToB: true},
		{A: "kinase activity", B: "transaminase activity", AToB: true},
		{A: "wholly unknown alpha", B: "wholly unknown beta", AToB: true, BToA: true},
		{A: "kinase activity", B: "kinase activity", AToB: true},
	}
	_, rep := d.DeriveAll(pairs, 20)

	if rep.TotalPairs != len(pairs) {
		t.Errorf("TotalPairs=%d, want %d", rep.TotalPairs, len(pairs))
	}
	if rep.Oriented+rep.Undetermined != rep.TotalPairs {
		t.Errorf("oriented(%d)+undetermined(%d) != total(%d)", rep.Oriented, rep.Undetermined, rep.TotalPairs)
	}
	sum := 0
	for _, n := range rep.BySignal {
		sum += n
	}
	if sum != rep.Oriented {
		t.Errorf("BySignal sums to %d, want oriented=%d", sum, rep.Oriented)
	}
	if rep.ContradictsStored != 1 {
		t.Errorf("ContradictsStored=%d, want 1", rep.ContradictsStored)
	}
}

// An unmeasured signal must not report a precision figure, because the spec uses
// that figure to license enabling it.
func TestLexicalPrecisionRequiresEnoughSamples(t *testing.T) {
	r := &Report{LexicalAgree: 3, LexicalTotal: 3}
	if _, ok := r.LexicalPrecision(); ok {
		t.Error("LexicalPrecision reported ok on a 3-pair sample; want not-measured")
	}
	r = &Report{LexicalAgree: 45, LexicalTotal: 50}
	rate, ok := r.LexicalPrecision()
	if !ok {
		t.Fatal("LexicalPrecision not ok on a 50-pair sample")
	}
	if rate < 0.89 || rate > 0.91 {
		t.Errorf("rate=%v, want ~0.90", rate)
	}
}

func TestDeriveAllIsDeterministicUnderInputReordering(t *testing.T) {
	d := NewDeriver(loadFixture(t))
	d.EnableLexical(true)
	base := []Pair{
		{A: "kinase activity", B: "protein kinase activity", AToB: true},
		{A: "AMP-thymidine kinase activity", B: "kinase activity", AToB: true},
		{A: "zeta unknown", B: "alpha unknown", BToA: true},
		{A: "regulation of T cell selection", B: "regulation of positive thymic T cell selection", AToB: true},
		{A: "kinase activity", B: "transaminase activity", AToB: true},
	}
	shuffled := []Pair{base[3], base[0], base[4], base[1], base[2]}

	_, r1 := d.DeriveAll(base, 3)
	_, r2 := d.DeriveAll(shuffled, 3)

	if r1.Oriented != r2.Oriented || r1.Undetermined != r2.Undetermined {
		t.Fatalf("counts differ under reordering: %+v vs %+v", r1, r2)
	}
	if len(r1.Samples) != len(r2.Samples) {
		t.Fatalf("sample counts differ: %d vs %d", len(r1.Samples), len(r2.Samples))
	}
	for i := range r1.Samples {
		if r1.Samples[i] != r2.Samples[i] {
			t.Errorf("sample %d differs:\n %+v\n %+v", i, r1.Samples[i], r2.Samples[i])
		}
	}
}
