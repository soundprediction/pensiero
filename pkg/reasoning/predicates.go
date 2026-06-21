package reasoning

import "strings"

// Characteristic is a GENERAL, domain-agnostic logical primitive a predicate
// satisfies — the OWL/relational property characteristics the reasoner composes
// over. Domains (medical, legal, …) declare which primitives each of their
// predicates has; the engine reasons purely in terms of these primitives plus the
// inverse / sub-property / composition / disjoint primitives below.
type Characteristic uint16

const (
	Transitive        Characteristic = 1 << iota // P(a,b)∧P(b,c) ⟹ P(a,c)
	Symmetric                                    // P(a,b) ⟹ P(b,a)
	Asymmetric                                   // P(a,b) ⟹ ¬P(b,a)
	Reflexive                                    // P(a,a)
	Irreflexive                                  // ¬P(a,a)
	Functional                                   // P(a,b)∧P(a,c) ⟹ b=c
	InverseFunctional                            // P(a,b)∧P(c,b) ⟹ a=c
)

// PredicateMeta declares a canonical predicate in terms of general primitives.
// Raw is the surface form (functor F maps Raw→Canonical); a canonical-only meta
// has Raw==Canonical.
type PredicateMeta struct {
	Raw           string
	Canonical     string
	InverseOf     string   // general inverse primitive:    P(a,b) ⟹ InverseOf(b,a)
	SubPropertyOf []string // general hierarchy primitive:  P ⊑ Q ⟹ (P(a,b) ⟹ Q(a,b))
	Chars         Characteristic
}

// Has reports whether the predicate carries a general characteristic.
func (m PredicateMeta) Has(c Characteristic) bool { return m.Chars&c != 0 }

// CompositionRule is the general role-composition primitive: First∘Second ⊑ Result
// — First(a,b) ∧ Second(b,c) ⟹ Result(a,c). Transitivity is the special case
// First==Second==Result; sub-property is the degenerate single-step case.
type CompositionRule struct{ First, Second, Result string }

// DisjointPair is the general disjoint-property primitive: A and B cannot both
// relate the same ordered pair (drives contradiction detection alongside
// ontology-class disjointness).
type DisjointPair struct{ A, B string }

// PredicateRegistry is the general predicate-primitive store: canonicalization
// (functor F), per-predicate characteristics + inverses + sub-property edges, and
// the global composition rules and disjoint pairs.
type PredicateRegistry struct {
	byRaw    map[string]PredicateMeta
	byCanon  map[string]PredicateMeta
	comps    []CompositionRule
	disjoint []DisjointPair
}

// NewPredicateRegistry builds a registry from predicate metas, composition rules,
// and disjoint pairs. Each meta is keyed by both its raw and canonical forms so an
// already-canonical input normalizes to itself.
func NewPredicateRegistry(metas []PredicateMeta, comps []CompositionRule, disjoint []DisjointPair) *PredicateRegistry {
	r := &PredicateRegistry{
		byRaw:    map[string]PredicateMeta{},
		byCanon:  map[string]PredicateMeta{},
		comps:    comps,
		disjoint: disjoint,
	}
	for _, m := range metas {
		if m.Canonical == "" {
			m.Canonical = m.Raw
		}
		r.byRaw[normKey(m.Raw)] = m
		r.byRaw[normKey(m.Canonical)] = m
		r.byCanon[normKey(m.Canonical)] = m
	}
	return r
}

func normKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Canonical applies the normalization functor F. For an unregistered predicate it
// returns the identity (canonical==input, no characteristics), ok=false.
func (r *PredicateRegistry) Canonical(raw string) (PredicateMeta, bool) {
	if r != nil {
		if m, ok := r.byRaw[normKey(raw)]; ok {
			return m, true
		}
	}
	return PredicateMeta{Raw: raw, Canonical: strings.TrimSpace(raw)}, false
}

// Characteristics returns the general characteristics of a canonical predicate.
func (r *PredicateRegistry) Characteristics(canon string) Characteristic {
	if r == nil {
		return 0
	}
	return r.byCanon[normKey(canon)].Chars
}

// IsTransitive / IsSymmetric / IsFunctional test individual general primitives.
func (r *PredicateRegistry) IsTransitive(canon string) bool { return r.has(canon, Transitive) }
func (r *PredicateRegistry) IsSymmetric(canon string) bool  { return r.has(canon, Symmetric) }
func (r *PredicateRegistry) IsFunctional(canon string) bool { return r.has(canon, Functional) }

func (r *PredicateRegistry) has(canon string, c Characteristic) bool {
	if r == nil {
		return false
	}
	m, ok := r.byCanon[normKey(canon)]
	return ok && m.Has(c)
}

// Conflicting returns the predicates declared disjoint with canon: a predicate B
// such that an asserted B(a,b) is logically inconsistent with a claimed canon(a,b).
// Symmetric over the registered DisjointPairs. Drives contradiction detection.
func (r *PredicateRegistry) Conflicting(canon string) []string {
	if r == nil {
		return nil
	}
	c := normKey(canon)
	var out []string
	for _, d := range r.disjoint {
		switch {
		case normKey(d.A) == c:
			out = append(out, d.B)
		case normKey(d.B) == c:
			out = append(out, d.A)
		}
	}
	return out
}

// Inverse returns the canonical inverse predicate, if declared.
func (r *PredicateRegistry) Inverse(canon string) (string, bool) {
	if r == nil {
		return "", false
	}
	if m, ok := r.byCanon[normKey(canon)]; ok && m.InverseOf != "" {
		return m.InverseOf, true
	}
	return "", false
}

// SubPropertiesOf returns the predicates P subsumes (Q such that P ⊑ Q).
func (r *PredicateRegistry) SuperPropertiesOf(canon string) []string {
	if r == nil {
		return nil
	}
	return r.byCanon[normKey(canon)].SubPropertyOf
}

// Compositions returns the global composition rules (First∘Second ⊑ Result).
func (r *PredicateRegistry) Compositions() []CompositionRule {
	if r == nil {
		return nil
	}
	return r.comps
}

// DisjointWith reports whether predicates a and b are declared disjoint.
func (r *PredicateRegistry) DisjointWith(a, b string) bool {
	if r == nil {
		return false
	}
	a, b = normKey(a), normKey(b)
	for _, d := range r.disjoint {
		if (normKey(d.A) == a && normKey(d.B) == b) || (normKey(d.A) == b && normKey(d.B) == a) {
			return true
		}
	}
	return false
}

// TransitivePreds returns every canonical predicate carrying the Transitive
// primitive — the default closure/subsumption filter.
func (r *PredicateRegistry) TransitivePreds() []string {
	var out []string
	if r == nil {
		return out
	}
	for c, m := range r.byCanon {
		if m.Has(Transitive) {
			out = append(out, c)
		}
	}
	return out
}
