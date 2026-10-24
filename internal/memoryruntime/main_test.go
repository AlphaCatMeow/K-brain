package memoryruntime

import (
	"fmt"
	"os"
	"testing"
)

// Extraction logs to k-brain.log under the data root; keep tests off the
// developer's real ~/.liveagent.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "memoryruntime-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("LIVEAGENT_HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
