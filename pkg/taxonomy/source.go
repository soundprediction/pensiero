package taxonomy

// Orient makes a Deriver usable as a direction source for graph builders.
//
// It answers from the signals alone. Callers that also know which orientations
// the graph stores should use Derive with a populated Pair when they need the
// ContradictsStored tally; Orient deliberately does not take that argument,
// because stored orientation must never influence the answer — only the report.
func (d *Deriver) Orient(a, b string) (parent, child string, ok bool) {
	dir := d.Derive(Pair{A: a, B: b})
	if dir.Tier != TierOriented {
		return "", "", false
	}
	return dir.Parent, dir.Child, true
}
