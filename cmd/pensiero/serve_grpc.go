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
	server    *grpc.Server
	store     *generationStore
	reloader  *snapshotReloader
	telemetry *queryTelemetry
	done      chan struct{}
	stopOnce  sync.Once
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
	initial, err := buildGeneration(ctx, opts.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("load gRPC serving graph: %w", err)
	}
	if err := validator.Validate(ctx, initial); err != nil {
		closeGeneration(initial)
		return nil, err
	}
	store := newGenerationStore(initial)
	reloader := newSnapshotReloader(opts.SourcePath, opts.Interval, buildGeneration, validator.Validate, store, logger)
	if fp, err := snapshotFingerprintForPath(opts.SourcePath); err == nil {
		reloader.setLast(fp)
	}
	reloader.Start(ctx)

	listener, err := net.Listen("tcp", opts.GRPCAddr)
	if err != nil {
		reloader.Close()
		_ = store.Close()
		return nil, fmt.Errorf("grpc listen %s: %w", opts.GRPCAddr, err)
	}

	server := grpc.NewServer()
	cfg := serveReasoningConfig()
	cache := newProofCache(store, reg, cfg, defaultProofCacheMaxEntries, defaultProofCacheMaxBytes)
	reasoner := newTelemetryReasonerWithLoad(cache, telemetry, load)
	grpcsvc.NewServer(reasoner).Register(server)
	runtime := &grpcReasoningRuntime{
		server:    server,
		store:     store,
		reloader:  reloader,
		telemetry: telemetry,
		done:      make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			runtime.Close(logger)
		case <-runtime.done:
		}
	}()
	go func() {
		logger.Printf("grpc addr=%s backend=%s graph=%s generation=%s pool_size=%d", listener.Addr().String(), reasoner.Name(), opts.SourcePath, initial.id, opts.GRPCPoolSize)
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
	})
}
