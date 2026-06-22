package grpcsvc

import (
	"context"

	"github.com/soundprediction/pensiero/pkg/grpcsvc/pb"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// BackendName identifies the remote backend in the registry / diagnostics.
const BackendName = "grpc"

// Client calls a remote pensiero reasoner over gRPC, presenting the
// reasoning.Reasoner surface so it is a drop-in for an in-process reasoner.
type Client struct {
	cc  *grpc.ClientConn
	rc  pb.ReasonerServiceClient
	own bool
}

// compile-time check: the gRPC client satisfies the host Reasoner contract.
var _ reasoning.Reasoner = (*Client)(nil)

// Dial connects to a pensiero reasoner gRPC server at target (host:port).
func Dial(target string, opts ...grpc.DialOption) (*Client, error) {
	base := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	cc, err := grpc.NewClient(target, append(base, opts...)...)
	if err != nil {
		return nil, err
	}
	return &Client{cc: cc, rc: pb.NewReasonerServiceClient(cc), own: true}, nil
}

// NewClientConn wraps an already-built ClientConn; the caller owns its lifecycle.
func NewClientConn(cc *grpc.ClientConn) *Client {
	return &Client{cc: cc, rc: pb.NewReasonerServiceClient(cc)}
}

// Entails decides whether a claim is symbolically supported/contradicted.
func (c *Client) Entails(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
	res, err := c.rc.Entails(ctx, &pb.EntailsRequest{Claim: claimToProto(claim)})
	if err != nil {
		return reasoning.EntailResult{}, err
	}
	return entailResultFromProto(res), nil
}

// Contradicts reports an ontology-disjointness conflict for the claim.
func (c *Client) Contradicts(ctx context.Context, claim reasoning.Claim) (bool, *reasoning.Proof, error) {
	res, err := c.rc.Contradicts(ctx, &pb.ContradictsRequest{Claim: claimToProto(claim)})
	if err != nil {
		return false, nil, err
	}
	return res.GetContradicts(), proofPtrFromProto(res.GetProof()), nil
}

// Derive returns ranked proof paths from Source toward Target.
func (c *Client) Derive(ctx context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	res, err := c.rc.Derive(ctx, deriveReqToProto(req))
	if err != nil {
		return nil, err
	}
	return proofsFromProto(res.GetProofs()), nil
}

// Name identifies the backend.
func (c *Client) Name() string { return BackendName }

// Health pings the server.
func (c *Client) Health(ctx context.Context) (string, error) {
	h, err := c.rc.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		return "", err
	}
	return h.GetStatus(), nil
}

// Close closes the underlying connection when this Client owns it.
func (c *Client) Close() error {
	if c.own {
		return c.cc.Close()
	}
	return nil
}
