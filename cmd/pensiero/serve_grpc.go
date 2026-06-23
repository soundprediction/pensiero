package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
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
	pool, err := newPooledGraphQuerier(opts.SourcePath, opts.GRPCPoolSize)
	if err != nil {
		return nil, fmt.Errorf("load gRPC serving graph: %w", err)
	}
	// Zero values are intentional here: reasoning.Config.withDefaults supplies
	// MaxHops, Decay, MinConf, Limit, and TauHigh for serving requests.
	reasoner, err := reasoning.New(reasoning.BackendName, pool, reg, reasoning.Config{})
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("create gRPC reasoner: %w", err)
	}
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
