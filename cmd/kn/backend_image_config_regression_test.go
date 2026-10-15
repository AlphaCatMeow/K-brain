package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackendInvalidImageConfigDegradesWithoutLosingConfig(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "kn")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/kn")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	filename := filepath.Join(root, "config.json")
	raw := `{"providers":{},"image":{"connections":[{"id":"ic_missing","enabled":false,"maxConcurrent":1,"binding":{"kind":"provider_ref","providerId":"removed"}}]}}`
	if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		p := startBackendProcessWithEnv(t, binary, "127.0.0.1:0", filename, filepath.Join(root, "sessions"), []string{"HOME=" + root, "LIVEAGENT_HOME=" + root}, "-data-dir", root, "-parent-stdio")
		t.Cleanup(func() {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
			}
		})
		base := "http://" + waitForBackendReady(t, p)
		for path, want := range map[string]int{"/v1/health": 200, "/v1/sessions": 200, "/v1/image/projects": 503} {
			res, err := http.Get(base + path)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != want {
				t.Fatalf("%s: %d", path, res.StatusCode)
			}
		}
		if !strings.Contains(p.output.String(), "invalid image configuration; original configuration preserved") {
			t.Fatal("missing degraded configuration diagnostic")
		}
		p.stdin.Close()
		waitBackendProcess(t, p, true)
		got, err := os.ReadFile(filename)
		if err != nil || string(got) != raw {
			t.Fatalf("configuration overwritten: %v", err)
		}
	}
}
