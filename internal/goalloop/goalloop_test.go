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
	for _, s := range []string{"GOAL_MET — everything verified", "\n  GOAL_MET done", "**GOAL_MET** — shipped", "## GOAL_MET", "> `GOAL_MET`: verified", "GOAL_MET"} {
		if !Met(s) {
			t.Fatalf("%q must count as met", s)
		}
	}
	for _, s := range []string{
		"I am making progress toward GOAL_MET soon",
		"GOAL_METRIC improved",
		"Done. GOAL_MET",
		"Verified: all green.\n\nGOAL_MET — shipped",
		"Not GOAL_MET yet — tests still fail.",
		"I cannot say GOAL_MET: the build is red.",
		"This is not GOAL_MET.",
		"未达成 GOAL_MET，继续工作",
		"GOAL_MET-pending",
		"GOAL_MET_DONE",
		"goal_met done",
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
