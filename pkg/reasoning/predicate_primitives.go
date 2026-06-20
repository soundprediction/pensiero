package reasoning

// Medical-DOMAIN predicate primitives, declared via the general characteristics in
// predicates.go and layered ON TOP OF the general primitives (general_primitives.go).
// Taxonomic/mereological/identity/association relations come from the general base;
// only clinical-specific predicates are declared here.

// medicalPredicates are the clinical predicates (domain layer). Sub-property edges
// reference general predicates (e.g. associated_with) resolved in the merged registry.
var medicalPredicates = []PredicateMeta{
	// clinical evidence relations — directed with inverses; sub-properties of the
	// general associated_with so any clinical link implies (weaker) association.
	{Canonical: "symptom_of", InverseOf: "has_symptom", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "has_symptom", InverseOf: "symptom_of", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "has_phenotype", InverseOf: "phenotype_of", SubPropertyOf: []string{"has_symptom"}},
	{Canonical: "phenotype_of", InverseOf: "has_phenotype", SubPropertyOf: []string{"symptom_of"}},
	{Canonical: "sign_of", InverseOf: "has_sign", SubPropertyOf: []string{"symptom_of"}},
	{Canonical: "has_sign", InverseOf: "sign_of", SubPropertyOf: []string{"has_symptom"}},
	{Canonical: "manifests_as", InverseOf: "manifestation_of", SubPropertyOf: []string{"has_symptom"}},
	{Canonical: "manifestation_of", InverseOf: "manifests_as", SubPropertyOf: []string{"symptom_of"}},

	// causal / risk
	{Canonical: "causes", InverseOf: "caused_by", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "caused_by", InverseOf: "causes", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "risk_factor_for", InverseOf: "has_risk_factor", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "has_risk_factor", InverseOf: "risk_factor_for", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "complication_of", InverseOf: "has_complication", SubPropertyOf: []string{"associated_with"}},
	{Canonical: "has_complication", InverseOf: "complication_of", SubPropertyOf: []string{"associated_with"}},

	// clinical associative
	{Canonical: "co_occurs_with", Chars: Symmetric, SubPropertyOf: []string{"associated_with"}},
	{Canonical: "correlated_with", Chars: Symmetric, SubPropertyOf: []string{"associated_with"}},
	{Canonical: "differential_of", Chars: Symmetric, SubPropertyOf: []string{"associated_with"}},

	// therapeutic / pharmacologic / diagnostic
	{Canonical: "treats", InverseOf: "treated_by"},
	{Canonical: "treated_by", InverseOf: "treats"},
	{Canonical: "contraindicated_for", InverseOf: "has_contraindication"},
	{Canonical: "has_contraindication", InverseOf: "contraindicated_for"},
	{Canonical: "interacts_with", Chars: Symmetric},
	{Canonical: "diagnosed_by", InverseOf: "diagnoses"},
	{Canonical: "diagnoses", InverseOf: "diagnosed_by"},
}

// medicalCompositions: subtype inheritance of clinical features (general primitive
// is_a/part_of composed with clinical relations).
var medicalCompositions = []CompositionRule{
	{First: "is_a", Second: "has_symptom", Result: "has_symptom"},
	{First: "is_a", Second: "has_phenotype", Result: "has_phenotype"},
	{First: "is_a", Second: "has_sign", Result: "has_sign"},
	{First: "is_a", Second: "has_risk_factor", Result: "has_risk_factor"},
	{First: "is_a", Second: "has_complication", Result: "has_complication"},
	{First: "is_a", Second: "treated_by", Result: "treated_by"},
	{First: "part_of", Second: "has_symptom", Result: "has_symptom"},
}

// medicalAliases maps clinical surface forms to canonicals (general aliases like
// "is a"/"part of"/"associated with" come from generalAliases).
var medicalAliases = map[string]string{
	"is a symptom of": "symptom_of", "symptom of": "symptom_of",
	"is a sign of": "sign_of", "sign of": "sign_of", "has symptom": "has_symptom",
	"has phenotype": "has_phenotype", "phenotype of": "phenotype_of",
	"presents with": "has_symptom", "is a presenting feature of": "symptom_of",
	"manifests as": "manifests_as",
	"causes":       "causes", "cause": "causes", "can cause": "causes", "may cause": "causes",
	"caused by": "caused_by", "results in": "causes", "leads to": "causes",
	"risk factor for": "risk_factor_for", "is a risk factor for": "risk_factor_for",
	"complication of":    "complication_of",
	"is associated with": "associated_with", "associate": "associated_with",
	"correlated with": "correlated_with", "positive_correlate": "correlated_with",
	"co-occurs with": "co_occurs_with",
	"treats":         "treats", "treated by": "treated_by", "treated with": "treated_by",
	"used to treat": "treats", "indicated for": "treats",
	"contraindicated for": "contraindicated_for", "interacts with": "interacts_with",
	"diagnosed by": "diagnosed_by", "diagnoses": "diagnoses",
}

// DefaultMedicalRegistry returns the general primitives EXTENDED with the medical
// domain: general taxonomy/mereology/association + the clinical predicates,
// composition rules, and aliases. A host may further extend it.
func DefaultMedicalRegistry() *PredicateRegistry {
	preds := append(append([]PredicateMeta{}, generalPredicates...), medicalPredicates...)
	comps := append(append([]CompositionRule{}, generalCompositions...), medicalCompositions...)
	aliases := map[string]string{}
	for k, v := range generalAliases {
		aliases[k] = v
	}
	for k, v := range medicalAliases {
		aliases[k] = v
	}
	return buildRegistry(preds, aliases, comps, nil)
}

// TransitivePrimitives is the default taxonomic-closure predicate set.
func TransitivePrimitives() []string {
	return []string{"is_a", "subsumes", "part_of", "has_part", "located_in"}
}
