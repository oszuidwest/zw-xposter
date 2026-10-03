package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireInstanceLock keeps one process responsible for a shared state file.
func acquireInstanceLock(path string) (*os.File, error) {
	// The lock is taken first, so it also creates the directory for a new STATE_FILE.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create orchestrator lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // The path comes from the operator's STATE_FILE.
	if err != nil {
		return nil, fmt.Errorf("open orchestrator lock %q: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("orchestrator lock %q is already held; stop the other orchestrator first", path)
		}
		return nil, fmt.Errorf("lock orchestrator file %q: %w", path, err)
	}
	return file, nil
}
