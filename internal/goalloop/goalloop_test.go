package goalloop

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestContinuePromptCarriesGoalAndToken(t *testing.T) {
	p := ContinuePrompt("ship the feature")
	if !strings.Contains(p, "ship the feature") || strings.Count(p, MetToken) != 3 {
		t.Fatalf("prompt: %q", p)
	}
}

func TestMet(t *testing.T) {
	for _, s := range []string{"GOAL_MET — everything verified", "\n  GOAL_MET done", "Done. GOAL_MET"} {
		if !Met(s) {
			t.Fatalf("%q must count as met", s)
		}
	}
	for _, s := range []string{
		"I am making progress toward GOAL_MET soon",
		"GOAL_METRIC improved",
		"almost done",
		"",
		strings.Repeat("x", 250) + " GOAL_MET",
	} {
		if Met(s) {
			t.Fatalf("%q must not count as met", s)
		}
	}
}

func TestMaxRoundsResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if n := MaxRounds(dir, 0); n != config.DefaultGoalMaxRounds {
		t.Fatalf("default: %d", n)
	}
	if n := MaxRounds(dir, 7); n != 7 {
		t.Fatalf("global: %d", n)
	}
	if err := config.SetProjectGoalMaxRounds(dir, 3); err != nil {
		t.Fatal(err)
	}
	if n := MaxRounds(dir, 7); n != 3 {
		t.Fatalf("project override: %d", n)
	}
	if n := MaxRounds("", 7); n != 7 {
		t.Fatalf("empty dir skips project lookup: %d", n)
	}
}
