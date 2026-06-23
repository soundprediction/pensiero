package connector

import (
	"net/http"
	"time"
)

// PredicatoClient interacts with the Predicato API
type PredicatoClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewPredicatoClient creates a new Predicato client
func NewPredicatoClient(baseURL string) *PredicatoClient {
	return &PredicatoClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExtractExtendedRequest matches Predicato's expectation
type ExtractExtendedRequest struct {
	Text          string   `json:"text"`
	EntityTypes   []string `json:"entity_types,omitempty"`
	RelationTypes []string `json:"relation_types,omitempty"`
}

// ExtendedTriple matches Predicato's output
type ExtendedTriple struct {
	Subject           string  `json:"subject"`
	Predicate         string  `json:"predicate"`
	Object            string  `json:"object"`
	Condition         string  `json:"condition,omitempty"`
	Temporal          string  `json:"temporal,omitempty"`
	Location          string  `json:"location,omitempty"`
	Certainty         string  `json:"certainty,omitempty"`
	Scope             string  `json:"scope,omitempty"`
	SourceAttribution string  `json:"source_attribution,omitempty"`
	Confidence        float64 `json:"confidence,omitempty"`
}

// Rule matches Predicato's output
type Rule struct {
	Antecedent        string  `json:"antecedent"`
	Consequent        string  `json:"consequent"`
	Exception         string  `json:"exception,omitempty"`
	RuleType          string  `json:"rule_type,omitempty"`
	Scope             string  `json:"scope,omitempty"`
	SourceAttribution string  `json:"source_attribution,omitempty"`
	Confidence        float64 `json:"confidence,omitempty"`
}

// ExtendedExtractionResult matches Predicato's output
type ExtendedExtractionResult struct {
	SourceText string              `json:"source_text"`
	Entities   map[string][]string `json:"entities"`
	Relations  []interface{}       `json:"relations"`
	Triples    []ExtendedTriple    `json:"triples"`
	Rules      []Rule              `json:"rules"`
}
