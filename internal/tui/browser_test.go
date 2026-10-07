package tui

import "testing"

func TestBrowserStepLabel(t *testing.T) {
	if got := browserStepLabel(`{"code":"# Searching Amazon for towels\ngoto(\"x\")"}`); got != "Searching Amazon for towels" {
		t.Errorf("got %q", got)
	}
	if got := browserStepLabel(`{"code":"goto(\"x\")"}`); got != "" {
		t.Errorf("no label: %q", got)
	}
	if got := browserStepLabel(`not json`); got != "" {
		t.Errorf("bad json: %q", got)
	}
	if got := browserStepLabel(`{"code":"#hash-nospace\ngoto(\"x\")"}`); got != "hash-nospace" {
		t.Errorf("bare hash: %q", got)
	}
}

func TestCuaStepLabel(t *testing.T) {
	for raw, want := range map[string]string{
		`{"action":"discover"}`:                "Discover Cua tools",
		`{"action":"describe","tool":"click"}`: "Describe click",
		`{"action":"call","tool":"type_text","arguments":{"text":"private input"}}`: "type_text",
		`{"action":"guide","guide":"SKILL.md"}`:                                     "Read Cua guide SKILL.md",
	} {
		if got := browserStepLabel(raw); got != want {
			t.Errorf("%s: %q want %q", raw, got, want)
		}
	}
}
