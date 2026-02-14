package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/soundprediction/pensiero/pkg/db"
	"github.com/soundprediction/pensiero/pkg/models"
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

// IngestFromPredicato orchestrates knowledge extraction from Predicato and storage in Pensiero
func (c *PredicatoClient) IngestFromPredicato(ctx context.Context, repo *db.Repository, text string) error {
	reqBody := ExtractExtendedRequest{
		Text: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/v1/extract", c.BaseURL), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("predicato returned status: %d", resp.StatusCode)
	}

	var result ExtendedExtractionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	// Map triples to EpistemicEdges
	for i, triple := range result.Triples {
		edgeID := fmt.Sprintf("predicato-triple-%d-%d", time.Now().Unix(), i)

		contextObj := models.Context{
			Confidence: triple.Confidence,
			Conditions: []models.Condition{},
			Provenance: &models.Provenance{
				EvidenceID:   edgeID,
				SourceSystem: "predicato",
				Extractor:    "extended-extraction",
				Timestamp:    time.Now(),
			},
		}

		if triple.Condition != "" {
			contextObj.Conditions = append(contextObj.Conditions, models.Condition{Type: "condition", Value: triple.Condition})
		}
		if triple.Temporal != "" {
			contextObj.Conditions = append(contextObj.Conditions, models.Condition{Type: "temporal", Value: triple.Temporal})
		}
		if triple.Location != "" {
			contextObj.Conditions = append(contextObj.Conditions, models.Condition{Type: "location", Value: triple.Location})
		}

		contextJSON, _ := json.Marshal(contextObj)

		edge := &models.EpistemicEdge{
			ID:           edgeID,
			Source:       triple.Subject,
			Target:       triple.Object,
			Predicate:    triple.Predicate,
			RawPredicate: triple.Predicate,
			Status:       models.StatusObservation,
			Confidence:   triple.Confidence,
			Context:      json.RawMessage(contextJSON),
		}

		if err := repo.SaveEdge(edge); err != nil {
			return fmt.Errorf("failed to save edge %s: %w", edgeID, err)
		}
	}

	// Map rules to MetaRelations
	for i, rule := range result.Rules {
		ruleID := fmt.Sprintf("predicato-rule-%d-%d", time.Now().Unix(), i)

		bodyObj := map[string]interface{}{
			"antecedent": rule.Antecedent,
			"consequent": rule.Consequent,
			"exception":  rule.Exception,
			"rule_type":  rule.RuleType,
		}
		bodyJSON, _ := json.Marshal(bodyObj)

		provenanceObj := models.Provenance{
			EvidenceID:   ruleID,
			SourceSystem: "predicato",
			Extractor:    "extended-extraction",
			Timestamp:    time.Now(),
		}
		provenanceJSON, _ := json.Marshal(provenanceObj)

		meta := &models.MetaRelation{
			ID:         ruleID,
			Head:       rule.Consequent,
			Body:       json.RawMessage(bodyJSON),
			Frequency:  1,
			Confidence: rule.Confidence,
			Provenance: json.RawMessage(provenanceJSON),
			CreatedAt:  time.Now(),
		}

		if err := repo.SaveMetaRelation(meta); err != nil {
			return fmt.Errorf("failed to save meta relation %s: %w", ruleID, err)
		}
	}

	return nil
}
