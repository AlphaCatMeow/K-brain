package session

// Sixth review: store-level contract of Meta.GoalMemoryOff.

import "testing"

func review6Store(t *testing.T) *Store {
	t.Helper()
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 验证（预期通过）：SetGoalMemory 往返；空目标不带标记；SetGoal("") 清掉标记。
func TestReview6SetGoalMemoryRoundTrip(t *testing.T) {
	s := review6Store(t)
	id, err := s.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoalMemory(id, "g", true); err != nil {
		t.Fatal(err)
	}
	if meta, _, _ := s.Load(id); meta.Goal != "g" || !meta.GoalMemoryOff {
		t.Fatalf("round trip: %+v", meta)
	}
	if err := s.SetGoalMemory(id, "", true); err != nil {
		t.Fatal(err)
	}
	if meta, _, _ := s.Load(id); meta.GoalMemoryOff {
		t.Fatalf("empty goal kept the mark: %+v", meta)
	}
	_ = s.SetGoalMemory(id, "g", true)
	if err := s.SetGoal(id, ""); err != nil {
		t.Fatal(err)
	}
	if meta, _, _ := s.Load(id); meta.Goal != "" || meta.GoalMemoryOff {
		t.Fatalf("clear kept the mark: %+v", meta)
	}
}

// 缺陷：Fork 复制 Goal 但不复制 GoalMemoryOff。
func TestReview6ForkKeepsGoalMemoryOff(t *testing.T) {
	s := review6Store(t)
	id, err := s.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoalMemory(id, "written while off", true); err != nil {
		t.Fatal(err)
	}
	forkID, err := s.Fork(id, 1<<30, "fork")
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := s.Load(forkID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Goal != "written while off" || !meta.GoalMemoryOff {
		t.Fatalf("fork copied goal=%q but GoalMemoryOff=%v", meta.Goal, meta.GoalMemoryOff)
	}
}
