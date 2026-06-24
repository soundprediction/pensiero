//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || illumos

package main

import (
	"errors"
	"os"
	"syscall"
)

func flockLeaderSupported() error {
	return nil
}

func tryLockLeaderFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errLeaderLockHeld
		}
		return err
	}
	return nil
}

func unlockLeaderFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
