package netproxy

import (
	"os"
	"sync"
	"testing"
)

// resetForTest drops the published state so the next request re-reads the
// environment, and restores a fresh state when the test ends.
func resetForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		initOnce = sync.Once{}
		current.Store(nil)
	}
	reset()
	t.Cleanup(reset)
}

// standaloneForTest unsets EnvVar for the test and resets the package so it
// starts in standalone mode. Set environment proxies before calling it.
func standaloneForTest(t *testing.T) {
	t.Helper()
	t.Setenv(EnvVar, "")
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatal(err)
	}
	resetForTest(t)
}
