//go:build !linux

package main

import "context"

func runLowPriorityIGLWorker(ctx context.Context, run func(context.Context) error) error {
	return run(ctx)
}
