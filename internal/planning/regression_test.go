package planning

import (
	"testing"
	"time"
)

func TestEmptyReminderPollKeepsPristineMigrationDestination(t *testing.T) {
	s := openTest(t)
	if _, err := s.Claim(); err != nil {
		t.Fatal(err)
	}
	if readTest(t, s).Seq != 0 {
		t.Fatal("empty notification poll consumed pristine migration state")
	}
	legacy := openTest(t)
	applyTest(t, legacy, "todo.create", Item{"title": "legacy"}, nil)
	if _, err := s.Import(readTest(t, legacy), "desktop", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRecurringReminderMigrationAndDisabledTargets(t *testing.T) {
	s := openTest(t)
	cal := readTest(t, s).Calendars[0]
	now := time.Now()
	ev := applyTest(t, s, "event.create", Item{"calendarId": cal["id"], "title": "daily", "time": Item{"kind": "timed", "startAt": now.Add(-time.Minute).UnixMilli(), "endAt": now.Add(time.Hour).UnixMilli(), "timeZone": "UTC"}, "recurrence": Item{"frequency": "daily", "interval": 1, "count": 3}}, nil)
	due, e := s.Claim()
	if e != nil || len(due) != 1 {
		t.Fatal(due, e)
	}
	id := "event:" + text(ev, "id") + "@" + now.UTC().Format("2006-01-02")
	if text(due[0], "id") != id {
		t.Fatal("legacy occurrence ID changed", due)
	}
	due[0]["success"] = true
	if _, e = s.Finish(due[0]); e != nil {
		t.Fatal(e)
	}
	migrated := openTest(t)
	if _, e = migrated.Import(readTest(t, s), "legacy", nil); e != nil {
		t.Fatal(e)
	}
	if due, e = migrated.Claim(); e != nil || len(due) != 0 {
		t.Fatal("migration redelivered notification", due, e)
	}
	ev = applyTest(t, migrated, "event.update", Item{"reminderMinutes": -1}, ev)
	for _, reminder := range readTest(t, migrated).Reminders {
		if text(reminder, "status") == "pending" {
			t.Fatal("disabled event still pending", reminder)
		}
	}
	task := applyTest(t, migrated, "todo.create", Item{"title": "due", "dueAt": now.Add(-time.Minute).UnixMilli(), "dueReminder": true}, nil)
	applyTest(t, migrated, "todo.update", Item{"dueAt": nil}, task)
	if due, e = migrated.Claim(); e != nil || len(due) != 0 {
		t.Fatal("removed due date delivered", due, e)
	}
}

func TestMalformedFieldsRollback(t *testing.T) {
	s := openTest(t)
	for _, data := range []Item{
		{"title": "bad", "dueAt": "tomorrow"},
		{"title": "bad", "reminderMinutes": 1.5},
		{"title": "bad", "dueReminder": "true"},
		{"title": "bad", "tagIds": "tag"},
		{"title": "bad", "schedule": []any{}},
		{"title": "bad", "dueTimeZone": "missing/zone"},
	} {
		if _, e := s.Mutate(mutation("todo.create", data, nil)); e == nil {
			t.Fatal("malformed field accepted", data)
		}
	}
	if readTest(t, s).Seq != 0 {
		t.Fatal("invalid request changed data")
	}
}

func TestSplitPreservesCountAndDeleteFollowing(t *testing.T) {
	s := openTest(t)
	cal := readTest(t, s).Calendars[0]
	ev := applyTest(t, s, "event.create", Item{"calendarId": cal["id"], "title": "daily", "time": Item{"kind": "allDay", "startDate": "2026-10-05", "endDateExclusive": "2026-10-06", "timeZone": "UTC"}, "recurrence": Item{"frequency": "daily", "interval": 1, "count": 5}}, nil)
	next := applyTest(t, s, "event.split", Item{"date": "2026-10-07", "title": "following"}, ev)
	from, _ := time.Parse("2006-01-02", "2026-10-05")
	v, e := s.Query(from.UnixMilli(), from.AddDate(0, 0, 10).UnixMilli())
	if e != nil || len(v.Events) != 5 || number(object(next, "recurrence"), "count") != 3 {
		t.Fatal(v.Events, next, e)
	}
	applyTest(t, s, "event.split", Item{"date": "2026-10-08", "delete": true}, next)
	v, e = s.Query(from.UnixMilli(), from.AddDate(0, 0, 10).UnixMilli())
	if e != nil || len(v.Events) != 3 {
		t.Fatal(v.Events, e)
	}
}
