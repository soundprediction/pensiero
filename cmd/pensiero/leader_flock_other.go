//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !illumos

package main

import (
	"fmt"
	"os"
	"runtime"
)

func flockLeaderSupported() error {
	return fmt.Errorf("%w: %s; use --leader-mode=none", errLeaderFlockUnsupported, runtime.GOOS)
}

func tryLockLeaderFile(*os.File) error {
	return flockLeaderSupported()
}

func unlockLeaderFile(*os.File) error {
	return nil
}
