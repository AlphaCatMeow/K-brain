package backend

import (
	"strings"
	"testing"
)

// End-to-end over the Gateway (desktop path): a turn sent while memory is off is
// cancelled, memory is turned back on, and the tail is retried with
// resume_message_id. Memory comes back (index re-injected, memory tools offered), but
// the "turned on again" instruction computed for the resumed message is dropped by
// markResumedMessage, so the request's last memory-switch instruction is still
// "Do not save memories ... tell the user memory is turned off".
func TestReview9MemorySwitchResumeAfterReenableLiftsOffInstruction(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	f.setEnabled(t, false)
	seq = f.review3CancelledTurn(t, sess.ID, "off-1", "please look at the build script", seq)
	if text := f.review3LastRequestText(); !strings.Contains(text, "Memory is turned off") {
		t.Fatalf("precondition: off turn must carry the off instruction:\n%s", text)
	}

	f.setEnabled(t, true)
	f.review3Resume(t, sess.ID, "on-resume", seq)
	tools, _ := f.client.lastRequest(t)
	text := f.review3LastRequestText()
	off := strings.LastIndex(text, "Memory is turned off")
	on := strings.LastIndex(text, "Memory is turned on again")
	if on < off {
		t.Fatalf("memory is back on (tools=%v) but the resumed request's last switch instruction is still the off one (off at %d, on-again at %d):\n%s", tools, off, on, text)
	}
}
