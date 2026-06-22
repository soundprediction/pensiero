package grpcsvc

import (
	"context"
	"net"
	"testing"

	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// fakeReasoner returns a canned entailment for the roundtrip.
type fakeReasoner struct{ gotClaim reasoning.Claim }

func (f *fakeReasoner) Name() string { return "fake" }
func (f *fakeReasoner) Derive(context.Context, reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	return nil, nil
}
func (f *fakeReasoner) Contradicts(context.Context, reasoning.Claim) (bool, *reasoning.Proof, error) {
	return false, nil, nil
}
func (f *fakeReasoner) Entails(_ context.Context, c reasoning.Claim) (reasoning.EntailResult, error) {
	f.gotClaim = c
	return reasoning.EntailResult{
		Verdict:    reasoning.VerdictEntailed,
		Confidence: 0.81,
		Best:       &reasoning.Proof{Source: c.Subject, Target: c.Object, Predicate: c.Predicate, Hops: 2, Confidence: 0.81, Steps: []reasoning.ProofStep{{Rule: "trans", Predicate: "is_a", Confidence: 0.9}}},
	}, nil
}

func TestEntailsRoundtrip(t *testing.T) {
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	fake := &fakeReasoner{}
	gs := grpc.NewServer()
	NewServer(fake).Register(gs)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cc.Close() }()
	client := NewClientConn(cc)

	res, err := client.Entails(context.Background(), reasoning.Claim{Subject: "hypothyroidism", Predicate: "is_a", Object: "thyroid disorder"})
	if err != nil {
		t.Fatalf("Entails: %v", err)
	}
	if res.Verdict != reasoning.VerdictEntailed || res.Confidence != 0.81 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Best == nil || res.Best.Hops != 2 || len(res.Best.Steps) != 1 || res.Best.Steps[0].Rule != "trans" {
		t.Fatalf("proof not round-tripped: %+v", res.Best)
	}
	if fake.gotClaim.Subject != "hypothyroidism" {
		t.Fatalf("server did not receive claim: %+v", fake.gotClaim)
	}
}
