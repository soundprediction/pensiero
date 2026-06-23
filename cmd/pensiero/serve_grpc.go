package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/grpcsvc"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc"
)

const grpcGracefulStopTimeout = 5 * time.Second

type grpcReasoningRuntime struct {
	server   *grpc.Server
	pool     *pooledGraphQuerier
	done     chan struct{}
	stopOnce sync.Once
}

func startGRPCReasoningServer(ctx context.Context, opts serveOptions, reg *reasoning.PredicateRegistry, readiness *readinessGate, logger *log.Logger) (*grpcReasoningRuntime, error) {
	initHandle, err := graphHandleInitializerForBackend(opts.Backend, opts.ReasoningExt)
	if err != nil {
		return nil, err
	}
	pool, err := newPooledGraphQuerier(opts.SourcePath, opts.GRPCPoolSize, initHandle)
	if err != nil {
		return nil, fmt.Errorf("load gRPC serving graph: %w", err)
	}
	// Zero values are intentional here: reasoning.Config.withDefaults supplies
	// MaxHops, Decay, MinConf, Limit, and TauHigh for serving requests.
	reasoner, err := reasoning.New(opts.Backend, pool, reg, reasoning.Config{})
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("create gRPC reasoner: %w", err)
	}
	if native, ok := reasoner.(*reasoning.NativeReasoner); ok {
		native.SetEnforcePredicate(true)
	}
	reasoner = reasoning.NewPredicateConstrained(reasoner, reg)
	listener, err := net.Listen("tcp", opts.GRPCAddr)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("grpc listen %s: %w", opts.GRPCAddr, err)
	}

	server := grpc.NewServer()
	grpcsvc.NewServer(reasoner).Register(server)
	runtime := &grpcReasoningRuntime{
		server: server,
		pool:   pool,
		done:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			runtime.Close(logger)
		case <-runtime.done:
		}
	}()
	go func() {
		logger.Printf("grpc addr=%s backend=%s graph=%s pool_size=%d", listener.Addr().String(), reasoner.Name(), opts.SourcePath, opts.GRPCPoolSize)
		readiness.MarkReady()
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.Printf("grpc error=%v", err)
		}
		close(runtime.done)
	}()
	return runtime, nil
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
		if err := r.pool.Close(); err != nil && logger != nil {
			logger.Printf("grpc pool close error=%v", err)
		}
	})
}
