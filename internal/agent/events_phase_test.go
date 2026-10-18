package agent

import "testing"

func TestFanInForwardsRunPhase(t *testing.T) {
	var got []string
	ev := FanIn(Events{}, Events{OnPhase: func(stage string) { got = append(got, "first:"+stage) }}, Events{OnPhase: func(stage string) { got = append(got, "second:"+stage) }})
	ev.OnPhase("waiting_model")
	if len(got) != 2 || got[0] != "first:waiting_model" || got[1] != "second:waiting_model" {
		t.Fatalf("phase fan-in: %v", got)
	}
}
