package grpcsvc

import (
	"github.com/soundprediction/pensiero/pkg/grpcsvc/pb"
	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func claimToProto(c reasoning.Claim) *pb.Claim {
	return &pb.Claim{Subject: c.Subject, Predicate: c.Predicate, Object: c.Object}
}

func claimFromProto(p *pb.Claim) reasoning.Claim {
	if p == nil {
		return reasoning.Claim{}
	}
	return reasoning.Claim{Subject: p.Subject, Predicate: p.Predicate, Object: p.Object}
}

func proofStepToProto(s reasoning.ProofStep) *pb.ProofStep {
	return &pb.ProofStep{EdgeId: s.EdgeID, Rule: s.Rule, Predicate: s.Predicate, Source: s.Source, Target: s.Target, Confidence: s.Confidence}
}

func proofStepFromProto(p *pb.ProofStep) reasoning.ProofStep {
	return reasoning.ProofStep{EdgeID: p.EdgeId, Rule: p.Rule, Predicate: p.Predicate, Source: p.Source, Target: p.Target, Confidence: p.Confidence}
}

func proofValToProto(pr reasoning.Proof) *pb.Proof {
	steps := make([]*pb.ProofStep, 0, len(pr.Steps))
	for i := range pr.Steps {
		steps = append(steps, proofStepToProto(pr.Steps[i]))
	}
	return &pb.Proof{Source: pr.Source, Target: pr.Target, Predicate: pr.Predicate, RuleClass: pr.RuleClass, Steps: steps, Hops: int32(pr.Hops), Confidence: pr.Confidence}
}

func proofPtrToProto(pr *reasoning.Proof) *pb.Proof {
	if pr == nil {
		return nil
	}
	return proofValToProto(*pr)
}

func proofFromProto(p *pb.Proof) reasoning.Proof {
	if p == nil {
		return reasoning.Proof{}
	}
	steps := make([]reasoning.ProofStep, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, proofStepFromProto(s))
	}
	return reasoning.Proof{Source: p.Source, Target: p.Target, Predicate: p.Predicate, RuleClass: p.RuleClass, Steps: steps, Hops: int(p.Hops), Confidence: p.Confidence}
}

func proofPtrFromProto(p *pb.Proof) *reasoning.Proof {
	if p == nil {
		return nil
	}
	pr := proofFromProto(p)
	return &pr
}

func proofsToProto(prs []reasoning.Proof) []*pb.Proof {
	if len(prs) == 0 {
		return nil
	}
	out := make([]*pb.Proof, 0, len(prs))
	for i := range prs {
		out = append(out, proofValToProto(prs[i]))
	}
	return out
}

func proofsFromProto(ps []*pb.Proof) []reasoning.Proof {
	if len(ps) == 0 {
		return nil
	}
	out := make([]reasoning.Proof, 0, len(ps))
	for _, p := range ps {
		out = append(out, proofFromProto(p))
	}
	return out
}

func entailResultToProto(r reasoning.EntailResult) *pb.EntailResult {
	return &pb.EntailResult{Best: proofPtrToProto(r.Best), Verdict: string(r.Verdict), All: proofsToProto(r.All), Confidence: r.Confidence}
}

func entailResultFromProto(p *pb.EntailResult) reasoning.EntailResult {
	if p == nil {
		return reasoning.EntailResult{}
	}
	return reasoning.EntailResult{Best: proofPtrFromProto(p.Best), Verdict: reasoning.Verdict(p.Verdict), All: proofsFromProto(p.All), Confidence: p.Confidence}
}

func deriveReqToProto(r reasoning.DeriveRequest) *pb.DeriveRequest {
	return &pb.DeriveRequest{Source: r.Source, Target: r.Target, Preds: r.Preds, MaxHops: int32(r.MaxHops), Decay: r.Decay, MinConf: r.MinConf, Limit: int32(r.Limit), IncludeInverse: r.IncludeInverse}
}

func deriveReqFromProto(p *pb.DeriveRequest) reasoning.DeriveRequest {
	if p == nil {
		return reasoning.DeriveRequest{}
	}
	return reasoning.DeriveRequest{Source: p.Source, Target: p.Target, Preds: p.Preds, MaxHops: int(p.MaxHops), Decay: p.Decay, MinConf: p.MinConf, Limit: int(p.Limit), IncludeInverse: p.IncludeInverse}
}
