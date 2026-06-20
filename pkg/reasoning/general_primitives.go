package reasoning

// General (domain-agnostic) predicate primitives. These are reusable across ANY
// domain and exist IN ADDITION TO a domain's own predicates: a domain registry
// (e.g. DefaultMedicalRegistry) is built by extending this general base, so the
// abstract relations (is_a, part_of, same_as, related_to, …) and their general
// composition rules are always available and a domain only declares its specific
// predicates on top.

// generalPredicates are the universal logical relations, declared via the general
// characteristics in predicates.go.
var generalPredicates = []PredicateMeta{
	// taxonomy / subsumption
	{Canonical: "is_a", Chars: Transitive, InverseOf: "subsumes"},
	{Canonical: "subsumes", Chars: Transitive, InverseOf: "is_a"},
	// instantiation (individual -> class)
	{Canonical: "instance_of", InverseOf: "has_instance", SubPropertyOf: []string{"related_to"}},
	{Canonical: "has_instance", InverseOf: "instance_of"},
	// mereology
	{Canonical: "part_of", Chars: Transitive, InverseOf: "has_part"},
	{Canonical: "has_part", Chars: Transitive, InverseOf: "part_of"},
	{Canonical: "located_in", Chars: Transitive, InverseOf: "location_of"},
	{Canonical: "location_of", Chars: Transitive, InverseOf: "located_in"},
	// identity / equivalence (full equivalence relation)
	{Canonical: "same_as", Chars: Symmetric | Transitive | Reflexive},
	{Canonical: "equivalent_to", Chars: Symmetric | Transitive | Reflexive},
	// generic association / dependence / order
	{Canonical: "related_to", Chars: Symmetric},
	{Canonical: "associated_with", Chars: Symmetric, SubPropertyOf: []string{"related_to"}},
	{Canonical: "depends_on", Chars: Transitive, SubPropertyOf: []string{"related_to"}},
	{Canonical: "precedes", Chars: Transitive | Asymmetric, InverseOf: "follows"},
	{Canonical: "follows", Chars: Transitive | Asymmetric, InverseOf: "precedes"},
}

// generalCompositions are the universal role-composition primitives.
var generalCompositions = []CompositionRule{
	{First: "instance_of", Second: "is_a", Result: "instance_of"},  // instance of a subclass is an instance of the superclass
	{First: "located_in", Second: "part_of", Result: "located_in"}, // located in a part => located in the whole
	{First: "same_as", Second: "is_a", Result: "is_a"},
}

// generalAliases maps common surface forms of the general relations to canonicals.
var generalAliases = map[string]string{
	"is a": "is_a", "isa": "is_a", "subclass of": "is_a", "is a kind of": "is_a",
	"type of": "is_a", "subtype of": "is_a",
	"instance of": "instance_of", "part of": "part_of", "located in": "located_in",
	"same as": "same_as", "equivalent to": "equivalent_to",
	"related to": "related_to", "associated with": "associated_with",
	"depends on": "depends_on", "precedes": "precedes",
}

// DefaultGeneralRegistry returns ONLY the general predicate primitives — a reusable
// base usable directly for non-medical graphs, or extended by a domain registry.
func DefaultGeneralRegistry() *PredicateRegistry {
	return buildRegistry(generalPredicates, generalAliases, generalCompositions, nil)
}

// buildRegistry assembles a registry from canonical predicate metas, raw->canonical
// aliases (each alias inheriting the canonical's characteristics/inverse/super-
// properties), composition rules, and disjoint pairs. Shared by the general and
// domain registries.
func buildRegistry(preds []PredicateMeta, aliases map[string]string,
	comps []CompositionRule, disjoint []DisjointPair) *PredicateRegistry {
	byCanon := map[string]PredicateMeta{}
	metas := make([]PredicateMeta, 0, len(preds)+len(aliases))
	for _, p := range preds {
		if p.Raw == "" {
			p.Raw = p.Canonical
		}
		byCanon[normKey(p.Canonical)] = p
		metas = append(metas, p)
	}
	for raw, canon := range aliases {
		base := byCanon[normKey(canon)]
		metas = append(metas, PredicateMeta{
			Raw:           raw,
			Canonical:     canon,
			Chars:         base.Chars,
			InverseOf:     base.InverseOf,
			SubPropertyOf: base.SubPropertyOf,
		})
	}
	return NewPredicateRegistry(metas, comps, disjoint)
}
