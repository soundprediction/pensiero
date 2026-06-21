package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

type serveOptions struct {
	SourcePath       string
	ScopesCSV        string
	ScopesDir        string
	OutDir           string
	PredicateCSV     string
	TaxonomicCSV     string
	TaxonomicDir     string
	RegistrySpec     string
	HealthAddr       string
	Interval         time.Duration
	MinSupport       int
	MinParentSupport int
	MaxParentLevel   int
	Once             bool
}

type scopeDescriptor struct {
	Name                string   `json:"name"`
	Scope               string   `json:"scope"`
	ScopeEntities       []string `json:"scope_entities"`
	ScopeEntitiesFile   string   `json:"scope_entities_file"`
	Predicates          []string `json:"predicates"`
	TaxonomicPredicates []string `json:"taxonomic_predicates"`
	TaxonomicDirection  string   `json:"taxonomic_direction"`
	MinSupport          int      `json:"min_support"`
	MinParentSupport    int      `json:"min_parent_support"`
	MaxParentLevel      int      `json:"max_parent_level"`
}

func runServe(args []string) error {
	opts := defaultServeOptions()
	fs := flagSet("serve")
	fs.StringVar(&opts.SourcePath, "source", opts.SourcePath, "source graph path")
	fs.StringVar(&opts.ScopesCSV, "scopes", opts.ScopesCSV, "comma-separated scope names")
	fs.StringVar(&opts.ScopesDir, "scopes-dir", opts.ScopesDir, "directory of JSON scope descriptors")
	fs.StringVar(&opts.OutDir, "out-dir", opts.OutDir, "published graph directory")
	fs.DurationVar(&opts.Interval, "interval", opts.Interval, "IGL period")
	fs.IntVar(&opts.MinSupport, "min-support", opts.MinSupport, "minimum child support for lifted relations")
	fs.IntVar(&opts.MinParentSupport, "min-parent-support", opts.MinParentSupport, "minimum child support for selected parent nodes")
	fs.IntVar(&opts.MaxParentLevel, "max-parent-level", opts.MaxParentLevel, "maximum parent depth to keep")
	fs.StringVar(&opts.PredicateCSV, "predicates", opts.PredicateCSV, "comma-separated predicates; empty uses registry-derived predicates")
	fs.StringVar(&opts.TaxonomicCSV, "taxonomic-predicates", opts.TaxonomicCSV, "comma-separated hierarchy predicates")
	fs.StringVar(&opts.TaxonomicDir, "taxonomic-direction", opts.TaxonomicDir, "hierarchy edge direction: child-to-parent or parent-to-child")
	fs.StringVar(&opts.RegistrySpec, "registry", opts.RegistrySpec, "general or path to a registry JSON file")
	fs.StringVar(&opts.HealthAddr, "health-addr", opts.HealthAddr, "health/metrics listen address; empty disables HTTP")
	fs.BoolVar(&opts.Once, "once", opts.Once, "run one IGL pass and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(opts.SourcePath) == "" {
		return fmt.Errorf("--source is required")
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		return fmt.Errorf("--out-dir is required")
	}
	scopes, err := loadServeScopes(opts)
	if err != nil {
		return err
	}
	reg, err := loadRegistry(opts.RegistrySpec)
	if err != nil {
		return err
	}
	source, err := openLadybugGraph(opts.SourcePath, true)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	metrics := generalization.NewMetrics()
	loop := &generalization.Loop{
		Publisher: &generalization.Publisher{
			Source:   source,
			Registry: reg,
			Writer:   ladybugSnapshotWriter{},
			Validate: validateLadybugSnapshot,
		},
		Metrics:  metrics,
		Logger:   logger,
		Scopes:   scopes,
		Interval: opts.Interval,
		OutDir:   opts.OutDir,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.Once {
		result, err := loop.RunOnce(ctx)
		printPassResult(result)
		return err
	}
	if strings.TrimSpace(opts.HealthAddr) != "" {
		if _, err := startHealthServer(ctx, opts.HealthAddr, metrics, logger); err != nil {
			return err
		}
	}
	return loop.Run(ctx)
}

func defaultServeOptions() serveOptions {
	return serveOptions{
		SourcePath:       os.Getenv("PENSIERO_SOURCE"),
		ScopesCSV:        os.Getenv("PENSIERO_SCOPES"),
		ScopesDir:        os.Getenv("PENSIERO_SCOPES_DIR"),
		OutDir:           os.Getenv("PENSIERO_OUT_DIR"),
		PredicateCSV:     os.Getenv("PENSIERO_PREDICATES"),
		TaxonomicCSV:     os.Getenv("PENSIERO_TAXONOMIC_PREDICATES"),
		TaxonomicDir:     firstEnv("PENSIERO_TAXONOMIC_DIRECTION", string(generalization.TaxonomicDirectionChildToParent)),
		RegistrySpec:     firstEnv("PENSIERO_REGISTRY", "general"),
		HealthAddr:       firstEnv("PENSIERO_HEALTH_ADDR", "127.0.0.1:8080"),
		Interval:         envDuration("PENSIERO_INTERVAL", time.Minute),
		MinSupport:       envInt("PENSIERO_MIN_SUPPORT", generalization.DefaultMinSupport),
		MinParentSupport: envInt("PENSIERO_MIN_PARENT_SUPPORT", 1),
		MaxParentLevel:   envInt("PENSIERO_MAX_PARENT_LEVEL", generalization.DefaultMaxParentLevel),
	}
}

func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

type ladybugSnapshotWriter struct{}

func (ladybugSnapshotWriter) Write(ctx context.Context, path string, scope string, graph *generalization.Graph) error {
	target, err := openLadybugGraph(path, false)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	emitter := generalization.NewCypherEmitter(target, scope)
	emitErr := emitter.Emit(ctx, graph)
	closeErr := target.Close()
	if emitErr != nil {
		return emitErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func validateLadybugSnapshot(ctx context.Context, path string, graph *generalization.Graph) error {
	if graph == nil || graph.Stats.NodeCount == 0 {
		return fmt.Errorf("empty snapshot")
	}
	target, err := openLadybugGraph(path, true)
	if err != nil {
		return fmt.Errorf("open snapshot read-only: %w", err)
	}
	defer target.Close()
	rows, err := target.Query(ctx, `MATCH (n:Entity) RETURN count(n) AS count`, nil)
	if err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	if len(rows) == 0 || countValue(rows[0]["count"]) == 0 {
		return fmt.Errorf("empty snapshot")
	}
	return nil
}

func loadServeScopes(opts serveOptions) ([]generalization.Scope, error) {
	direction, err := generalization.ParseTaxonomicDirection(opts.TaxonomicDir)
	if err != nil {
		return nil, err
	}
	base := generalization.Config{
		Predicates:          splitCSV(opts.PredicateCSV),
		TaxonomicPredicates: splitCSV(opts.TaxonomicCSV),
		TaxonomicDirection:  direction,
		MaxParentLevel:      opts.MaxParentLevel,
		MinParentSupport:    opts.MinParentSupport,
		MinSupport:          opts.MinSupport,
	}
	var scopes []generalization.Scope
	for _, name := range splitCSV(opts.ScopesCSV) {
		cfg := cloneConfig(base)
		cfg.Scope = name
		scopes = append(scopes, generalization.Scope{Name: name, Config: cfg})
	}
	if strings.TrimSpace(opts.ScopesDir) != "" {
		loaded, err := loadScopeDescriptors(opts.ScopesDir, base)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, loaded...)
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("--scopes or --scopes-dir is required")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		name := strings.TrimSpace(scope.Name)
		if name == "" {
			name = strings.TrimSpace(scope.Config.Scope)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate scope %q", name)
		}
		seen[name] = true
	}
	return scopes, nil
}

func loadScopeDescriptors(dir string, base generalization.Config) ([]generalization.Scope, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	scopes := make([]generalization.Scope, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var desc scopeDescriptor
		if err := json.Unmarshal(data, &desc); err != nil {
			return nil, fmt.Errorf("scope descriptor %s: %w", path, err)
		}
		scope, err := scopeFromDescriptor(dir, entry.Name(), desc, base)
		if err != nil {
			return nil, fmt.Errorf("scope descriptor %s: %w", path, err)
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

func scopeFromDescriptor(dir string, fileName string, desc scopeDescriptor, base generalization.Config) (generalization.Scope, error) {
	name := strings.TrimSpace(desc.Name)
	if name == "" {
		name = strings.TrimSpace(desc.Scope)
	}
	if name == "" {
		name = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}
	cfg := cloneConfig(base)
	cfg.Scope = firstNonEmpty(desc.Scope, name)
	if desc.Predicates != nil {
		cfg.Predicates = append([]string{}, desc.Predicates...)
	}
	if desc.TaxonomicPredicates != nil {
		cfg.TaxonomicPredicates = append([]string{}, desc.TaxonomicPredicates...)
	}
	if strings.TrimSpace(desc.TaxonomicDirection) != "" {
		direction, err := generalization.ParseTaxonomicDirection(desc.TaxonomicDirection)
		if err != nil {
			return generalization.Scope{}, err
		}
		cfg.TaxonomicDirection = direction
	}
	if desc.MinSupport > 0 {
		cfg.MinSupport = desc.MinSupport
	}
	if desc.MinParentSupport > 0 {
		cfg.MinParentSupport = desc.MinParentSupport
	}
	if desc.MaxParentLevel > 0 {
		cfg.MaxParentLevel = desc.MaxParentLevel
	}
	cfg.ScopeEntities = append([]string{}, desc.ScopeEntities...)
	if strings.TrimSpace(desc.ScopeEntitiesFile) != "" {
		path := desc.ScopeEntitiesFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		entities, err := readScopeEntities(path)
		if err != nil {
			return generalization.Scope{}, err
		}
		cfg.ScopeEntities = mergeStrings(cfg.ScopeEntities, entities)
	}
	return generalization.Scope{Name: name, Config: cfg}, nil
}

func startHealthServer(ctx context.Context, addr string, metrics *generalization.Metrics, logger *log.Logger) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		snapshot := metrics.Snapshot()
		lastErr := lastMetricsError(snapshot)
		status := "ok"
		code := http.StatusOK
		if lastErr != "" {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status":     status,
			"last_error": lastErr,
			"passes":     snapshot.Passes,
			"scopes":     snapshot.Scopes,
		})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, metrics.Snapshot())
	})
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("health listen: %w", err)
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		logger.Printf("health addr=%s", listener.Addr().String())
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Printf("health error=%v", err)
		}
	}()
	return server, nil
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func printPassResult(result generalization.PassResult) {
	for _, scope := range result.Scopes {
		if scope.LastError != "" {
			fmt.Fprintf(os.Stderr, "scope=%s error=%s duration=%s\n", scope.Scope, scope.LastError, scope.Duration)
			continue
		}
		fmt.Fprintf(os.Stderr, "scope=%s nodes=%d relations=%d delta_nodes=%+d delta_relations=%+d duration=%s path=%s\n",
			scope.Scope,
			scope.Stats.NodeCount,
			scope.Stats.RelationCount,
			scope.Delta.Nodes,
			scope.Delta.Relations,
			scope.Duration,
			scope.Path,
		)
	}
}

func lastMetricsError(snapshot generalization.MetricsSnapshot) string {
	if snapshot.LastPass.LastError != "" {
		return snapshot.LastPass.LastError
	}
	for _, scope := range snapshot.Scopes {
		if scope.LastError != "" {
			return scope.LastError
		}
	}
	return ""
}

func cloneConfig(cfg generalization.Config) generalization.Config {
	out := cfg
	out.ScopeEntities = append([]string{}, cfg.ScopeEntities...)
	out.TaxonomicPredicates = append([]string{}, cfg.TaxonomicPredicates...)
	out.Predicates = append([]string{}, cfg.Predicates...)
	return out
}

func mergeStrings(a []string, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, value := range append(a, b...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstEnv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func countValue(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		if uint64(v) > uint64(1<<63-1) {
			return 0
		}
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		if v > uint64(1<<63-1) {
			return 0
		}
		return int64(v)
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}
