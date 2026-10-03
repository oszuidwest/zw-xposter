package main

import (
	"path/filepath"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestAcquireInstanceLockLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrator.lock")
	first, err := acquireInstanceLock(path)
	testutil.NoError(t, err)
	defer func() { _ = first.Close() }()

	second, err := acquireInstanceLock(path)
	if second != nil {
		_ = second.Close()
		t.Fatal("second acquireInstanceLock() returned a lock")
	}
	testutil.ErrorContains(t, err, "already held")
	testutil.NoError(t, first.Close())

	second, err = acquireInstanceLock(path)
	testutil.NoError(t, err)
	testutil.NoError(t, second.Close())
}

func TestRunLocksNextToStateFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("STATE_FILE", statePath)
	held, err := acquireInstanceLock(statePath + ".lock")
	testutil.NoError(t, err)
	defer func() { _ = held.Close() }()

	err = run(options{once: true}, nil)
	testutil.ErrorContains(t, err, "already held")
}
