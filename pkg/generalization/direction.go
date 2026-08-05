package generalization

import (
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

// Stored taxonomy orientation is not evidence.
//
// Measured on the deployed thyroid graph, of 5,029 IS_PARENT_OF edges 3,640
// (72.4%) are stored in BOTH orientations, and of the unidirectional remainder
// that an independent signal can adjudicate, 51.8% run parent->child and 48.2%
// backwards. That is a coin flip, so walking the hierarchy in the direction the
// graph happens to store lifts roughly half of all relations onto descendants
// instead of ancestors — silently, and with a proof attached.
//
// A DirectionSource replaces that guess with a derived answer. Pairs it cannot
// orient are DROPPED rather than passed through: a dropped edge costs recall,
// while a wrongly-oriented one attaches findings to the wrong condition.
type DirectionSource interface {
	// Orient returns which of the two entity names is the parent. ok is false
	// when no signal could determine it, in which case the pair must not be used.
	Orient(a, b string) (parent, child string, ok bool)
}

// applyDirection re-derives the orientation of taxonomy rows, dropping those
// that cannot be oriented.
//
// It returns the surviving rows and the number dropped, so the caller can report
// what was discarded instead of silently shrinking the hierarchy.
func applyDirection(src DirectionSource, rows []taxonomyRow) (kept []taxonomyRow, dropped, flipped int) {
	if src == nil {
		return rows, 0, 0
	}
	kept = make([]taxonomyRow, 0, len(rows))
	for _, r := range rows {
		childName := strings.TrimSpace(r.child.Name)
		parentName := strings.TrimSpace(r.parent.Name)
		if childName == "" || parentName == "" {
			dropped++
			continue
		}
		parent, _, ok := src.Orient(childName, parentName)
		if !ok {
			dropped++
			continue
		}
		// The row is stored child -> parent. If the resolver says the row's
		// "child" is actually the parent, swap the endpoints.
		if namesEqual(parent, childName) {
			r.child, r.parent = r.parent, r.child
			flipped++
		}
		kept = append(kept, r)
	}
	return kept, dropped, flipped
}

// namesEqual compares entity names the way the resolver does: case-insensitively
// and ignoring surrounding and repeated whitespace. A stricter comparison here
// would fail to recognise the resolver's own answer and flip rows at random.
func namesEqual(a, b string) bool {
	return strings.EqualFold(collapseSpace(a), collapseSpace(b))
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// normalizeRelationDirection rewrites a source relation into the orientation its
// endpoint types say it actually expresses.
//
// Clinical relation direction in the ingested corpus is inconsistent: of 20,172
// HAS_PHENOTYPE edges measured on a deployed graph, 11,613 run DISEASE->symptom
// and 8,523 run symptom->DISEASE. Lifting propagates whatever it is given, so a
// derived graph inherits the defect.
//
// pkg/reasoning repairs this at READ time, but only as a veto: path search runs
// before relabelling, so an edge stored backwards is found under its stored
// predicate, relabelled to the inverse, and then REJECTED — while a query for
// the inverse never finds it at all. The edge becomes unusable in either
// direction. Normalising at WRITE time is what makes it findable: the edge is
// stored the way the reasoner will look for it.
//
// The endpoints are swapped rather than the predicate relabelled, so the derived
// graph keeps one canonical spelling per relationship ("B has_phenotype A"
// instead of a mix of has_phenotype and phenotype_of).
//
// Returns the row unchanged when types are absent or ambiguous — the same
// fail-closed posture as the read-time repair, since a wrongly flipped clinical
// edge asserts something about a patient that the graph does not contain.
func normalizeRelationDirection(reg *reasoning.PredicateRegistry, row directRow) (directRow, bool) {
	if reg == nil || len(row.source.Labels) == 0 || len(row.target.Labels) == 0 {
		return row, false
	}
	oriented := reasoning.OrientPredicate(reg, row.predicate, row.source.Labels, row.target.Labels)
	if oriented == "" || strings.EqualFold(oriented, row.predicate) {
		return row, false
	}
	// The types say this edge expresses the INVERSE of how it is stored, so the
	// same fact in the stored predicate's orientation is the endpoints reversed.
	meta, known := reg.Canonical(oriented)
	if !known || !strings.EqualFold(meta.InverseOf, canonicalOf(reg, row.predicate)) {
		// Not a clean inverse pair — leave it alone rather than guess.
		return row, false
	}
	row.source, row.target = row.target, row.source
	return row, true
}

func canonicalOf(reg *reasoning.PredicateRegistry, pred string) string {
	meta, _ := reg.Canonical(pred)
	return meta.Canonical
}

// applyRelationDirection normalises a batch of source relations, returning the
// count flipped so a run reports how much of its input was stored backwards.
func applyRelationDirection(reg *reasoning.PredicateRegistry, rows []directRow) ([]directRow, int) {
	flipped := 0
	for i := range rows {
		if out, did := normalizeRelationDirection(reg, rows[i]); did {
			rows[i] = out
			flipped++
		}
	}
	return rows, flipped
}
