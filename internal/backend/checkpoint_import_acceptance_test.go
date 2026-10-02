//go:build migration_acceptance

package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// Run with -tags=migration_acceptance and KBRAIN_MIGRATION_ACCEPTANCE_DIR set.
func TestLegacyCheckpointShippedPathAcceptance(t *testing.T) {
	scratch := os.Getenv("KBRAIN_MIGRATION_ACCEPTANCE_DIR")
	if !filepath.IsAbs(scratch) {
		t.Fatal("KBRAIN_MIGRATION_ACCEPTANCE_DIR must be an absolute scratch directory")
	}
	root, err := os.MkdirTemp(scratch, "checkpoint-chain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("acceptance artifacts: %s", root)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	liveagent := os.Getenv("LIVEAGENT_CHECKOUT")
	if liveagent == "" {
		liveagent = filepath.Join(filepath.Dir(repo), "liveagent")
	}
	liveagent, err = filepath.EvalSymlinks(liveagent)
	if err != nil {
		t.Fatal(err)
	}
	gui := filepath.Join(liveagent, "crates", "agent-gui")
	run := func(t *testing.T, name, dir string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "KBRAIN_MIGRATION_ACCEPTANCE_DIR="+root)
		log, err := os.Create(filepath.Join(root, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = log, log
		err = cmd.Run()
		_ = log.Close()
		if err != nil {
			output, _ := os.ReadFile(filepath.Join(root, name+".log"))
			t.Fatalf("%s: %v\n%s", name, err, output)
		}
		output, _ := os.ReadFile(filepath.Join(root, name+".log"))
		t.Logf("%s:\n%s", name, output)
	}
	run(t, "rust-export", liveagent, "cargo", "test", "-p", "liveagent", "--lib", "exports_checkpoint_acceptance_fixture", "--", "--nocapture")
	homeBytes, err := os.ReadFile(filepath.Join(root, "native-home.txt"))
	if err != nil {
		t.Fatal(err)
	}
	home := string(homeBytes)
	sourcePaths := []string{filepath.Join(home, "chat-history.sqlite3"), filepath.Join(home, ".liveagent/checkpoints/acceptance/index.jsonl"), filepath.Join(home, ".liveagent/checkpoints/acceptance/blobs/0123456789abcdef@v1")}
	before := map[string][32]byte{}
	for _, path := range sourcePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = sha256.Sum256(data)
	}
	for _, scenario := range []string{"fresh", "unresolved-repair"} {
		t.Run(scenario, func(t *testing.T) {
			stateDir := filepath.Join(root, scenario)
			if err := os.WriteFile(filepath.Join(home, "workspace/binary.dat"), []byte("modified workspace"), 0600); err != nil {
				t.Fatal(err)
			}
			var store *session.Store
			var backend *Server
			var httpServer *httptest.Server
			start := func() {
				var err error
				store, err = session.Open(filepath.Join(stateDir, "sessions"))
				if err != nil {
					t.Fatal(err)
				}
				backend, err = New(Options{Store: store, EventDir: filepath.Join(stateDir, "events"), MemoryRoot: filepath.Join(stateDir, "memory"), DefaultCWD: filepath.Join(home, "workspace"), Token: "acceptance-token", Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
					ag := agent.New(&scriptedClient{response: "unused"}, model.Model, 1024, "system")
					ag.ModelName = model.Model
					ag.Provider = model.Provider
					return ag, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				httpServer = httptest.NewServer(backend)
			}
			stop := func() {
				if httpServer != nil {
					httpServer.Close()
					_ = backend.Close()
					_ = store.Close()
					httpServer = nil
				}
			}
			defer stop()
			start()
			script := filepath.Join(gui, "test/providers/kbrain-checkpoint-acceptance.mjs")
			page := filepath.Join(root, "native-page.json")
			phase := "import"
			if scenario == "unresolved-repair" {
				run(t, scenario+"-seed", gui, "node", script, page, httpServer.URL, "acceptance-token", "seed-unresolved")
				stop()
				start()
				phase = "repair"
			}
			run(t, scenario+"-frontend-import", gui, "node", script, page, httpServer.URL, "acceptance-token", phase)
			if len(store.Snapshots("acceptance")) != 1 {
				t.Fatal("native checkpoint was not registered")
			}
			metadataBefore, err := store.HistorySnapshot("acceptance")
			if err != nil {
				t.Fatal(err)
			}
			stop()
			start()
			run(t, scenario+"-frontend-restart-rewind", gui, "node", script, page, httpServer.URL, "acceptance-token", "rewind")
			if len(store.Snapshots("acceptance")) != 0 {
				t.Fatal("import retry resurrected consumed checkpoint references")
			}
			metadataAfter, err := store.HistorySnapshot("acceptance")
			if err != nil {
				t.Fatal(err)
			}
			beforeMessages, _ := json.Marshal(metadataBefore.Messages)
			afterMessages, _ := json.Marshal(metadataAfter.Messages)
			if !bytes.Equal(beforeMessages, afterMessages) {
				t.Fatal("file rewind changed imported history")
			}
			ledgers, err := filepath.Glob(filepath.Join(stateDir, "events/checkpoints/acceptance/legacy-import/*.json"))
			if err != nil || len(ledgers) != 1 {
				t.Fatalf("ledger files: %v %v", ledgers, err)
			}
			data, err := os.ReadFile(ledgers[0])
			if err != nil {
				t.Fatal(err)
			}
			var ledger legacyCheckpointMetadata
			if err := json.Unmarshal(data, &ledger); err != nil {
				t.Fatal(err)
			}
			if len(ledger.Records) != 1 || ledger.Records[0].MtimeMs != 8 || *ledger.Records[0].Mode != 33188 || *ledger.Records[0].Note != "acceptance ledger" || *ledger.Records[0].BlobBase64 != "AP8BgA==" {
				t.Fatalf("native ledger metadata lost: %+v", ledger)
			}
			rawIndex, err := os.ReadFile(sourcePaths[1])
			if err != nil || !bytes.Equal(rawIndex, []byte(ledger.IndexJSONL)) {
				t.Fatalf("raw source index lost: %v", err)
			}
			for _, path := range sourcePaths {
				data, err := os.ReadFile(path)
				if err != nil || sha256.Sum256(data) != before[path] {
					t.Fatalf("source artifact changed: %s %v", path, err)
				}
			}
			t.Log("PASS: native SQLite/index/blob export -> frontend migration -> authenticated Go HTTP import -> backend restart -> preview/rewind -> binary restoration; source hashes unchanged")
		})
	}
}
