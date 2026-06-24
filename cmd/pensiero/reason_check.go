package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

// runReasonCheck loads conditional rules from a graph and runs a single claim
// through the exact production reasoning stack the serving daemon uses
// (symbolic-graph base reasoner over the graph → GraphConditionOracle →
// ConditionalReasoner), printing the verdict and proof. It proves a graph
// populated by `load-rules` actually fires rules end-to-end — no daemon, no gRPC.
func runReasonCheck(args []string) error {
	fs := flag.NewFlagSet("reason-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var graphPath, claimSpec, backend, reasoningExt, assumeSpec string
	fs.StringVar(&graphPath, "graph", "", "ladybug graph path (read-only)")
	fs.StringVar(&claimSpec, "claim", "", `claim to test as "subject|predicate|object"`)
	fs.StringVar(&backend, "backend", reasoning.NativeBackendName, "ladybug-native or symbolic-graph")
	fs.StringVar(&reasoningExt, "reasoning-extension", "reasoning", "reasoning extension path/name (ladybug-native only)")
	fs.StringVar(&assumeSpec, "assume", "", `per-request assumed facts, comma-separated "s|p|o" (e.g. patient context)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if graphPath == "" || claimSpec == "" {
		return fmt.Errorf(`--graph and --claim ("subject|predicate|object") are required`)
	}
	parts := strings.SplitN(claimSpec, "|", 3)
	if len(parts) != 3 {
		return fmt.Errorf("--claim must be subject|predicate|object")
	}
	claim := reasoning.Claim{
		Subject:   strings.TrimSpace(parts[0]),
		Predicate: strings.TrimSpace(parts[1]),
		Object:    strings.TrimSpace(parts[2]),
	}

	gh, err := openLadybugGraph(graphPath, true)
	if err != nil {
		return fmt.Errorf("open graph %s: %w", graphPath, err)
	}
	defer gh.Close()

	ctx := context.Background()

	// ladybug-native runs the reasoning algorithm via a C extension loaded into the
	// connection (the symbolic-graph backend emits Cypher ladybug can't parse), so
	// load it before constructing the backend — exactly as the serving daemon does.
	if backend == reasoning.NativeBackendName {
		if err := reasoningExtensionInitializer(reasoningExt)(ctx, gh); err != nil {
			return err
		}
	}

	reg, _, err := loadRegistryWithTypePacks("general", nil, nil)
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	cfg := serveReasoningConfig()

	base, err := reasoning.New(backend, gh, reg, cfg)
	if err != nil {
		return fmt.Errorf("create base reasoner: %w", err)
	}
	loaded, stats, err := reasoning.LoadRulesFromGraph(ctx, gh)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	ruleSet, err := reasoning.CompileRules(loaded, reg)
	if err != nil {
		return fmt.Errorf("compile rules: %w", err)
	}
	fmt.Printf("rules: loaded=%d compiled=%d (skipped_invalid=%d)\n", len(loaded), ruleSet.Len(), ruleSet.SkippedInvalid)

	reasoner := reasoning.Reasoner(base)
	if ruleSet.Len() > 0 {
		oracle := reasoning.NewAssumedFactsOracle(reasoning.NewGraphConditionOracle(gh, base, reg, cfg), reg)
		reasoner = reasoning.NewConditionalReasoner(base, oracle, ruleSet, reg, reasoning.ConditionalConfig{Decay: cfg.Decay})
	}
	_ = stats

	if assumeSpec != "" {
		var facts []reasoning.Claim
		for _, spec := range strings.Split(assumeSpec, ",") {
			p := strings.SplitN(strings.TrimSpace(spec), "|", 3)
			if len(p) == 3 {
				facts = append(facts, reasoning.Claim{Subject: strings.TrimSpace(p[0]), Predicate: strings.TrimSpace(p[1]), Object: strings.TrimSpace(p[2])})
			}
		}
		ctx = reasoning.WithAssumedFacts(ctx, facts)
		fmt.Printf("assumed facts: %d\n", len(facts))
	}

	result, err := reasoner.Entails(ctx, claim)
	if err != nil {
		return fmt.Errorf("entails: %w", err)
	}
	fmt.Printf("claim: %s -%s-> %s\n", claim.Subject, claim.Predicate, claim.Object)
	fmt.Printf("verdict: %s  confidence=%.3f\n", result.Verdict, result.Confidence)
	if result.Best != nil {
		fmt.Printf("proof: class=%s hops=%d steps=%d\n", result.Best.RuleClass, result.Best.Hops, len(result.Best.Steps))
		for i, s := range result.Best.Steps {
			fmt.Printf("  [%d] %s  (%s)  %s -%s-> %s\n", i, s.Rule, s.EdgeID, s.Source, s.Predicate, s.Target)
		}
	}
	return nil
}
