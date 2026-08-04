package generalization

import "strings"

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
