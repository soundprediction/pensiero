package reasoning

import (
	"context"
	"testing"
)

type fakeReasoner struct {
	entailsResult EntailResult
	deriveProofs  []Proof
}

func (f fakeReasoner) Derive(context.Context, DeriveRequest) ([]Proof, error) {
	return append([]Proof{}, f.deriveProofs...), nil
}

func (f fakeReasoner) Entails(context.Context, Claim) (EntailResult, error) {
	return f.entailsResult, nil
}

func (f fakeReasoner) Contradicts(context.Context, Claim) (bool, *Proof, error) {
	return false, nil, nil
}

func (f fakeReasoner) Name() string {
	return "fake"
}

func TestPredicateConstrainedEntailsKeepsMatchingPredicate(t *testing.T) {
	wrapped := NewPredicateConstrained(fakeReasoner{
		entailsResult: EntailResult{
			Verdict: VerdictEntailed,
			Best: &Proof{
				Steps: []ProofStep{{Predicate: "treats"}},
			},
			All: []Proof{{
				Steps: []ProofStep{{Predicate: "treats"}},
			}},
			Confidence: 0.9,
		},
	}, DefaultMedicalRegistry())

	res, err := wrapped.Entails(context.Background(), Claim{
		Subject:   "aspirin",
		Predicate: "treats",
		Object:    "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "treats" {
		t.Fatalf("best=%+v, want effective predicate treats", res.Best)
	}
	if len(res.All) != 1 {
		t.Fatalf("all=%d, want pass-through proofs", len(res.All))
	}
}

func TestPredicateConstrainedEntailsDowngradesWrongPredicate(t *testing.T) {
	wrapped := NewPredicateConstrained(fakeReasoner{
		entailsResult: EntailResult{
			Verdict: VerdictEntailed,
			Best: &Proof{
				Steps: []ProofStep{{Predicate: "causes"}},
			},
			All: []Proof{{
				Steps: []ProofStep{{Predicate: "causes"}},
			}},
			Confidence: 0.9,
		},
	}, DefaultMedicalRegistry())

	res, err := wrapped.Entails(context.Background(), Claim{
		Subject:   "aspirin",
		Predicate: "treats",
		Object:    "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
	if res.Best != nil || len(res.All) != 0 {
		t.Fatalf("best=%+v all=%d, want dropped proofs", res.Best, len(res.All))
	}
}

func TestPredicateConstrainedEntailsDowngradesNoStepPredicates(t *testing.T) {
	wrapped := NewPredicateConstrained(fakeReasoner{
		entailsResult: EntailResult{
			Verdict: VerdictEntailed,
			Best: &Proof{
				Steps: []ProofStep{{Predicate: " "}},
			},
			Confidence: 0.9,
		},
	}, DefaultMedicalRegistry())

	res, err := wrapped.Entails(context.Background(), Claim{
		Subject:   "aspirin",
		Predicate: "treats",
		Object:    "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
	if res.Best != nil || len(res.All) != 0 {
		t.Fatalf("best=%+v all=%d, want dropped proofs", res.Best, len(res.All))
	}
}

func TestPredicateConstrainedEntailsPassesContradictedThrough(t *testing.T) {
	proof := &Proof{
		Predicate: "contraindicated_for",
		Steps:     []ProofStep{{Predicate: "contraindicated_for"}},
	}
	wrapped := NewPredicateConstrained(fakeReasoner{
		entailsResult: EntailResult{
			Verdict:    VerdictContradicted,
			Best:       proof,
			Confidence: 1.0,
		},
	}, DefaultMedicalRegistry())

	res, err := wrapped.Entails(context.Background(), Claim{
		Subject:   "aspirin",
		Predicate: "treats",
		Object:    "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictContradicted {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictContradicted)
	}
	if res.Best != proof {
		t.Fatalf("best=%+v, want contradicted proof passed through", res.Best)
	}
}

func TestPredicateConstrainedDeriveFiltersPredicate(t *testing.T) {
	wrapped := NewPredicateConstrained(fakeReasoner{
		deriveProofs: []Proof{
			{Steps: []ProofStep{{Predicate: "treats"}}},
			{Steps: []ProofStep{{Predicate: "causes"}}},
		},
	}, DefaultMedicalRegistry())

	proofs, err := wrapped.Derive(context.Background(), DeriveRequest{
		Source:    "aspirin",
		Target:    "headache",
		Predicate: "treats",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 1 {
		t.Fatalf("proofs=%d, want 1", len(proofs))
	}
	if proofs[0].Predicate != "treats" {
		t.Fatalf("predicate=%q, want treats", proofs[0].Predicate)
	}
}
