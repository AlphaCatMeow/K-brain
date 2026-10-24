package memoryruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

// 8e5bc94 "Record every batch rejection in warningDetails" and bf0d30a "log ...
// batch warnings": extract() logs only result.WarningDetails, but ApplyBatch
// reports a rejected daily append in Warnings alone. Once today's journal is
// near the 8 KiB cap every append_daily the extractor submits is dropped with
// no log line, the silent-drop class bf0d30a set out to remove.
func TestRejectedDailyAppendIsReportedAndLogged(t *testing.T) {
	buf := captureLog(t)
	client := &fixtureClient{payload: `{"decisions":[{"action":"append_daily","body":"` + strings.Repeat("y", 400) + `"}]}`}
	rt, store := testRuntime(t, client)
	prefill := memory.BatchArgs{DailyAppend: &struct {
		Bullet string `json:"bullet"`
	}{Bullet: strings.Repeat("x", 8000)}}
	if res, err := store.ApplyBatch(prefill); err != nil || len(res.Warnings) != 0 {
		t.Fatalf("prefill: %+v %v", res, err)
	}
	direct, err := store.ApplyBatch(memory.BatchArgs{Trigger: "extraction", DailyAppend: &struct {
		Bullet string `json:"bullet"`
	}{Bullet: strings.Repeat("y", 400)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Warnings) == 1 && len(direct.WarningDetails) == 0 {
		t.Errorf("rejected daily append is in warnings only: warnings=%q warningDetails=%v", direct.Warnings, direct.WarningDetails)
	}
	rt.Completed(context.Background(), extractionTurn("today I migrated the build to pnpm workspaces"))
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.calls > 0
	})
	waitFor(t, func() bool { return strings.Contains(buf.String(), "memory extraction") })
	if !strings.Contains(buf.String(), "memory extraction") {
		t.Error("extractor's append_daily was rejected by the store but nothing was logged")
	}
}
