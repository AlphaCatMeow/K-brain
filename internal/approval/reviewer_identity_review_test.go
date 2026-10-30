package approval

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 3322167 keys an ambiguous-start address on the whole IPV6 rule match. Tool arguments are
// JSON, so a line break before a labelled address becomes the letter "n" of "\n" and joins the
// match ("\neth0:fe80::1" -> match "neth0:fe80::1"); a tab joins as "t". The user's own value
// then gets a different identity than in user_intent, is redacted as foreign and marks the
// evidence sensitive. Baseline 4063efc (and 02a1aa5) keyed both on the located value and
// shared one placeholder.
func TestReviewUserLabelledAddressSurvivesJSONEscapedLineBreak(t *testing.T) {
	for name, tc := range map[string]struct{ intent, action string }{
		"newline before eth0": {
			intent: "append the line eth0:fe80::1 to links.txt",
			action: `{"path":"links.txt","content":"# links\neth0:fe80::1\n"}`,
		},
		"tab before ip6": {
			intent: "add ip6:2001:db8:4860::/32 to the SPF record",
			action: `{"path":"spf.txt","content":"v=spf1\tip6:2001:db8:4860::/32 -all\n"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var evidence string
			report := capturingReviewer(&evidence).Review(t.Context(), Evidence{Phase: "action", Tool: "Write", UserIntent: tc.intent, Action: tc.action})
			if !report.Safe() || strings.Count(evidence, "[USER_IPV6_1]") != 2 || strings.Contains(evidence, "REDACTED") {
				t.Fatalf("user's own address lost its placeholder: report=%+v evidence=%s", report, evidence)
			}
		})
	}
}

// Probe: with the mutex, a review blocked behind a resolution that gets cancelled resolves
// again on its own context and completes; it neither deadlocks nor inherits the cancellation.
func TestReviewBlockedBehindCancelledResolutionCompletes(t *testing.T) {
	r, modelsSeen, chatCalls := reviewServer(t, true, 0, "")
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan Report, 1)
	go func() { first <- r.Review(ctx, Evidence{Phase: "action", Tool: "Bash", Action: "ls"}) }()
	<-modelsSeen
	second := make(chan Report, 1)
	go func() { second <- r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: "pwd"}) }()
	time.Sleep(50 * time.Millisecond) // let the second review queue on the resolver
	cancel()
	if got := <-first; got.Reason != "Review cancelled; manual approval required" {
		t.Fatalf("first = %+v", got)
	}
	select {
	case got := <-second:
		if !got.Safe() || chatCalls.Load() != 1 {
			t.Fatalf("second = %+v chat calls %d", got, chatCalls.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second review stuck behind the resolver")
	}
}
