package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestCompactCommitFailureRestoresAgentTranscript(t *testing.T) {
	client := &scriptedClient{response: "summary"}
	ag := agent.New(client, "fixture-model", 1024, "system")
	ag.Messages = []ai.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	}
	before := ag.MessagesSnapshot()
	err := ag.ManualCompactCommit(context.Background(), agent.Events{}, func() error {
		return context.Canceled
	})
	if err == nil || !reflect.DeepEqual(before, ag.MessagesSnapshot()) {
		t.Fatalf("commit failure did not restore transcript: err=%v messages=%+v", err, ag.MessagesSnapshot())
	}
}

func TestCompactStartJournalFailureDoesNotCreateAcceptedRun(t *testing.T) {
	store, backend, server, sess := compactFixture(t, &scriptedClient{response: "summary"})
	defer store.Close()
	before := getSession(t, server.URL, sess.ID)
	if err := os.Mkdir(filepath.Join(backend.eventDir, sess.ID+".jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{
		ConversationID: sess.ID, ClientRequestID: "journal-failure", ExpectedRevision: before.Revision,
	}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("journal failure status = %d", resp.StatusCode)
	}
	view := getSession(t, server.URL, sess.ID)
	if view.LastSeq != before.LastSeq {
		t.Fatalf("journal failure advanced event sequence: before=%d after=%d", before.LastSeq, view.LastSeq)
	}
}

type compactCallbackClient struct {
	*scriptedClient
	complete func(context.Context) error
}

func (c *compactCallbackClient) Complete(ctx context.Context, _ ai.Request) (string, ai.Usage, error) {
	return "summary", ai.Usage{PromptTokens: 7, CompletionTokens: 3}, c.complete(ctx)
}

func waitCompactDone(t *testing.T, rt *runtimeSession) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		rt.mu.Lock()
		done, changed := rt.runDone, rt.changed
		rt.mu.Unlock()
		if done {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("compaction did not finish")
		}
	}
}

func TestCompactHTTPFailureRestoresTransaction(t *testing.T) {
	for _, fault := range []string{"save", "journal", "restore", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			store, backend, server, sess := compactFixture(t, &scriptedClient{response: "answer"})
			defer store.Close()
			seedCompactHistory(t, server.URL, sess.ID)
			rt, err := backend.loadRuntimeByID(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := getSession(t, server.URL, sess.ID)
			ag := rt.agent
			snapshot := rt.recorder.Snapshot()
			path := store.TranscriptPath(sess.ID)
			transcript, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			// Every exit path must unblock the compaction callback, or Server.Close waits
			// forever on the blocked worker.
			released := false
			t.Cleanup(func() {
				if !released {
					released = true
					close(release)
				}
			})
			var modelContext context.Context
			ag.CompactClient = &compactCallbackClient{scriptedClient: &scriptedClient{}, complete: func(ctx context.Context) error {
				modelContext = ctx
				close(entered)
				<-release
				if fault == "cancel" {
					<-ctx.Done()
					return ctx.Err()
				}
				if fault == "restore" {
					return errors.New("injected model failure")
				}
				return nil
			}}
			var accepted protocol.CompactAccepted
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{
				ClientRequestID: "fault", ExpectedRevision: before.Revision,
			}, &accepted)
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("model did not start")
			}
			switch fault {
			case "save":
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires privileges on Windows")
				}
				// Reads traverse the symlink; the atomic writer rejects a symlink directory.
				dir := filepath.Dir(path)
				// Register the restore first: a later t.Fatal must never strand the renamed
				// directory, or Close deadlocks on the blocked compaction worker (LIFO cleanups).
				t.Cleanup(func() { _ = os.Remove(dir); _ = os.Rename(dir+".saved", dir) })
				if err := os.Rename(dir, dir+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".saved", dir); err != nil {
					t.Fatal(err)
				}
			case "journal":
				journal := filepath.Join(backend.eventDir, sess.ID+".jsonl")
				if err := os.Rename(journal, journal+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(journal, 0700); err != nil {
					t.Fatal(err)
				}
			case "restore":
				rt.mu.Lock()
				rt.agent = agent.New(&scriptedClient{}, "fixture-model", 1024, "replacement")
				rt.mu.Unlock()
			case "cancel":
				resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", struct{}{}, nil)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("cancel status = %d", resp.StatusCode)
				}
			}
			if !released {
				released = true
				close(release)
			}
			waitCompactDone(t, rt)
			select {
			case <-modelContext.Done():
			case <-time.After(time.Second):
				t.Fatal("compaction context was not released")
			}
			gotTranscript, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(transcript, gotTranscript) {
				t.Fatal("failure changed durable history")
			}
			if !reflect.DeepEqual(snapshot.Messages, ag.MessagesSnapshot()) || !reflect.DeepEqual(snapshot.Usage, ag.UsageSummary()) {
				t.Fatal("failure changed Agent context or usage")
			}
			if !reflect.DeepEqual(snapshot, rt.recorder.Snapshot()) {
				t.Fatal("failure changed recorder snapshot")
			}
			rt.mu.Lock()
			state, quarantined := rt.runs["fault"].State, rt.runtimeErr != nil
			events := append([]protocol.Event(nil), rt.events...)
			rt.mu.Unlock()
			want := "failed"
			if fault == "cancel" {
				want = "cancelled"
			}
			if state != want {
				t.Fatalf("state = %s, want %s", state, want)
			}
			if (fault == "restore" || fault == "journal") && !quarantined {
				t.Fatal("unrecoverable runtime was not quarantined")
			}
			for _, event := range events {
				if event.RunID != accepted.RunID {
					continue
				}
				if event.Type == protocol.EventRunCompleted || event.Type == protocol.EventHistoryUpdated || event.Type == protocol.EventUsage {
					t.Fatalf("failed transaction published %s", event.Type)
				}
			}
			if fault == "save" {
				journal, err := os.ReadFile(filepath.Join(backend.eventDir, sess.ID+".jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
					var event protocol.Event
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					if event.RunID == accepted.RunID && (event.Type == protocol.EventRunCompleted || event.Type == protocol.EventUsage) {
						t.Fatal("save failure retained staged success journal")
					}
				}
			}
		})
	}
}
