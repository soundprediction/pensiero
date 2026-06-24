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

	"github.com/soundprediction/pensiero/pkg/connector"
	"github.com/soundprediction/pensiero/pkg/generalization"
	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type serveOptions struct {
	SourcePath          string
	SourceDir           string
	ScopesCSV           string
	ScopesDir           string
	OutDir              string
	PredicateCSV        string
	PredicatePacksCSV   string
	TypePacksCSV        string
	TaxonomicCSV        string
	TaxonomicDir        string
	RegistrySpec        string
	HealthAddr          string
	GRPCAddr            string
	Backend             string
	ReasoningExt        string
	EmbedderURL         string
	EmbedderModel       string
	GoldenFile          string
	LeaderMode          string
	Interval            time.Duration
	IGLQuiet            time.Duration
	IGLMinPublish       time.Duration
	CognitionInterval   time.Duration
	CognitionWindow     time.Duration
	CognitionThought    time.Duration
	MinSupport          int
	MinParentSupport    int
	MaxParentLevel      int
	GRPCPoolSize        int
	InventorySample     int
	CognitionMax        int
	QueryHotWeight      int
	RandomWeight        int
	UnresolvedWeight    int
	SemanticWeight      int
	BridgeWeight        int
	RandomSampleLimit   int
	SemanticSample      int
	MaxOpenTopics       int
	Once                bool
	ShowCognitionLabels bool
	DefaultTopic        string
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
	fs.StringVar(&opts.SourceDir, "source-dir", opts.SourceDir, "directory of per-topic serving graphs")
	fs.StringVar(&opts.ScopesCSV, "scopes", opts.ScopesCSV, "comma-separated scope names")
	fs.StringVar(&opts.ScopesDir, "scopes-dir", opts.ScopesDir, "directory of JSON scope descriptors")
	fs.StringVar(&opts.OutDir, "out-dir", opts.OutDir, "published graph directory")
	fs.DurationVar(&opts.Interval, "interval", opts.Interval, "IGL period")
	fs.DurationVar(&opts.IGLQuiet, "igl-quiet", opts.IGLQuiet, "minimum query-idle time before an IGL pass")
	fs.DurationVar(&opts.IGLMinPublish, "igl-min-publish", opts.IGLMinPublish, "minimum interval between successful IGL publishes")
	fs.DurationVar(&opts.CognitionInterval, "cognition-interval", opts.CognitionInterval, "background cognition scheduler period")
	fs.DurationVar(&opts.CognitionWindow, "cognition-window", opts.CognitionWindow, "maximum background cognition work per idle window")
	fs.DurationVar(&opts.CognitionThought, "cognition-thought-budget", opts.CognitionThought, "maximum time budget for one background thought")
	fs.IntVar(&opts.CognitionMax, "cognition-max-thoughts", opts.CognitionMax, "maximum background thoughts per idle window")
	fs.IntVar(&opts.QueryHotWeight, "cognition-query-hot-weight", opts.QueryHotWeight, "fixed topic weight for hot query proof precompute")
	fs.IntVar(&opts.RandomWeight, "cognition-random-weight", opts.RandomWeight, "fixed topic weight for random graph samples; minimum 1")
	fs.IntVar(&opts.UnresolvedWeight, "cognition-unresolved-weight", opts.UnresolvedWeight, "fixed topic weight for unresolved contradiction checks")
	fs.IntVar(&opts.SemanticWeight, "cognition-semantic-weight", opts.SemanticWeight, "fixed topic weight for embedder semantic neighbors")
	fs.IntVar(&opts.BridgeWeight, "cognition-bridge-weight", opts.BridgeWeight, "fixed topic weight for factorization questions (two hub entities sharing many same-predicate neighbours -> ask for a shared generalization)")
	fs.IntVar(&opts.RandomSampleLimit, "cognition-random-sample", opts.RandomSampleLimit, "bounded entity sample size for random cognition topics")
	fs.IntVar(&opts.SemanticSample, "cognition-semantic-sample", opts.SemanticSample, "bounded entity sample size for semantic cognition topics")
	fs.BoolVar(&opts.ShowCognitionLabels, "cognition-show-labels", opts.ShowCognitionLabels, "include raw entity labels (not just hashes) in /thinking and /questions; default hashes-only for privacy")
	fs.IntVar(&opts.MaxOpenTopics, "max-open-topics", opts.MaxOpenTopics, "maximum lazily-open topic graphs for gRPC serving")
	fs.IntVar(&opts.MinSupport, "min-support", opts.MinSupport, "minimum child support for lifted relations")
	fs.IntVar(&opts.MinParentSupport, "min-parent-support", opts.MinParentSupport, "minimum child support for selected parent nodes")
	fs.IntVar(&opts.MaxParentLevel, "max-parent-level", opts.MaxParentLevel, "maximum parent depth to keep")
	fs.StringVar(&opts.PredicateCSV, "predicates", opts.PredicateCSV, "comma-separated predicates; empty uses registry-derived predicates")
	fs.StringVar(&opts.PredicatePacksCSV, "predicate-packs", opts.PredicatePacksCSV, "comma-separated predicate packs to extend the general registry")
	fs.StringVar(&opts.TypePacksCSV, "type-packs", opts.TypePacksCSV, "comma-separated entity type packs for advisory registry validation")
	fs.StringVar(&opts.TaxonomicCSV, "taxonomic-predicates", opts.TaxonomicCSV, "comma-separated hierarchy predicates")
	fs.StringVar(&opts.TaxonomicDir, "taxonomic-direction", opts.TaxonomicDir, "hierarchy edge direction: child-to-parent or parent-to-child")
	fs.StringVar(&opts.RegistrySpec, "registry", opts.RegistrySpec, "general or path to a registry JSON file")
	fs.StringVar(&opts.HealthAddr, "health-addr", opts.HealthAddr, "health/metrics listen address; empty disables HTTP")
	fs.StringVar(&opts.GRPCAddr, "grpc-addr", opts.GRPCAddr, "gRPC reasoning listen address; empty disables gRPC")
	fs.IntVar(&opts.GRPCPoolSize, "grpc-pool-size", opts.GRPCPoolSize, "read-only graph handles for gRPC reasoning")
	fs.IntVar(&opts.InventorySample, "inventory-sample", opts.InventorySample, "sampled predicate inventory row limit; 0 disables inventory")
	fs.StringVar(&opts.Backend, "backend", opts.Backend, "gRPC reasoning backend: ladybug-native or symbolic-graph")
	fs.StringVar(&opts.ReasoningExt, "reasoning-extension", opts.ReasoningExt, "reasoning extension path/name; empty loads reasoning by name")
	fs.StringVar(&opts.EmbedderURL, "embedder-url", opts.EmbedderURL, "OpenAI-compatible /v1/embeddings base URL for optional semantic cognition; empty disables")
	fs.StringVar(&opts.EmbedderModel, "embedder-model", opts.EmbedderModel, "embedding model name sent to the OpenAI-compatible embedder")
	fs.StringVar(&opts.GoldenFile, "golden-file", opts.GoldenFile, "optional JSON golden claims for validating gRPC snapshot reloads")
	fs.StringVar(&opts.LeaderMode, "leader-mode", opts.LeaderMode, "IGL leader election mode: flock, none, or k8s-lease")
	fs.StringVar(&opts.DefaultTopic, "default-topic", opts.DefaultTopic, "fallback topic when keyword routing has no overlap")
	fs.BoolVar(&opts.Once, "once", opts.Once, "run one IGL pass and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts.LeaderMode = normalizeLeaderMode(opts.LeaderMode)
	hasSource := strings.TrimSpace(opts.SourcePath) != ""
	hasSourceDir := strings.TrimSpace(opts.SourceDir) != ""
	grpcEnabled := strings.TrimSpace(opts.GRPCAddr) != ""
	if !hasSource && !hasSourceDir {
		return fmt.Errorf("--source or --source-dir is required")
	}
	if hasSourceDir && !grpcEnabled {
		return fmt.Errorf("--grpc-addr is required with --source-dir")
	}
	if !hasSource && opts.Once {
		return fmt.Errorf("--source is required with --once")
	}
	if hasSource && strings.TrimSpace(opts.OutDir) == "" {
		return fmt.Errorf("--out-dir is required")
	}
	if grpcEnabled && opts.GRPCPoolSize <= 0 {
		return fmt.Errorf("--grpc-pool-size must be positive")
	}
	if opts.MaxOpenTopics <= 0 {
		opts.MaxOpenTopics = defaultMaxOpenTopics
	}
	if opts.Once && grpcEnabled {
		return fmt.Errorf("--once cannot be combined with --grpc-addr")
	}
	opts.Backend = strings.TrimSpace(opts.Backend)
	if opts.Backend == "" {
		return fmt.Errorf("--backend is required")
	}
	if opts.LeaderMode == leaderModeK8sLease {
		return fmt.Errorf("--leader-mode=%s is not implemented yet", leaderModeK8sLease)
	}
	if opts.LeaderMode != leaderModeFlock && opts.LeaderMode != leaderModeNone {
		return fmt.Errorf("unsupported --leader-mode %q (supported: %s, %s, %s)", opts.LeaderMode, leaderModeFlock, leaderModeNone, leaderModeK8sLease)
	}
	var scopes []generalization.Scope
	var err error
	if hasSource {
		scopes, err = loadServeScopes(opts)
		if err != nil {
			return err
		}
	}
	reg, _, err := loadRegistryWithTypePacks(opts.RegistrySpec, splitCSV(opts.PredicatePacksCSV), splitCSV(opts.TypePacksCSV))
	if err != nil {
		return err
	}
	var source graphHandle
	if hasSource {
		source, err = openLadybugGraph(opts.SourcePath, true)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		defer source.Close()
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	metrics := generalization.NewMetrics()
	loadTracker := NewLoadTracker(LoadTrackerConfig{})
	questions := newQuestionStore(defaultQuestionLimit, logger)
	unconfirmed := newUnconfirmedStore(defaultUnconfirmedLimit, logger)
	thinking := newThinkingState(defaultThinkingRecentLimit)
	setCognitionLabels(opts.ShowCognitionLabels)
	var loop *generalization.Loop
	var runner iglPassRunner
	var leader scopeLeader
	var leaderScopeNames []string
	if hasSource {
		loop = &generalization.Loop{
			Publisher: &generalization.Publisher{
				Source:   loadAwareSource{inner: source, load: loadTracker},
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
		runner = loop
	}
	if hasSource && opts.LeaderMode != leaderModeNone {
		leader, err = newLeaderForMode(opts.LeaderMode, opts.OutDir)
		if err != nil {
			return err
		}
		defer func() {
			if err := leader.Close(); err != nil {
				logger.Printf("leader election close error=%v", err)
			}
		}()
		leaderScopeNames, err = leadershipScopeNames(scopes)
		if err != nil {
			return err
		}
		runner = newLeaderGatedIGLRunner(loop, leader, logger)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var reasoningTelemetry *queryTelemetry
	if grpcEnabled {
		reasoningTelemetry = newQueryTelemetry(defaultQueryTelemetryLimit)
	}
	readiness := newReadinessGate()
	if opts.Once {
		result, err := runner.RunOnce(ctx)
		printPassResult(result)
		return err
	}
	var grpcRuntime *grpcReasoningRuntime
	if grpcEnabled {
		grpcRuntime, err = startGRPCReasoningServer(ctx, opts, reg, reasoningTelemetry, loadTracker, readiness, logger)
		if err != nil {
			return err
		}
		defer grpcRuntime.Close(logger)
		startCognitionWorker(ctx, opts, grpcRuntime, reasoningTelemetry, loadTracker, reg, questions, unconfirmed, thinking, logger)
	} else {
		readiness.MarkReady()
	}
	var inventory *predicateInventory
	var inventoryGraph reasoning.GraphQuerier
	if hasSource {
		inventoryGraph = source
	}
	if grpcRuntime != nil && hasSourceDir {
		inventoryGraph = generationGraphQuerier{source: grpcRuntime.cognitionSource}
	}
	if inventoryGraph != nil {
		inventory = newPredicateInventory(inventoryGraph, reg, PredicateInventoryConfig{
			SampleLimit:     opts.InventorySample,
			RefreshInterval: defaultPredicateInventoryInterval,
			QuietFor:        opts.IGLQuiet,
			Load:            loadTracker,
			Logger:          logger,
		})
	}
	if strings.TrimSpace(opts.HealthAddr) != "" {
		if _, err := startHealthServer(ctx, opts.HealthAddr, metrics, reasoningTelemetry, readiness, inventory, questions, unconfirmed, thinking, grpcRuntime, logger); err != nil {
			return err
		}
	}
	if inventory != nil {
		inventory.Start(ctx)
	}
	if !hasSource {
		<-ctx.Done()
		return nil
	}
	scheduler := NewIGLScheduler(runner, loadTracker, IGLSchedulerConfig{
		BaseInterval:       opts.Interval,
		QuietFor:           opts.IGLQuiet,
		MinPublishInterval: opts.IGLMinPublish,
		Leader:             leader,
		LeaderScopes:       leaderScopeNames,
		Logger:             logger,
	})
	return runLowPriorityIGLWorker(ctx, scheduler.Run)
}

func defaultServeOptions() serveOptions {
	return serveOptions{
		SourcePath:        os.Getenv("PENSIERO_SOURCE"),
		SourceDir:         os.Getenv("PENSIERO_SOURCE_DIR"),
		ScopesCSV:         os.Getenv("PENSIERO_SCOPES"),
		ScopesDir:         os.Getenv("PENSIERO_SCOPES_DIR"),
		OutDir:            os.Getenv("PENSIERO_OUT_DIR"),
		PredicateCSV:      os.Getenv("PENSIERO_PREDICATES"),
		PredicatePacksCSV: os.Getenv("PENSIERO_PREDICATE_PACKS"),
		TypePacksCSV:      os.Getenv("PENSIERO_TYPE_PACKS"),
		TaxonomicCSV:      os.Getenv("PENSIERO_TAXONOMIC_PREDICATES"),
		TaxonomicDir:      firstEnv("PENSIERO_TAXONOMIC_DIRECTION", string(generalization.TaxonomicDirectionChildToParent)),
		RegistrySpec:      firstEnv("PENSIERO_REGISTRY", "general"),
		HealthAddr:        firstEnv("PENSIERO_HEALTH_ADDR", "127.0.0.1:8080"),
		GRPCAddr:          os.Getenv("PENSIERO_GRPC_ADDR"),
		Backend:           reasoning.NativeBackendName,
		ReasoningExt:      os.Getenv("PENSIERO_REASONING_EXTENSION"),
		EmbedderURL:       os.Getenv("PENSIERO_EMBEDDER_URL"),
		EmbedderModel:     firstEnv("PENSIERO_EMBEDDER_MODEL", connector.DefaultEmbeddingModel),
		GoldenFile:        os.Getenv("PENSIERO_GOLDEN_FILE"),
		LeaderMode:        firstEnv("PENSIERO_LEADER_MODE", leaderModeFlock),
		Interval:          envDuration("PENSIERO_INTERVAL", time.Minute),
		IGLQuiet:          envDuration("PENSIERO_IGL_QUIET", defaultIGLQuiet),
		IGLMinPublish:     envDuration("PENSIERO_IGL_MIN_PUBLISH", defaultIGLMinPublish),
		CognitionInterval: envDuration("PENSIERO_COGNITION_INTERVAL", defaultCognitionInterval),
		CognitionWindow:   envDuration("PENSIERO_COGNITION_WINDOW", defaultCognitionWindowBudget),
		CognitionThought:  envDuration("PENSIERO_COGNITION_THOUGHT_BUDGET", defaultCognitionThoughtTimeout),
		MinSupport:        envInt("PENSIERO_MIN_SUPPORT", generalization.DefaultMinSupport),
		MinParentSupport:  envInt("PENSIERO_MIN_PARENT_SUPPORT", 1),
		MaxParentLevel:    envInt("PENSIERO_MAX_PARENT_LEVEL", generalization.DefaultMaxParentLevel),
		GRPCPoolSize:      envInt("PENSIERO_GRPC_POOL_SIZE", defaultGRPCPoolSize),
		InventorySample:   envInt("PENSIERO_INVENTORY_SAMPLE", defaultPredicateInventorySample),
		CognitionMax:      envInt("PENSIERO_COGNITION_MAX_THOUGHTS", defaultCognitionMaxThoughts),
		// Cognition should mostly emit questions that help the graph generalize
		// (factorize through shared generalizations). The random/neighborhood
		// source is OFF by default: it proposed type-incoherent direct edges
		// (e.g. drug "treats" drug, backwards causation) which are off-objective
		// and read as nonsense. Idle generation is covered by the factorization
		// (bridge) source over rotating topics; unresolved/query-hot operate on
		// real humn claims.
		QueryHotWeight:    envInt("PENSIERO_COGNITION_QUERY_HOT_WEIGHT", 1),
		RandomWeight:      envInt("PENSIERO_COGNITION_RANDOM_WEIGHT", 0),
		UnresolvedWeight:  envInt("PENSIERO_COGNITION_UNRESOLVED_WEIGHT", 2),
		SemanticWeight:    envInt("PENSIERO_COGNITION_SEMANTIC_WEIGHT", 1),
		BridgeWeight:      envInt("PENSIERO_COGNITION_BRIDGE_WEIGHT", 3),
		RandomSampleLimit: envInt("PENSIERO_COGNITION_RANDOM_SAMPLE", defaultTopicRandomSampleLimit),
		SemanticSample:    envInt("PENSIERO_COGNITION_SEMANTIC_SAMPLE", defaultTopicSemanticSample),
		MaxOpenTopics:     envInt("PENSIERO_MAX_OPEN_TOPICS", defaultMaxOpenTopics),
		DefaultTopic:      os.Getenv("PENSIERO_DEFAULT_TOPIC"),
	}
}

func startCognitionWorker(ctx context.Context, opts serveOptions, runtime *grpcReasoningRuntime, telemetry *queryTelemetry, load *LoadTracker, reg *reasoning.PredicateRegistry, questions *questionStore, unconfirmed *unconfirmedStore, thinking *thinkingState, logger *log.Logger) {
	if runtime == nil || runtime.cognitionSource == nil || runtime.cache == nil {
		return
	}
	var embedder cognitionEmbedder
	if strings.TrimSpace(opts.EmbedderURL) != "" {
		embedder = connector.NewOpenAIEmbedder(connector.EmbedderConfig{
			BaseURL:     opts.EmbedderURL,
			Model:       opts.EmbedderModel,
			MinInterval: 100 * time.Millisecond,
		})
		if logger != nil {
			logger.Printf("embedder url=%s model=%s", strings.TrimRight(opts.EmbedderURL, "/"), opts.EmbedderModel)
		}
	}
	// In multi-topic mode topics open only on query; wrap the acquirer so
	// background cognition opens and rotates topics itself and never starves.
	cognitionSource := runtime.cognitionSource
	if mgr, ok := cognitionSource.(*topicGenerationManager); ok {
		cognitionSource = newRotatingCognitionAcquirer(mgr)
	}
	selector := NewTopicSelector(cognitionSource, telemetry, reg, embedder, TopicSelectorConfig{
		QueryHotWeight:    opts.QueryHotWeight,
		RandomWeight:      opts.RandomWeight,
		UnresolvedWeight:  opts.UnresolvedWeight,
		SemanticWeight:    opts.SemanticWeight,
		BridgeWeight:      opts.BridgeWeight,
		HotKeyLimit:       defaultTopicHotKeyLimit,
		RandomSampleLimit: opts.RandomSampleLimit,
		SemanticSample:    opts.SemanticSample,
	})
	thinking.SetSourceWeights(selector.SourceWeights())
	engine := &ThoughtEngine{
		Reasoner:    runtime.cache,
		Questions:   questions,
		Unconfirmed: unconfirmed,
		Reg:         reg,
		Logger:      logger,
	}
	scheduler := NewCognitionScheduler(selector, engine, load, CognitionSchedulerConfig{
		BaseInterval:  opts.CognitionInterval,
		QuietFor:      opts.IGLQuiet,
		WindowBudget:  opts.CognitionWindow,
		ThoughtBudget: opts.CognitionThought,
		MaxThoughts:   opts.CognitionMax,
		Logger:        logger,
		Thinking:      thinking,
	})
	go func() {
		if err := runLowPriorityIGLWorker(ctx, scheduler.Run); err != nil && ctx.Err() == nil && logger != nil {
			logger.Printf("cognition scheduler error=%v", err)
		}
	}()
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

func startHealthServer(ctx context.Context, addr string, metrics *generalization.Metrics, reasoningTelemetry *queryTelemetry, readiness *readinessGate, inventory *predicateInventory, questions *questionStore, unconfirmed *unconfirmedStore, thinking *thinkingState, topics topicStatusProvider, logger *log.Logger) (*http.Server, error) {
	mux := healthHandler(metrics, reasoningTelemetry, readiness, inventory, questions, unconfirmed, thinking, topics)
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

func healthHandler(metrics *generalization.Metrics, reasoningTelemetry *queryTelemetry, readiness *readinessGate, inventory *predicateInventory, questions *questionStore, unconfirmed *unconfirmedStore, thinking *thinkingState, topics topicStatusProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		snapshot := metrics.Snapshot()
		querySnapshot := reasoningTelemetry.Snapshot()
		lastErr := lastMetricsError(snapshot)
		status := "ok"
		code := http.StatusOK
		if !readiness.Ready() {
			status = "starting"
			code = http.StatusServiceUnavailable
		} else if lastErr != "" {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}
		payload := map[string]any{
			"status":     status,
			"last_error": lastErr,
			"passes":     snapshot.Passes,
			"scopes":     snapshot.Scopes,
			"reasoning": map[string]any{
				"cache_hit_ratio": querySnapshot.CacheHitRatio,
				"total":           querySnapshot.Total,
				"timeouts":        querySnapshot.Timeouts,
			},
		}
		if topics != nil {
			payload["topics"] = topics.TopicSnapshot()
		}
		writeJSON(w, code, payload)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		snapshot := metrics.Snapshot()
		payload := map[string]any{
			"started_at": snapshot.StartedAt,
			"last_pass":  snapshot.LastPass,
			"scopes":     snapshot.Scopes,
			"passes":     snapshot.Passes,
		}
		if reasoningTelemetry != nil {
			payload["reasoning"] = reasoningTelemetry.Snapshot()
			payload["reasoning_hot_keys"] = reasoningTelemetry.HotKeys(10)
		}
		if topics != nil {
			payload["topics"] = topics.TopicSnapshot()
		}
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("/inventory", func(w http.ResponseWriter, r *http.Request) {
		if inventory == nil {
			writeJSON(w, http.StatusOK, PredicateInventorySnapshot{})
			return
		}
		writeJSON(w, http.StatusOK, inventory.Snapshot())
	})
	mux.HandleFunc("/questions", func(w http.ResponseWriter, r *http.Request) {
		if questions == nil {
			writeJSON(w, http.StatusOK, QuestionSnapshot{})
			return
		}
		writeJSON(w, http.StatusOK, questions.Snapshot())
	})
	mux.HandleFunc("/unconfirmed", func(w http.ResponseWriter, r *http.Request) {
		if unconfirmed == nil {
			writeJSON(w, http.StatusOK, UnconfirmedSnapshot{})
			return
		}
		writeJSON(w, http.StatusOK, unconfirmed.Snapshot())
	})
	mux.HandleFunc("/thinking", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, thinking.Snapshot(questions.Count(), unconfirmed.Count()))
	})
	return mux
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
