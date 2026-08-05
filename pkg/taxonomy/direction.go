package taxonomy

import (
	"sort"
	"strings"
)

// Tier is the confidence class of a derived direction. There are deliberately
// only two: a pair's direction is either determined by a signal, or it is not.
//
// There is NO tier for "stored unidirectionally, therefore probably right". An
// earlier design had one; it was refuted by measurement (51.8% correct, which is
// a coin flip) and the spec now forbids it.
type Tier string

const (
	// TierOriented means a signal determined which endpoint is the parent.
	TierOriented Tier = "oriented"
	// TierUndetermined means no signal could, so the pair is dropped.
	TierUndetermined Tier = "undetermined"
)

// Signal names the evidence that determined a direction.
type Signal string

const (
	// SignalOntology is an authoritative published DAG. Highest precedence.
	SignalOntology Signal = "ontology"
	// SignalLexical is token-subset containment: the more-qualified name is the
	// child. Secondary, and only enabled once its precision has been measured
	// against the ontology-adjudicable subset.
	SignalLexical Signal = "lexical"
	// SignalNone accompanies TierUndetermined.
	SignalNone Signal = "none"
)

// Direction is the derived orientation of one entity pair.
type Direction struct {
	Parent string
	Child  string
	Tier   Tier
	Signal Signal
	// ContradictsStored is true when the graph stores this pair ONLY in the
	// opposite orientation — i.e. lifting it as stored would have walked the
	// hierarchy backwards.
	ContradictsStored bool
	// SignalsDisagreed is true when ontology and lexical produced opposite
	// answers. Ontology wins, but the disagreement is counted: a rising
	// disagreement rate means the lexical signal is drifting out of trust.
	SignalsDisagreed bool
}

// Deriver derives hierarchy direction for entity pairs.
//
// The zero value is not usable; construct with NewDeriver.
type Deriver struct {
	ont *Ontology
	// lexical enables the token-subset signal. Off by default: the spec requires
	// a signal's precision be measured against the ontology-adjudicable subset
	// before it is trusted, and an unmeasured signal enabled by default is
	// exactly how an unmeasurable error rate gets into a clinical corpus.
	lexical bool
}

// NewDeriver returns a Deriver using ont as its authoritative source. ont may be
// nil, in which case only explicitly enabled secondary signals apply.
func NewDeriver(ont *Ontology) *Deriver {
	return &Deriver{ont: ont}
}

// EnableLexical turns on the token-subset signal. Callers MUST have measured its
// precision (see Report.LexicalPrecision) before enabling it for a production
// derivation.
func (d *Deriver) EnableLexical(on bool) { d.lexical = on }

// Pair is an unordered entity pair as stored in the graph, carrying which
// orientations were actually present.
type Pair struct {
	A, B string
	// AToB is true when the graph stores an edge A -> B; BToA likewise. Both may
	// be true — 72.4% of measured pairs are stored in both directions — and that
	// carries no information either way.
	AToB, BToA bool
}

// Derive returns the direction of a single pair.
//
// Precedence is ontology, then lexical. On disagreement the ontology wins and
// the disagreement is recorded.
func (d *Deriver) Derive(p Pair) Direction {
	a, b := strings.TrimSpace(p.A), strings.TrimSpace(p.B)
	// A self-loop asserts a term is its own parent. Nothing can orient that.
	if a == "" || b == "" || NormalizeName(a) == NormalizeName(b) {
		return Direction{Tier: TierUndetermined, Signal: SignalNone}
	}

	ontParent, ontChild, ontOK := d.deriveOntology(a, b)
	lexParent, lexChild, lexOK := "", "", false
	if d.lexical {
		lexParent, lexChild, lexOK = deriveLexical(a, b)
	}

	var out Direction
	switch {
	case ontOK:
		out = Direction{Parent: ontParent, Child: ontChild, Tier: TierOriented, Signal: SignalOntology}
		out.SignalsDisagreed = lexOK && lexParent != ontParent
	case lexOK:
		out = Direction{Parent: lexParent, Child: lexChild, Tier: TierOriented, Signal: SignalLexical}
	default:
		return Direction{Tier: TierUndetermined, Signal: SignalNone}
	}

	out.ContradictsStored = storedOnlyBackwards(p, out.Parent)
	return out
}

// storedOnlyBackwards reports whether the graph holds this pair exclusively in
// the child->parent orientation.
func storedOnlyBackwards(p Pair, parent string) bool {
	forward, backward := p.AToB, p.BToA
	if parent == p.B {
		forward, backward = p.BToA, p.AToB
	}
	return backward && !forward
}

// deriveOntology reads direction off the ontology's is_a DAG. Both endpoints
// must match a term; a single-sided match tells us nothing about the pair.
func (d *Deriver) deriveOntology(a, b string) (parent, child string, ok bool) {
	if d.ont == nil {
		return "", "", false
	}
	ia, okA := d.ont.lookup(a)
	ib, okB := d.ont.lookup(b)
	if !okA || !okB || ia == ib {
		return "", "", false
	}
	switch {
	case d.ont.isAncestor(ib, ia):
		return b, a, true
	case d.ont.isAncestor(ia, ib):
		return a, b, true
	}
	// Both are known terms but neither subsumes the other: the ontology
	// positively asserts they are not in a hierarchy relation. Measured at 7 of
	// 264 adjudicable pairs (2.7%). Undetermined, and correctly so.
	return "", "", false
}

// deriveLexical implements token-subset containment: the more-qualified name is
// the child.
//
// Substring containment is NOT sufficient and must not be substituted. Ontology
// naming inserts qualifiers mid-string, so "regulation of positive thymic T cell
// selection" is a child of "regulation of T cell selection" while failing
// strings.Contains. Measured: substring resolved 476 bidirectional edges,
// token-subset resolved 611.
func deriveLexical(a, b string) (parent, child string, ok bool) {
	ta, tb := nameTokens(a), nameTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return "", "", false
	}
	switch {
	case properSubset(ta, tb):
		return a, b, true
	case properSubset(tb, ta):
		return b, a, true
	}
	// Equal token sets, or neither contained in the other (e.g. sibling terms).
	return "", "", false
}

// nameTokens lowercases and splits on non-alphanumerics, dropping
// single-character tokens so punctuation and initials do not dominate. This
// mirrors normalizeEntityTokens in pkg/reasoning/entity_resolve.go; the two must
// stay consistent, since divergent normalization between resolution and
// orientation would silently orient a different pair than the one resolved.
func nameTokens(s string) map[string]bool {
	out := make(map[string]bool)
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 1 {
			out[cur.String()] = true
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

// properSubset reports whether sub is a strict subset of super.
func properSubset(sub, super map[string]bool) bool {
	if len(sub) >= len(super) {
		return false
	}
	for t := range sub {
		if !super[t] {
			return false
		}
	}
	return true
}

// Report is the measurement instrument required by the spec: every derivation
// run must account for every pair and state the precision of any non-
// authoritative signal it used.
type Report struct {
	TotalPairs   int            `json:"total_pairs"`
	Oriented     int            `json:"oriented"`
	Undetermined int            `json:"undetermined"`
	BySignal     map[Signal]int `json:"by_signal"`
	// ContradictsStored counts pairs whose derived direction is the opposite of
	// the only orientation the graph stores. These are the edges that would have
	// been lifted backwards.
	ContradictsStored int `json:"contradicts_stored"`
	// SignalDisagreements counts pairs where lexical contradicted the ontology.
	SignalDisagreements int `json:"signal_disagreements"`

	// LexicalAgree/LexicalTotal measure the lexical signal against the
	// ontology-adjudicable subset — the only subset where an answer is known.
	LexicalAgree int `json:"lexical_agree"`
	LexicalTotal int `json:"lexical_total"`

	Ontologies []string `json:"ontologies"`
	Samples    []Sample `json:"samples"`
}

// Sample is one hand-checkable classified pair.
type Sample struct {
	A        string `json:"a"`
	B        string `json:"b"`
	Parent   string `json:"parent,omitempty"`
	Child    string `json:"child,omitempty"`
	Tier     Tier   `json:"tier"`
	Signal   Signal `json:"signal"`
	Stored   string `json:"stored"`
	Backward bool   `json:"contradicts_stored"`
}

// LexicalPrecision returns the lexical signal's agreement rate with the
// ontology on pairs both can adjudicate, and whether enough pairs existed to
// make the figure meaningful.
//
// A run that reports ok=false has NOT measured the signal, and per the spec the
// signal must then stay disabled rather than be assumed adequate.
func (r *Report) LexicalPrecision() (rate float64, ok bool) {
	const minSample = 30
	if r.LexicalTotal < minSample {
		return 0, false
	}
	return float64(r.LexicalAgree) / float64(r.LexicalTotal), true
}

// DeriveAll derives every pair and produces the run report.
//
// sampleN pairs per tier are retained for hand-checking. Sampling is
// deterministic — pairs are sorted before selection — so two runs over the same
// input produce byte-identical reports, as the spec requires.
func (d *Deriver) DeriveAll(pairs []Pair, sampleN int) (map[int]Direction, *Report) {
	rep := &Report{
		TotalPairs: len(pairs),
		BySignal:   map[Signal]int{},
	}
	if d.ont != nil {
		rep.Ontologies = d.ont.Sources()
	}

	out := make(map[int]Direction, len(pairs))
	perTier := map[Tier][]Sample{}

	order := make([]int, len(pairs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		px, py := pairs[order[x]], pairs[order[y]]
		if px.A != py.A {
			return px.A < py.A
		}
		return px.B < py.B
	})

	for _, i := range order {
		p := pairs[i]
		dir := d.Derive(p)
		out[i] = dir

		switch dir.Tier {
		case TierOriented:
			rep.Oriented++
			rep.BySignal[dir.Signal]++
			if dir.ContradictsStored {
				rep.ContradictsStored++
			}
			if dir.SignalsDisagreed {
				rep.SignalDisagreements++
			}
		default:
			rep.Undetermined++
		}

		// Measure lexical against the ontology wherever the ontology has an
		// answer, regardless of whether lexical is enabled for derivation — the
		// figure is what licenses enabling it.
		if ontParent, _, ontOK := d.deriveOntology(p.A, p.B); ontOK {
			if lexParent, _, lexOK := deriveLexical(p.A, p.B); lexOK {
				rep.LexicalTotal++
				if lexParent == ontParent {
					rep.LexicalAgree++
				}
			}
		}

		if len(perTier[dir.Tier]) < sampleN {
			perTier[dir.Tier] = append(perTier[dir.Tier], Sample{
				A: p.A, B: p.B,
				Parent: dir.Parent, Child: dir.Child,
				Tier: dir.Tier, Signal: dir.Signal,
				Stored:   storedDesc(p),
				Backward: dir.ContradictsStored,
			})
		}
	}

	for _, t := range []Tier{TierOriented, TierUndetermined} {
		rep.Samples = append(rep.Samples, perTier[t]...)
	}
	return out, rep
}

func storedDesc(p Pair) string {
	switch {
	case p.AToB && p.BToA:
		return "both"
	case p.AToB:
		return "a->b"
	case p.BToA:
		return "b->a"
	default:
		return "none"
	}
}
