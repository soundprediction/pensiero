package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type graphHandle interface {
	reasoning.GraphQuerier
	Close() error
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "build-generalization":
		return runBuildGeneralization(args[1:])
	case "serve":
		return runServe(args[1:])
	case "-h", "--help", "help":
		return usageError()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runBuildGeneralization(args []string) error {
	fs := flag.NewFlagSet("build-generalization", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var sourcePath, scope, scopeFile, outPath, backupPath, predicateCSV, predicatePacksCSV, typePacksCSV, taxonomicCSV, taxonomicDirection, registrySpec string
	var minSupport, maxParentLevel int
	fs.StringVar(&sourcePath, "source", "", "source graph path")
	fs.StringVar(&scope, "scope", "", "scope name")
	fs.StringVar(&scopeFile, "scope-entities", "", "file with scope entity ids or names")
	fs.StringVar(&outPath, "out", "", "output graph path")
	fs.StringVar(&backupPath, "backup-db", "", "backup graph path for dropped edges; empty disables")
	fs.IntVar(&minSupport, "min-support", generalization.DefaultMinSupport, "minimum child support for lifted relations")
	fs.IntVar(&maxParentLevel, "max-parent-level", generalization.DefaultMaxParentLevel, "maximum parent depth to keep")
	fs.StringVar(&predicateCSV, "predicates", "", "comma-separated predicates; empty uses registry-derived predicates")
	fs.StringVar(&predicatePacksCSV, "predicate-packs", "", "comma-separated predicate packs to extend the general registry")
	fs.StringVar(&typePacksCSV, "type-packs", "", "comma-separated entity type packs for advisory registry validation")
	fs.StringVar(&taxonomicCSV, "taxonomic-predicates", "", "comma-separated hierarchy predicates; empty uses registry-derived predicates")
	fs.StringVar(&taxonomicDirection, "taxonomic-direction", string(generalization.TaxonomicDirectionChildToParent), "hierarchy edge direction: child-to-parent or parent-to-child")
	fs.StringVar(&registrySpec, "registry", "general", "general or path to a registry JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if sourcePath == "" {
		return fmt.Errorf("--source is required")
	}
	if outPath == "" {
		return fmt.Errorf("--out is required")
	}
	scopeEntities, err := readScopeEntities(scopeFile)
	if err != nil {
		return err
	}
	if scope == "" && len(scopeEntities) == 0 {
		return fmt.Errorf("--scope or --scope-entities is required")
	}
	direction, err := generalization.ParseTaxonomicDirection(taxonomicDirection)
	if err != nil {
		return err
	}
	reg, _, err := loadRegistryWithTypePacks(registrySpec, splitCSV(predicatePacksCSV), splitCSV(typePacksCSV))
	if err != nil {
		return err
	}

	ctx := context.Background()
	source, err := openLadybugGraph(sourcePath, true)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()

	cfg := generalization.Config{
		Scope:               scope,
		ScopeEntities:       scopeEntities,
		Predicates:          splitCSV(predicateCSV),
		TaxonomicPredicates: splitCSV(taxonomicCSV),
		TaxonomicDirection:  direction,
		MaxParentLevel:      maxParentLevel,
		MinSupport:          minSupport,
	}
	graph, err := generalization.Build(ctx, source, reg, cfg)
	if err != nil {
		return err
	}

	tmpPath := tempOutputPath(outPath)
	if err := os.RemoveAll(tmpPath); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tmpPath)
		}
	}()
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("output already exists: %s", outPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	target, err := openLadybugGraph(tmpPath, false)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	emitter := generalization.NewCypherEmitter(target, scope)
	// Emit a fixed-size FLOAT[N] embedding column matching the SOURCE graph's width
	// (whatever the graph DB uses) so the emitted subgraph is vector-indexable, rather
	// than assuming a dimension. Falls back to the emitter default when the source has
	// no embeddings.
	if dim := detectEmbeddingDim(ctx, source); dim > 0 {
		emitter = emitter.WithEmbeddingDim(dim)
	}
	emitErr := emitter.Emit(ctx, graph)
	closeErr := target.Close()
	if emitErr != nil {
		return emitErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	published = true
	printStats(graph)
	backupPath = strings.TrimSpace(backupPath)
	if backupPath != "" {
		backupDroppedEdges(ctx, source, reg, cfg, graph, backupPath)
	}
	return nil
}

func usageError() error {
	return fmt.Errorf("usage: pensiero build-generalization --source <graph.ladybug> --scope <name> --out <scope.g_g.ladybug> [--backup-db <dropped.ladybug>] [--scope-entities <file>] [--min-support k] [--max-parent-level n] [--predicates list] [--predicate-packs list] [--type-packs list] [--taxonomic-predicates list] [--taxonomic-direction child-to-parent|parent-to-child] [--registry general|path]\n       pensiero serve --source <graph.ladybug> (--scopes <name[,name]> | --scopes-dir <dir>) --out-dir <dir> [--interval 1m] [--igl-quiet 3s] [--igl-min-publish 30s] [--leader-mode flock|none|k8s-lease] [--health-addr addr] [--grpc-addr addr] [--grpc-pool-size n] [--backend ladybug-native|symbolic-graph] [--reasoning-extension path] [--golden-file file] [--predicate-packs list] [--type-packs list] [--inventory-sample n]\n       pensiero serve --source-dir <dir> --grpc-addr <addr> [--default-topic topic] [--max-open-topics n] [--health-addr addr]\n       pensiero serve --source <graph.ladybug> (--scopes <name[,name]> | --scopes-dir <dir>) --out-dir <dir> --once")
}

func backupDroppedEdges(ctx context.Context, source reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg generalization.Config, graph *generalization.Graph, backupPath string) {
	dropped, err := generalization.DroppedRelations(ctx, source, reg, cfg, graph)
	if err != nil {
		warnDroppedEdgeBackup("compute dropped edges: %v", err)
		return
	}
	if len(dropped) == 0 {
		fmt.Fprintf(os.Stderr, "backed up 0 dropped edges to %s\n", backupPath)
		return
	}
	target, err := openLadybugGraph(backupPath, false)
	if err != nil {
		warnDroppedEdgeBackup("open %s: %v", backupPath, err)
		return
	}
	recordErr := generalization.NewCypherDroppedEdgeBackup(target).Record(ctx, cfg.Scope, dropped)
	closeErr := target.Close()
	if recordErr != nil {
		warnDroppedEdgeBackup("record to %s: %v", backupPath, recordErr)
		return
	}
	if closeErr != nil {
		warnDroppedEdgeBackup("close %s: %v", backupPath, closeErr)
		return
	}
	fmt.Fprintf(os.Stderr, "backed up %d dropped edges to %s\n", len(dropped), backupPath)
}

func warnDroppedEdgeBackup(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: dropped-edge backup: "+format+"\n", args...)
}

func readScopeEntities(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				set[part] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	return out, nil
}

// detectEmbeddingDim returns the source graph's name_embedding width (the dimension
// the graph DB actually uses), so the emitted subgraph declares a matching fixed-size
// FLOAT[N] column. Returns 0 when the source carries no embeddings.
func detectEmbeddingDim(ctx context.Context, source reasoning.GraphQuerier) int {
	rows, err := source.Query(ctx, "MATCH (n:Entity) WHERE size(n.name_embedding) > 0 RETURN size(n.name_embedding) AS d LIMIT 1", nil)
	if err != nil || len(rows) == 0 {
		return 0
	}
	switch v := rows[0]["d"].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case int32:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func tempOutputPath(outPath string) string {
	dir := filepath.Dir(outPath)
	base := filepath.Base(outPath)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	return filepath.Join(dir, "."+base+".tmp."+suffix)
}

func printStats(g *generalization.Graph) {
	fmt.Fprintf(os.Stderr, "scope=%s nodes=%d relations=%d scope_entities=%d concepts=%d endpoints=%d direct=%d lifted=%d\n",
		g.Scope,
		g.Stats.NodeCount,
		g.Stats.RelationCount,
		g.Stats.ScopeEntityCount,
		g.Stats.ConceptCount,
		g.Stats.EndpointCount,
		g.Stats.DirectRelationCount,
		g.Stats.LiftedRelationCount,
	)
}
