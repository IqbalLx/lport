//go:build windows

package main

import (
	"os"
	"syscall"
)

// detachedProcAttr is a no-op on Windows
func detachedProcAttr() *syscall.SysProcAttr {
	return nil
}

// processAlive reports whether a process with the given pid exists
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

// findExistingForwards is not supported on Windows
func findExistingForwards() []ExistingForward {
	return nil
}
