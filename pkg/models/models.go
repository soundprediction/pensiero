package models

import (
	"encoding/json"
	"time"
)

// EpistemicStatus defines the source of the knowledge fact
type EpistemicStatus string

const (
	StatusAxiom       EpistemicStatus = "axiom"
	StatusObservation EpistemicStatus = "observation"
	StatusInduced     EpistemicStatus = "induced"
	StatusDeduced     EpistemicStatus = "deduced"
)

// Context represents the algebraic metadata for a relation
type Context struct {
	Confidence           float64                `json:"confidence"`
	ValidFrom            *time.Time             `json:"valid_from,omitempty"`
	ValidTo              *time.Time             `json:"valid_to,omitempty"`
	Conditions           []Condition            `json:"conditions"`
	Provenance           *Provenance            `json:"provenance,omitempty"`
	AdditionalProperties map[string]interface{} `json:"-"`
}

type Condition struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type Provenance struct {
	EvidenceID           string    `json:"evidence_id"`
	SourceSystem         string    `json:"source_system"`
	Extractor            string    `json:"extractor"`
	ExtractionConfidence float64   `json:"extraction_confidence"`
	EvidenceRef          string    `json:"evidence_ref,omitempty"`
	Timestamp            time.Time `json:"timestamp"`
}

// EpistemicEdge is the primary fact record
type EpistemicEdge struct {
	ID           string          `json:"id"`
	Source       string          `json:"source"`
	Target       string          `json:"target"`
	Predicate    string          `json:"predicate"`
	RawPredicate string          `json:"raw_predicate"`
	Status       EpistemicStatus `json:"status"`
	Confidence   float64         `json:"confidence"`
	Context      json.RawMessage `json:"context"` // Stored as Json in CozoDB
}

// PredicateRegistry maps raw text to canonical logic
type PredicateRegistry struct {
	Raw          string `json:"raw"`
	Canonical    string `json:"canonical"`
	LogicalClass string `json:"logical_class"` // transitive, symmetric, etc.
	Domain       string `json:"domain"`
	Range        string `json:"range"`
}

// MetaRelation holds induced or generalized rules
type MetaRelation struct {
	ID         string          `json:"id"`
	Head       string          `json:"head"`
	Body       json.RawMessage `json:"body"` // array of literals or conditions
	Frequency  int             `json:"frequency"`
	Confidence float64         `json:"confidence"`
	Provenance json.RawMessage `json:"provenance"`
	CreatedAt  time.Time       `json:"created_at"`
}

// AuditLog records system and user actions
type AuditLog struct {
	EntryID    string          `json:"entry_id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Timestamp  time.Time       `json:"timestamp"`
	Details    json.RawMessage `json:"details"`
}
