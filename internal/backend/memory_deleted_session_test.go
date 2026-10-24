package backend

import (
	"net/http/httptest"
	"testing"
)

// A stopped turn's message is held verbatim in the memory store's
// .kbrain-state.json under the session ID (memoryruntime Abandoned ->
// memory.HoldUnjudged). Deleting the conversation (history.go deleteSession)
// removes its transcript, events and cron loops (forgetSession) but leaves
// the held text: the deleted conversation's words stay on disk and nothing
// ever takes them, since no later turn runs under that ID.
func TestDeletedSessionDropsHeldUnjudgedMessages(t *testing.T) {
	server, rt, chat, _ := newAbandonedRebuildFixture(t)
	secret := "记住：我家里 Wi-Fi 的密码是 hunter2，先帮我跑一下全部测试"
	stopTurn(t, rt, chat, secret)
	server.releaseRuntime(rt)
	response := httptest.NewRecorder()
	server.deleteSession(response, httptest.NewRequest("DELETE", "/v1/sessions/"+rt.id, nil), rt.id)
	if response.Code != 200 {
		t.Fatalf("fixture: delete status=%d body=%s", response.Code, response.Body.String())
	}
	held, err := server.memoryStore.TakeUnjudged(rt.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("deleted session %s still holds %d unjudged messages in the memory state file: %q", rt.id, len(held), held[0].Text)
	}
}
