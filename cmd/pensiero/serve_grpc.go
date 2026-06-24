package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/grpcsvc"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc"
)

const grpcGracefulStopTimeout = 5 * time.Second

type grpcReasoningRuntime struct {
	server          *grpc.Server
	store           *generationStore
	topics          *topicGenerationManager
	cognitionSource generationAcquirer
	cache           *proofCache
	reloader        *snapshotReloader
	telemetry       *queryTelemetry
	done            chan struct{}
	stopOnce        sync.Once
}

func startGRPCReasoningServer(ctx context.Context, opts serveOptions, reg *reasoning.PredicateRegistry, telemetry *queryTelemetry, load *LoadTracker, readiness *readinessGate, logger *log.Logger) (*grpcReasoningRuntime, error) {
	buildGeneration, err := generationBuilderForServe(opts, reg)
	if err != nil {
		return nil, err
	}
	goldenSet, err := loadGoldenSet(opts.GoldenFile)
	if err != nil {
		return nil, fmt.Errorf("load golden file: %w", err)
	}
	validator := snapshotValidator{
		Golden:   goldenSet,
		Registry: reg,
	}
	var provider generationProvider
	var store *generationStore
	var topics *topicGenerationManager
	var reloader *snapshotReloader
	var cognitionSource generationAcquirer
	var initial *generation
	graphLabel := strings.TrimSpace(opts.SourcePath)
	if strings.TrimSpace(opts.SourceDir) != "" {
		topics, err = newTopicGenerationManager(ctx, opts.SourceDir, opts.DefaultTopic, opts.MaxOpenTopics, opts.Interval, buildGeneration, validator.Validate, logger)
		if err != nil {
			return nil, err
		}
		provider = topics
		cognitionSource = topics
		graphLabel = opts.SourceDir
	} else {
		initial, err = buildGeneration(ctx, opts.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("load gRPC serving graph: %w", err)
		}
		if err := validator.Validate(ctx, initial); err != nil {
			closeGeneration(initial)
			return nil, err
		}
		store = newGenerationStore(initial)
		reloader = newSnapshotReloader(opts.SourcePath, opts.Interval, buildGeneration, validator.Validate, store, logger)
		if fp, err := snapshotFingerprintForPath(opts.SourcePath); err == nil {
			reloader.setLast(fp)
		}
		reloader.Start(ctx)
		provider = store
		cognitionSource = store
	}

	listener, err := net.Listen("tcp", opts.GRPCAddr)
	if err != nil {
		if reloader != nil {
			reloader.Close()
		}
		if store != nil {
			_ = store.Close()
		}
		if topics != nil {
			_ = topics.Close()
		}
		return nil, fmt.Errorf("grpc listen %s: %w", opts.GRPCAddr, err)
	}

	server := grpc.NewServer()
	cfg := serveReasoningConfig()
	cache := newProofCache(provider, reg, cfg, defaultProofCacheMaxEntries, defaultProofCacheMaxBytes)
	reasoner := newTelemetryReasonerWithLoad(cache, telemetry, load)
	grpcsvc.NewServer(reasoner).Register(server)
	runtime := &grpcReasoningRuntime{
		server:          server,
		store:           store,
		topics:          topics,
		cognitionSource: cognitionSource,
		cache:           cache,
		reloader:        reloader,
		telemetry:       telemetry,
		done:            make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			runtime.Close(logger)
		case <-runtime.done:
		}
	}()
	go func() {
		if initial != nil {
			logger.Printf("grpc addr=%s backend=%s graph=%s generation=%s pool_size=%d", listener.Addr().String(), reasoner.Name(), graphLabel, initial.id, opts.GRPCPoolSize)
		} else {
			logger.Printf("grpc addr=%s backend=%s source_dir=%s topics=%d max_open_topics=%d pool_size=%d", listener.Addr().String(), reasoner.Name(), graphLabel, len(topics.topics), topics.maxOpen, opts.GRPCPoolSize)
		}
		readiness.MarkReady()
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.Printf("grpc error=%v", err)
		}
		close(runtime.done)
	}()
	return runtime, nil
}

func generationBuilderForServe(opts serveOptions, reg *reasoning.PredicateRegistry) (generationBuilder, error) {
	initHandle, err := graphHandleInitializerForBackend(opts.Backend, opts.ReasoningExt)
	if err != nil {
		return nil, err
	}
	cfg := serveReasoningConfig()
	return func(ctx context.Context, path string) (*generation, error) {
		pool, err := newPooledGraphQuerier(path, opts.GRPCPoolSize, initHandle)
		if err != nil {
			return nil, err
		}
		reasoner, err := reasoning.New(opts.Backend, pool, reg, cfg)
		if err != nil {
			_ = pool.Close()
			return nil, fmt.Errorf("create gRPC reasoner: %w", err)
		}
		if native, ok := reasoner.(*reasoning.NativeReasoner); ok {
			native.SetEnforcePredicate(true)
		}
		if opts.ConditionalRules {
			loadedRules, _, err := reasoning.LoadRulesFromGraph(ctx, pool)
			if err != nil {
				_ = pool.Close()
				return nil, err
			}
			ruleSet, err := reasoning.CompileRules(loadedRules, reg)
			if err != nil {
				_ = pool.Close()
				return nil, err
			}
			if ruleSet.Len() > 0 {
				// Per-request assumed facts (e.g. a patient's findings, sent over the
				// gRPC seam) ground rule conditions for one request without any graph
				// write; the graph oracle handles the rest.
				oracle := reasoning.NewAssumedFactsOracle(
					reasoning.NewGraphConditionOracle(pool, reasoner, reg, cfg), reg)
				reasoner = reasoning.NewConditionalReasoner(reasoner, oracle, ruleSet, reg, reasoning.ConditionalConfig{
					Decay: cfg.Decay,
				})
			}
		}
		return &generation{
			id:       newGenerationID(path),
			pool:     pool,
			reasoner: reasoner,
			path:     path,
		}, nil
	}, nil
}

func serveReasoningConfig() reasoning.Config {
	return reasoning.Config{
		MaxHops: 4,
		Decay:   0.9,
		MinConf: 0.05,
		Limit:   8,
		TauHigh: 0.6,
	}.WithExcludeDeduced(true)
}

func newGenerationID(path string) string {
	return fmt.Sprintf("%s-%d", filepath.Base(path), time.Now().UTC().UnixNano())
}

func graphHandleInitializerForBackend(backend string, reasoningExt string) (graphHandleInitializer, error) {
	switch backend {
	case reasoning.NativeBackendName:
		return reasoningExtensionInitializer(reasoningExt), nil
	case reasoning.BackendName:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported reasoning backend %q (supported: %s, %s)", backend, reasoning.NativeBackendName, reasoning.BackendName)
	}
}

func reasoningExtensionInitializer(reasoningExt string) graphHandleInitializer {
	ext := strings.TrimSpace(reasoningExt)
	if ext == "" {
		ext = "reasoning"
	}
	query := "LOAD EXTENSION " + cypherString(ext)
	return func(ctx context.Context, handle graphHandle) error {
		if _, err := handle.Query(ctx, query, nil); err != nil {
			return fmt.Errorf("load reasoning extension %q: %w", ext, err)
		}
		return nil
	}
}

func cypherString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

func (r *grpcReasoningRuntime) Close(logger *log.Logger) {
	r.stopOnce.Do(func() {
		if r.reloader != nil {
			r.reloader.Close()
		}
		gracefulDone := make(chan struct{})
		go func() {
			r.server.GracefulStop()
			close(gracefulDone)
		}()
		select {
		case <-gracefulDone:
		case <-time.After(grpcGracefulStopTimeout):
			if logger != nil {
				logger.Printf("grpc graceful stop timed out after %s; forcing stop", grpcGracefulStopTimeout)
			}
			r.server.Stop()
			<-gracefulDone
		}
		<-r.done
		if r.store != nil {
			if err := r.store.Close(); err != nil && logger != nil {
				logger.Printf("grpc generation store close error=%v", err)
			}
		}
		if r.topics != nil {
			if err := r.topics.Close(); err != nil && logger != nil {
				logger.Printf("grpc topic manager close error=%v", err)
			}
		}
	})
}

func (r *grpcReasoningRuntime) TopicSnapshot() topicServingSnapshot {
	if r == nil {
		return topicServingSnapshot{}
	}
	if r.topics != nil {
		return r.topics.TopicSnapshot()
	}
	if r.store == nil {
		return topicServingSnapshot{}
	}
	gen, release := r.store.Acquire()
	defer release()
	item := openTopicSnapshot{}
	if gen != nil {
		item.Topic = strings.TrimSuffix(filepath.Base(gen.path), filepath.Ext(gen.path))
		item.Path = gen.path
		item.GenerationID = gen.id
	}
	return topicServingSnapshot{
		Available: []string{item.Topic},
		Open:      []openTopicSnapshot{item},
	}
}
