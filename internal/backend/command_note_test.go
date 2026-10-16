package backend

import (
	"net/http"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestCommandNoteAppendsExchangeToHistory(t *testing.T) {
	client := &replyQueue{replies: []string{"ok"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/command-notes", commandNoteInput{Command: "/loop list", Result: "No loops in this conversation."}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("note status %d", resp.StatusCode)
	}
	_, msgs, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 || msgs[len(msgs)-2].Content != "/loop list" || msgs[len(msgs)-1].Content != "No loops in this conversation." {
		t.Fatalf("history: %+v", msgs)
	}
	// The next model turn sees the recorded exchange.
	var accepted protocol.RunAccepted
	postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "after-note", Prompt: "next"}, &accepted)
	waitForRunTerminal(t, base, sess.ID, accepted.RunID)
	for _, bad := range []commandNoteInput{{Command: "loop list", Result: "x"}, {Command: "/loop list"}} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/command-notes", bad, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%+v: status %d", bad, resp.StatusCode)
		}
	}
}
