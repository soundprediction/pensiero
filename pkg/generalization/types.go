package generalization

import (
	"context"
	"fmt"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	DefaultMaxParentLevel = 4
	DefaultMinSupport     = 2
)

type TaxonomicDirection string

const (
	TaxonomicDirectionChildToParent TaxonomicDirection = "child-to-parent"
	TaxonomicDirectionParentToChild TaxonomicDirection = "parent-to-child"
)

func ParseTaxonomicDirection(raw string) (TaxonomicDirection, error) {
	switch strings.TrimSpace(raw) {
	case "", string(TaxonomicDirectionChildToParent):
		return TaxonomicDirectionChildToParent, nil
	case string(TaxonomicDirectionParentToChild):
		return TaxonomicDirectionParentToChild, nil
	default:
		return "", fmt.Errorf("invalid taxonomic direction %q: want %q or %q", raw, TaxonomicDirectionChildToParent, TaxonomicDirectionParentToChild)
	}
}

func (d TaxonomicDirection) valid() bool {
	return d == TaxonomicDirectionChildToParent || d == TaxonomicDirectionParentToChild
}

type NodeKind string

const (
	NodeScope    NodeKind = "scope"
	NodeConcept  NodeKind = "concept"
	NodeEndpoint NodeKind = "endpoint"
)

type Config struct {
	Scope               string
	ScopeEntities       []string
	TaxonomicPredicates []string
	TaxonomicDirection  TaxonomicDirection
	Predicates          []string
	MaxParentLevel      int
	MinParentSupport    int
	MinSupport          int

	// IncludeDirectRelations copies every in-scope SOURCE relation into the output
	// alongside the derived ones. Default false: a generalization graph is meant to
	// be a small DERIVED subgraph — the taxonomy it selected plus the relations it
	// lifted onto parents — not a duplicate of its source. Copying them produced an
	// output the same size as the input (188,570 source edges -> 187,303 "derived"),
	// which hid the fact that lifting had produced almost nothing and doubled the
	// storage for no added inference.
	//
	// Direct relations are still READ, because lifting is computed from them; this
	// only controls whether they are re-emitted.
	IncludeDirectRelations bool
}

type EntityRef struct {
	ID   string
	Name string
}

type Node struct {
	ID      string
	Name    string
	Kind    NodeKind
	Depth   int
	Support int
}

type Relation struct {
	ID         string
	SourceID   string
	SourceName string
	Predicate  string
	TargetID   string
	TargetName string
	Sources    []string
	Confidence float64
	Support    int
	Lifted     bool
}

type Graph struct {
	Stats     Stats
	Scope     string
	Nodes     []Node
	Relations []Relation
}

type DroppedEdgeBackup interface {
	Record(ctx context.Context, scope string, dropped []Relation) error
}

type Stats struct {
	ParentLevelCounts   map[int]int
	NodeCount           int
	RelationCount       int
	ScopeEntityCount    int
	ConceptCount        int
	EndpointCount       int
	DirectRelationCount int
	LiftedRelationCount int
}

type Builder struct {
	source reasoning.GraphQuerier
	reg    *reasoning.PredicateRegistry
	cfg    Config
}

type taxonomyRow struct {
	child      EntityRef
	parent     EntityRef
	predicate  string
	depth      int
	confidence float64
}

type directRow struct {
	source     EntityRef
	target     EntityRef
	id         string
	predicate  string
	confidence float64
}
