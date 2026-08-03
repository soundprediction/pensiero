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
	var graphPath, claimSpec, backend, reasoningExt, assumeSpec, rulesGraph string
	var predicatePacks, typePacks, registrySpec, samplePredicate string
	var sampleN int
	fs.StringVar(&graphPath, "graph", "", "ladybug graph path (read-only)")
	fs.StringVar(&rulesGraph, "rules-graph", "", "optional shared rules graph applied on top of the topic graph (route-independent rules)")
	fs.StringVar(&claimSpec, "claim", "", `claim to test as "subject|predicate|object"`)
	fs.StringVar(&backend, "backend", reasoning.NativeBackendName, "ladybug-native or symbolic-graph")
	fs.StringVar(&reasoningExt, "reasoning-extension", "reasoning", "reasoning extension path/name (ladybug-native only)")
	fs.StringVar(&assumeSpec, "assume", "", `per-request assumed facts, comma-separated "s|p|o" (e.g. patient context)`)
	// The registry MUST be configurable here. This tool hardcoded "general", while
	// the serving daemon is normally run with --predicate-packs medical, so every
	// clinical predicate resolved as undeclared and every claim came back
	// "unsupported" — a diagnostic that could not reproduce production, and whose
	// confident negatives were worse than no answer at all.
	fs.StringVar(&registrySpec, "registry", "general", "general or path to a registry JSON file")
	fs.StringVar(&predicatePacks, "predicate-packs", "", "comma-separated predicate packs to extend the general registry (e.g. medical)")
	fs.StringVar(&typePacks, "type-packs", "", "comma-separated type packs")
	// Sampling makes the graph's ACTUAL contents inspectable. Without it there is
	// no way to know whether a claim failed because of naming, or because the graph
	// simply has no such edge — and entity names in these graphs are specific and
	// inconsistently cased ("Autoimmune hypothyroidism", lowercase "acromegaly").
	fs.StringVar(&samplePredicate, "sample-predicate", "", "instead of testing a claim, print real (subject, predicate, object) triples for this predicate")
	fs.IntVar(&sampleN, "sample-n", 10, "how many triples to print with --sample-predicate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if graphPath == "" {
		return fmt.Errorf("--graph is required")
	}
	if claimSpec == "" && samplePredicate == "" {
		return fmt.Errorf(`--claim ("subject|predicate|object") or --sample-predicate is required`)
	}
	var claim reasoning.Claim
	if claimSpec != "" {
		parts := strings.SplitN(claimSpec, "|", 3)
		if len(parts) != 3 {
			return fmt.Errorf("--claim must be subject|predicate|object")
		}
		claim = reasoning.Claim{
			Subject:   strings.TrimSpace(parts[0]),
			Predicate: strings.TrimSpace(parts[1]),
			Object:    strings.TrimSpace(parts[2]),
		}
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

	reg, _, err := loadRegistryWithTypePacks(registrySpec, splitCSV(predicatePacks), splitCSV(typePacks))
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	// Sampling mode: dump real triples and stop. Answers "does this graph even
	// contain the edges we are asking about, and under what exact entity names?"
	// — the question that has to be settled before any name-resolution work.
	if samplePredicate != "" {
		rows, qerr := gh.Query(ctx,
			"MATCH (s)-[r]->(o) WHERE r.name = $p RETURN s.name AS s, r.name AS p, o.name AS o LIMIT $n",
			map[string]any{"p": samplePredicate, "n": int64(sampleN)})
		if qerr != nil {
			return fmt.Errorf("sample %s: %w", samplePredicate, qerr)
		}
		fmt.Printf("sample predicate=%s rows=%d\n", samplePredicate, len(rows))
		for _, r := range rows {
			fmt.Printf("  %v | %v | %v\n", r["s"], r["p"], r["o"])
		}
		return nil
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
	if rulesGraph != "" {
		rg, rerr := openLadybugGraph(rulesGraph, true)
		if rerr != nil {
			return fmt.Errorf("open rules-graph: %w", rerr)
		}
		shared, sstats, serr := reasoning.LoadRulesFromGraph(ctx, rg)
		_ = rg.Close()
		if serr != nil {
			return fmt.Errorf("load shared rules: %w", serr)
		}
		fmt.Printf("shared rules loaded=%d from %s\n", sstats.Loaded, rulesGraph)
		loaded = append(loaded, shared...)
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
