//go:build linux

package main

import (
	"context"
	"runtime"
	"syscall"
)

const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

func runLowPriorityIGLWorker(ctx context.Context, run func(context.Context) error) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		setLowPriorityBestEffort()
		errc <- run(ctx)
	}()
	return <-errc
}

func setLowPriorityBestEffort() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10)
	_, _, _ = syscall.Syscall(
		syscall.SYS_IOPRIO_SET,
		uintptr(ioprioWhoProcess),
		0,
		uintptr(ioprioClassIdle<<ioprioClassShift),
	)
}
