package generalization

import (
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
