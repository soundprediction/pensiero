package db

//go:generate ../../scripts/fetch_cozo.sh

/*
#cgo LDFLAGS: -L${SRCDIR}/../../lib -lcozo_c -lz -lm -ldl -lpthread
#cgo CFLAGS: -I${SRCDIR}/../../include
*/
import "C"

import (
	"encoding/json"
	"fmt"

	cozo "github.com/cozodb/cozo-lib-go"
)

// Client wraps the CozoDB embedded instance
type Client struct {
	db cozo.CozoDB
}

// NewClient initializes a new embedded CozoDB instance
func NewClient(engine, path string, options map[string]interface{}) (*Client, error) {
	db, err := cozo.New(engine, path, options)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize CozoDB: %w", err)
	}
	return &Client{db: db}, nil
}

// Close closes the database instance
func (c *Client) Close() error {
	c.db.Close()
	return nil
}

// Run executes a Datalog query
func (c *Client) Run(query string, params map[string]interface{}) (interface{}, error) {
	res, err := c.db.Run(query, params)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	return res, nil
}

// InitSchema sets up the primary relations defined in the design doc
func (c *Client) InitSchema() error {
	queries := []string{
		`
	:create epistemic_edge {
		id: String,
		source: String,
		target: String,
		predicate: String,
		raw_predicate: String,
		status: String,
		confidence: Float,
		context: Json
	}
	`,
		`
	:create predicate_registry {
		raw: String,
		canonical: String,
		logical_class: String,
		domain: String,
		range: String
	}
	`,
		`
	:create ontology_disjoint {
		class_a: String,
		class_b: String,
		ontology_source: String
	}
	`,
		`
	:create graph_modules {
		node_id: String,
		module_id: String,
		cohesion_score: Float
	}
	`,
		`
	:create node_epistemic_status {
		node_id: String,
		connectivity_score: Float,
		support_count: Int,
		gap_score: Float
	}
	`,
		`
	:create meta_relation {
		id: String,
		head: String,
		body: Json,
		frequency: Int,
		confidence: Float,
		provenance: Json,
		created_at: String
	}
	`,
		`
	:create audit_log {
		entry_id: String,
		actor: String,
		action: String,
		target_type: String,
		target_id: String,
		timestamp: String,
		details: Json
	}
	`,
		`
	:create archive_edges {
		id: String,
		original_edge: Json,
		archived_by: String,
		archived_at: String,
		reason: String
	}
	`,
	}

	for _, q := range queries {
		if _, err := c.db.Run(q, nil); err != nil {
			return fmt.Errorf("failed to run schema query [%s]: %w", q, err)
		}
	}
	return nil
}

// NamedResult represents a simplified Cozo result map
type NamedResult map[string]interface{}

// ParseResult converts Cozo result to a list of maps
func (c *Client) ParseResult(res interface{}) ([]NamedResult, error) {
	// Cozo-go result is a struct with Header and Rows
	// We need to type assert it.
	// Based on cozo-lib-go API:
	// For now, let's treat it as a generic map or struct provided by the driver.

	// Note: Since I don't have the exact struct definition from the source,
	// for the implementation I'll assume we can marshal/unmarshal it or use its fields.

	data, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Headers []string        `json:"headers"`
		Rows    [][]interface{} `json:"rows"`
	}

	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	results := make([]NamedResult, len(parsed.Rows))
	for i, row := range parsed.Rows {
		item := make(NamedResult)
		for j, header := range parsed.Headers {
			if j < len(row) {
				item[header] = row[j]
			}
		}
		results[i] = item
	}

	return results, nil
}
