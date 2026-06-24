package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"github.com/soundprediction/predicato/pkg/ruleschema"
)

// runLoadRules writes structured conditional rules (ruleschema.Rule JSONL, e.g.
// produced by humn/modeling/rule_extraction/convert_gold_rules_to_ruleschema.py)
// directly into a ladybug graph as Rule nodes — NOT through any predicato
// ingestion pipeline. Each rule becomes:
//
//	(:Rule {uuid, name, summary, attributes})  with attributes = {"structured_rule": <rule JSON>}
//
// matching what reasoning.LoadRulesFromGraph reads. With --with-facts, the
// condition (and consequent-subject) clauses are also written as reified
// (:Entity)-[:RELATES_TO]->(:RelatesToNode_{name:predicate})-[:RELATES_TO]->(:Entity)
// triples so a ConditionalReasoner over this graph can actually fire the rules.
func runLoadRules(args []string) error {
	fs := flag.NewFlagSet("load-rules", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var graphPath, rulesPath, scope string
	var withFacts bool
	var limit int
	fs.StringVar(&graphPath, "graph", "", "output ladybug graph path (created/extended)")
	fs.StringVar(&rulesPath, "rules", "", "ruleschema.Rule JSONL input")
	fs.StringVar(&scope, "scope", "rules", "group_id/scope for written nodes")
	fs.BoolVar(&withFacts, "with-facts", false, "also write condition clauses as reified facts so rules are fireable")
	fs.IntVar(&limit, "limit", 0, "max rules to load (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if graphPath == "" || rulesPath == "" {
		return fmt.Errorf("--graph and --rules are required")
	}

	rules, err := readRuleSchemaJSONL(rulesPath, limit)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return fmt.Errorf("no rules read from %s", rulesPath)
	}

	gh, err := openLadybugGraph(graphPath, false)
	if err != nil {
		return fmt.Errorf("open graph %s for write: %w", graphPath, err)
	}
	defer gh.Close()

	ctx := context.Background()
	if err := ensureRuleSchema(ctx, gh); err != nil {
		return err
	}

	ents := newEntityCache()
	var wroteRules, wroteFacts, skipped int
	for _, r := range rules {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		if err := ruleschema.Validate(r); err != nil {
			skipped++
			continue
		}
		if err := writeRuleNode(ctx, gh, r, scope); err != nil {
			return fmt.Errorf("write rule %s: %w", r.ID, err)
		}
		wroteRules++
		if withFacts {
			n, err := writeRuleConditionFacts(ctx, gh, r, scope, ents)
			if err != nil {
				return fmt.Errorf("write facts for rule %s: %w", r.ID, err)
			}
			wroteFacts += n
		}
	}

	fmt.Printf("load-rules: graph=%s rules_written=%d facts_written=%d skipped_invalid=%d entities=%d\n",
		graphPath, wroteRules, wroteFacts, skipped, ents.count())

	// Self-check: reopen read-only and confirm LoadRulesFromGraph sees them.
	if err := gh.Close(); err != nil {
		return err
	}
	ro, err := openLadybugGraph(graphPath, true)
	if err != nil {
		return fmt.Errorf("reopen graph read-only: %w", err)
	}
	defer ro.Close()
	loaded, stats, err := reasoning.LoadRulesFromGraph(ctx, ro)
	if err != nil {
		return fmt.Errorf("verify LoadRulesFromGraph: %w", err)
	}
	fmt.Printf("verify: LoadRulesFromGraph loaded=%d scanned=%d skipped_no_structured=%d skipped_invalid=%d\n",
		len(loaded), stats.Scanned, stats.SkippedNoStructured, stats.SkippedInvalid)
	return nil
}

func readRuleSchemaJSONL(path string, limit int) ([]ruleschema.Rule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ruleschema.Rule
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r ruleschema.Rule
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("parse rule line: %w", err)
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, sc.Err()
}

func ensureRuleSchema(ctx context.Context, gh graphHandle) error {
	const dim = generalization.DefaultEmbeddingDim
	ddl := fmt.Sprintf(`
CREATE NODE TABLE IF NOT EXISTS Entity (
    uuid STRING PRIMARY KEY,
    name STRING,
    group_id STRING,
    labels STRING[],
    created_at TIMESTAMP,
    name_embedding FLOAT[%d],
    summary STRING,
    attributes STRING
);
CREATE NODE TABLE IF NOT EXISTS RelatesToNode_ (
    uuid STRING PRIMARY KEY,
    group_id STRING,
    created_at TIMESTAMP,
    name STRING,
    fact STRING,
    fact_embedding FLOAT[%d],
    episodes STRING[],
    attributes STRING,
    confidence DOUBLE,
    support INT64
);
CREATE REL TABLE IF NOT EXISTS RELATES_TO(
    FROM Entity TO RelatesToNode_,
    FROM RelatesToNode_ TO Entity
);
CREATE NODE TABLE IF NOT EXISTS Rule (
    uuid STRING PRIMARY KEY,
    name STRING,
    summary STRING,
    group_id STRING,
    created_at TIMESTAMP,
    attributes STRING
);
`, dim, dim)
	if _, err := gh.Query(ctx, ddl, nil); err != nil {
		return fmt.Errorf("rule schema DDL: %w", err)
	}
	return nil
}

func writeRuleNode(ctx context.Context, gh graphHandle, r ruleschema.Rule, scope string) error {
	ruleJSON, err := json.Marshal(r)
	if err != nil {
		return err
	}
	attrs, err := json.Marshal(map[string]any{"structured_rule": string(ruleJSON)})
	if err != nil {
		return err
	}
	human := strings.TrimSpace(r.Provenance.SourceAttribution)
	if human == "" {
		human = r.ID
	}
	_, err = gh.Query(ctx, `
CREATE (n:Rule {
    uuid: $uuid,
    name: $name,
    summary: $summary,
    group_id: $scope,
    created_at: $created_at,
    attributes: $attributes
})`, map[string]any{
		"uuid":       r.ID,
		"name":       truncate(human, 100),
		"summary":    human,
		"scope":      scope,
		"created_at": time.Now(),
		"attributes": string(attrs),
	})
	return err
}

// writeRuleConditionFacts writes each CONDITION pattern as a reified triple so a
// ConditionalReasoner can satisfy the rule's antecedent. Exceptions are NOT
// written — an exception ("unless ...") asserted as true would veto its own rule.
func writeRuleConditionFacts(ctx context.Context, gh graphHandle, r ruleschema.Rule, scope string, ents *entityCache) (int, error) {
	patterns := append([]ruleschema.Pattern{}, r.Conditions...)
	n := 0
	for i, p := range patterns {
		subj := termName(p.Subject)
		obj := termName(p.Object)
		pred := strings.TrimSpace(p.Predicate)
		if subj == "" || obj == "" || pred == "" {
			continue
		}
		sUUID, err := ents.ensure(ctx, gh, subj, scope)
		if err != nil {
			return n, err
		}
		oUUID, err := ents.ensure(ctx, gh, obj, scope)
		if err != nil {
			return n, err
		}
		relUUID := "rel:" + r.ID + ":" + fmt.Sprint(i)
		if _, err := gh.Query(ctx, `
CREATE (rel:RelatesToNode_ {
    uuid: $uuid, group_id: $scope, created_at: $created_at,
    name: $name, fact: $fact, confidence: 1.0, support: 1
})`, map[string]any{
			"uuid": relUUID, "scope": scope, "created_at": time.Now(),
			"name": pred, "fact": subj + " " + pred + " " + obj,
		}); err != nil {
			return n, err
		}
		if _, err := gh.Query(ctx, `
MATCH (s:Entity {uuid: $s}), (rel:RelatesToNode_ {uuid: $rel})
CREATE (s)-[:RELATES_TO]->(rel)`, map[string]any{"s": sUUID, "rel": relUUID}); err != nil {
			return n, err
		}
		if _, err := gh.Query(ctx, `
MATCH (rel:RelatesToNode_ {uuid: $rel}), (o:Entity {uuid: $o})
CREATE (rel)-[:RELATES_TO]->(o)`, map[string]any{"rel": relUUID, "o": oUUID}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

type entityCache struct{ seen map[string]string }

func newEntityCache() *entityCache { return &entityCache{seen: map[string]string{}} }

func (c *entityCache) count() int { return len(c.seen) }

func (c *entityCache) ensure(ctx context.Context, gh graphHandle, name, scope string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if uuid, ok := c.seen[key]; ok {
		return uuid, nil
	}
	sum := sha1.Sum([]byte(key))
	uuid := "ent:" + hex.EncodeToString(sum[:])
	if _, err := gh.Query(ctx, `
CREATE (n:Entity {
    uuid: $uuid, name: $name, group_id: $scope, labels: $labels,
    created_at: $created_at, summary: $summary, attributes: $attributes
})`, map[string]any{
		"uuid": uuid, "name": name, "scope": scope, "labels": []string{scope},
		"created_at": time.Now(), "summary": "", "attributes": "{}",
	}); err != nil {
		return "", err
	}
	c.seen[key] = uuid
	return uuid, nil
}

func termName(t ruleschema.Term) string {
	if s := strings.TrimSpace(t.Entity); s != "" {
		return s
	}
	return strings.TrimSpace(t.Var)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
