// Package testutil provides assertions and HTTP fixtures for tests only.
package testutil

import (
	"strings"
	"testing"
)

// NoError stops the test when a prerequisite fails.
func NoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Equal reports a mismatch without stopping the test.
func Equal[T comparable](t testing.TB, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// ErrorContains requires an error containing want, or no error when want is empty.
func ErrorContains(t testing.TB, err error, want string) {
	t.Helper()
	if want == "" {
		NoError(t, err)
	} else if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}
