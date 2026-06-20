// Package reasoning is a pluggable symbolic graph-reasoning engine: bounded,
// explainable multi-hop derivation over a property graph (ladybug/Kuzu by default).
// It is consumed by hosts (e.g. the humn DDx verifier) through the Reasoner
// interface and a name-keyed registry, so a host can register/swap reasoning
// backends without a compile-time dependency on a concrete graph driver.
//
// See SYMBOLIC_GRAPH_LOGIC.md for the full design.
package reasoning

import "context"

// Verdict is the symbolic outcome of a claim check.
type Verdict string

const (
	VerdictEntailed     Verdict = "entailed"     // a supporting derivation exists
	VerdictContradicted Verdict = "contradicted" // an ontology-disjointness conflict exists
	VerdictUnsupported  Verdict = "unsupported"  // neither: a knowledge gap
)

// Claim asks whether (Subject, Predicate, Object) holds in the graph.
type Claim struct {
	Subject   string // entity name or uuid as stored on Entity
	Predicate string // raw or canonical; normalized via the registry (functor F)
	Object    string
}

// ProofStep is one edge application in a derivation chain. The chain IS the
// explanation returned to the host.
type ProofStep struct {
	EdgeID     string  `json:"edge_id"`   // RelatesToNode_ id, or "F:..", ":refl"
	Rule       string  `json:"rule"`      // composition|trans|sym|inv|subsumption|F|refl
	Predicate  string  `json:"predicate"` // canonical predicate applied at this step
	Source     string  `json:"source"`    // resolved on hydration
	Target     string  `json:"target"`
	Confidence float64 `json:"confidence"` // this edge's confidence (pre-decay)
}

// Proof is a complete, ranked derivation for one derived fact.
type Proof struct {
	Source     string      `json:"source"`
	Target     string      `json:"target"`
	Predicate  string      `json:"predicate"`
	RuleClass  string      `json:"rule_class"`
	Steps      []ProofStep `json:"steps"`
	Hops       int         `json:"hops"`
	Confidence float64     `json:"confidence"` // composed (Context Monoid): prod(edge) * decay^(hops-1)
}

// DeriveRequest parameterizes a path-derivation query.
type DeriveRequest struct {
	Source         string
	Target         string   // empty => any reachable target
	Preds          []string // optional: restrict intermediate predicates (canonical)
	MaxHops        int      // logical hops (physical depth is 2x in the reified model)
	Decay          float64  // per-hop confidence decay (0,1]
	MinConf        float64  // early prune: drop proofs below this composed confidence
	Limit          int      // top-k proofs by confidence
	IncludeInverse bool     // allow traversing inverse/symmetric edges
}

// EntailResult is the outcome of Entails.
type EntailResult struct {
	Best       *Proof
	Verdict    Verdict
	All        []Proof
	Confidence float64
}

// Config holds engine defaults; zero values are replaced by sensible defaults.
type Config struct {
	MaxHops        int     // default 4
	Decay          float64 // default 0.9
	MinConf        float64 // default 0.05
	Limit          int     // default 8
	TauHigh        float64 // confidence at/above which Entails passes without deferring (default 0.6)
	ExcludeDeduced bool    // exclude status='deduced' edges to avoid circular self-support (default true)
}

func (c Config) withDefaults() Config {
	if c.MaxHops <= 0 {
		c.MaxHops = 4
	}
	if c.Decay <= 0 || c.Decay > 1 {
		c.Decay = 0.9
	}
	if c.MinConf <= 0 {
		c.MinConf = 0.05
	}
	if c.Limit <= 0 {
		c.Limit = 8
	}
	if c.TauHigh <= 0 {
		c.TauHigh = 0.6
	}
	return c
}

// GraphQuerier is the minimal graph-access surface the engine needs. It is
// satisfied by a thin adapter over the ladybug (go-predicato) driver in
// production, and by a mock in tests — so this package compiles and unit-tests as
// pure Go with no CGO/driver dependency.
type GraphQuerier interface {
	// Query runs a (Cypher) query with params and returns result rows as maps.
	Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error)
}

// Reasoner is the plugin contract a host (humn DDx verifier) depends on.
type Reasoner interface {
	// Derive returns ranked proof paths from Source toward Target (or any target).
	Derive(ctx context.Context, req DeriveRequest) ([]Proof, error)
	// Entails decides whether a claim is symbolically supported, contradicted, or
	// unsupported, with the best supporting/conflicting proof.
	Entails(ctx context.Context, c Claim) (EntailResult, error)
	// Contradicts reports an ontology-disjointness conflict for the claim.
	Contradicts(ctx context.Context, c Claim) (bool, *Proof, error)
	// Name identifies the backend (for the registry / diagnostics).
	Name() string
}
