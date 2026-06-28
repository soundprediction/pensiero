// Package grpcsvc serves pensiero's symbolic reasoner over gRPC so it can run as
// a separate process / on a different machine from its host (e.g. humn). The wire
// contract is in proto/pensiero/v1/reasoning.proto, code-generated into ./pb; the
// server wraps a reasoning.Reasoner and the Client presents the Reasoner surface.
package grpcsvc

import (
	"context"
	"fmt"
	"net"

	"github.com/soundprediction/pensiero/pkg/grpcsvc/pb"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc"
)

// ServiceName is the fully-qualified gRPC service name.
var ServiceName = pb.ReasonerService_ServiceDesc.ServiceName

// RuleFirer forward-chains conditional rules from per-request assumed facts.
type RuleFirer interface {
	FireRules(ctx context.Context, maxRules int) ([]reasoning.FiredRule, error)
}

// Server adapts a reasoning.Reasoner to the generated ReasonerServiceServer.
type Server struct {
	pb.UnimplementedReasonerServiceServer
	r     reasoning.Reasoner
	firer RuleFirer
}

// NewServer wraps a reasoner for gRPC serving.
func NewServer(r reasoning.Reasoner) *Server { return &Server{r: r} }

// SetRuleFirer wires the forward-chaining management reasoner (over the shared
// rules) used by the FireRules RPC. Optional; FireRules returns empty if unset.
func (s *Server) SetRuleFirer(f RuleFirer) { s.firer = f }

func (s *Server) FireRules(ctx context.Context, req *pb.FireRulesRequest) (*pb.FireRulesResponse, error) {
	if s.firer == nil {
		return &pb.FireRulesResponse{}, nil
	}
	if facts := claimsFromProto(req.GetAssumedFacts()); len(facts) > 0 {
		ctx = reasoning.WithAssumedFacts(ctx, facts)
	}
	fired, err := s.firer.FireRules(ctx, int(req.GetMaxRules()))
	if err != nil {
		return nil, err
	}
	return &pb.FireRulesResponse{Fired: firedRulesToProto(fired)}, nil
}

func (s *Server) Entails(ctx context.Context, req *pb.EntailsRequest) (*pb.EntailResult, error) {
	if facts := claimsFromProto(req.GetAssumedFacts()); len(facts) > 0 {
		ctx = reasoning.WithAssumedFacts(ctx, facts)
	}
	res, err := s.r.Entails(ctx, claimFromProto(req.GetClaim()))
	if err != nil {
		return nil, err
	}
	return entailResultToProto(res), nil
}

func (s *Server) Contradicts(ctx context.Context, req *pb.ContradictsRequest) (*pb.ContradictsResponse, error) {
	ok, proof, err := s.r.Contradicts(ctx, claimFromProto(req.GetClaim()))
	if err != nil {
		return nil, err
	}
	return &pb.ContradictsResponse{Contradicts: ok, Proof: proofPtrToProto(proof)}, nil
}

func (s *Server) Derive(ctx context.Context, req *pb.DeriveRequest) (*pb.DeriveResponse, error) {
	proofs, err := s.r.Derive(ctx, deriveReqFromProto(req))
	if err != nil {
		return nil, err
	}
	return &pb.DeriveResponse{Proofs: proofsToProto(proofs)}, nil
}

func (s *Server) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: "ok"}, nil
}

// Register installs the ReasonerService on a grpc.Server.
func (s *Server) Register(gs *grpc.Server) { pb.RegisterReasonerServiceServer(gs, s) }

// Serve builds a grpc.Server, registers the reasoner service, and blocks serving
// on addr until the listener closes.
func Serve(r reasoning.Reasoner, addr string, opts ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("grpcsvc listen %s: %w", addr, err)
	}
	gs := grpc.NewServer(opts...)
	NewServer(r).Register(gs)
	return gs.Serve(lis)
}
