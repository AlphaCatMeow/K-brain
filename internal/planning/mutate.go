package planning

import (
	"strings"
	"time"
)

var fields = map[string]string{
	"calendar.create": "name color", "calendar.update": "name color sortOrder isDefault reminderMinutes", "calendar.delete": "moveTo",
	"group.create": "name color sortOrder", "group.update": "name color sortOrder", "group.delete": "",
	"tag.create": "name color sortOrder", "tag.update": "name color sortOrder", "tag.delete": "",
	"mytasks.update": "color", "mytasks.delete": "defaultGroupId deleteTasks",
	"todo.create": "title notes estimateMinutes dueAt dueDate dueTimeZone dueReminder groupId parentId priority tagIds reminderMinutes schedule",
	"todo.update": "title notes status estimateMinutes dueAt dueDate dueTimeZone dueReminder groupId parentId priority tagIds reminderMinutes",
	"todo.move":   "parentId groupId beforeId afterId relativeRevision", "todo.schedule": "calendarId time title notes",
	"todo.delete": "", "todo.restore": "", "todo.purge": "",
	"event.create": "calendarId title time notes todoId recurrence reminderMinutes tagIds",
	"event.update": "time title titleOverride notes calendarId todoId recurrence reminderMinutes tagIds",
	"event.delete": "", "event.restore": "", "event.purge": "",
	"event.exception": "date delete time title notes", "event.split": "date delete time title notes recurrence calendarId reminderMinutes", "event.restoreException": "date exceptionId exceptionRevision",
	"calendar.import": "entries", "todo.import": "entries",
	"reminder.create": "targetType targetId triggerAt", "reminder.snooze": "minutes", "reminder.acknowledge": "", "reminder.delete": "",
	"source.create": "targetType targetId providerKind accountId externalId title",
}

func writable(v *Snapshot, id string) error {
	c := find(v.Calendars, id)
	if c == nil {
		return errCode("calendar_missing")
	}
	if boolean(c, "readOnly") {
		return errCode("calendar_read_only")
	}
	return nil
}
func apply(v *Snapshot, m Mutation) (any, error) {
	allowed, ok := fields[m.Action]
	if !ok {
		return nil, errCode("unknown_action")
	}
	for key := range m.Data {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return nil, errCode("field_immutable:" + key)
		}
	}
	d := m.Data
	x := lookup(v, m.Action, m.ID)
	now := time.Now().UnixMilli()
	kind, action, _ := strings.Cut(m.Action, ".")
	if x != nil && (kind == "todo" || kind == "event") && number(x, "deletedAt") != 0 && action != "restore" && action != "purge" {
		return nil, errCode("restore_" + kind + "_first")
	}
	if kind == "event" && x != nil {
		if e := writable(v, text(x, "calendarId")); e != nil {
			return nil, e
		}
	}
	switch m.Action {
	case "group.create", "tag.create":
		x = Item{"id": ID(), "name": d["name"], "color": "#64748b", "sortOrder": 0, "revision": 1}
		copyFields(x, d)
		if kind == "group" {
			v.Groups = append(v.Groups, x)
		} else {
			v.Tags = append(v.Tags, x)
		}
	case "group.update", "tag.update":
		copyFields(x, d)
		touch(x)
	case "group.delete":
		v.Groups = remove(v.Groups, map[string]bool{m.ID: true})
		if v.DefaultGroupID == m.ID {
			v.DefaultGroupID = ""
		}
		for _, t := range v.Todos {
			if text(t, "groupId") == m.ID {
				t["groupId"] = nullable(v.DefaultGroupID)
				touch(t)
			}
		}
		x = nil
	case "tag.delete":
		v.Tags = remove(v.Tags, map[string]bool{m.ID: true})
		for _, t := range append(v.Todos, v.Events...) {
			tags := []any{}
			for _, id := range array(t, "tagIds") {
				if id != m.ID {
					tags = append(tags, id)
				}
			}
			t["tagIds"] = tags
			touch(t)
		}
		x = nil
	case "calendar.create":
		x = calendar(text(d, "name"), "#2563eb", false)
		copyFields(x, d)
		x["sortOrder"] = len(v.Calendars)
		v.Calendars = append(v.Calendars, x)
	case "calendar.update":
		if len(d) != 1 || d["color"] == nil {
			if e := writable(v, m.ID); e != nil {
				return nil, e
			}
		}
		if boolean(d, "isDefault") {
			for _, c := range v.Calendars {
				if c != nil && text(c, "id") != m.ID && boolean(c, "isDefault") {
					c["isDefault"] = false
					touch(c)
				}
			}
		}
		copyFields(x, d)
		touch(x)
	case "calendar.delete":
		if e := writable(v, m.ID); e != nil {
			return nil, e
		}
		target := text(d, "moveTo")
		if target == m.ID {
			return nil, errCode("choose_other_calendar")
		}
		if e := writable(v, target); e != nil {
			return nil, e
		}
		for _, e := range v.Events {
			if text(e, "calendarId") == m.ID {
				e["calendarId"] = target
				touch(e)
			}
		}
		if boolean(x, "isDefault") {
			c := find(v.Calendars, target)
			c["isDefault"] = true
			touch(c)
		}
		v.Calendars = remove(v.Calendars, map[string]bool{m.ID: true})
		x = nil
	case "mytasks.update":
		if v.DefaultGroupID != "" {
			return nil, errCode("my_tasks_deleted")
		}
		v.MyTasksColor = text(d, "color")
	case "mytasks.delete":
		if v.DefaultGroupID != "" {
			return nil, errCode("my_tasks_deleted")
		}
		id := text(d, "defaultGroupId")
		if find(v.Groups, id) == nil {
			return nil, errCode("category_missing")
		}
		v.DefaultGroupID = id
		for _, t := range v.Todos {
			if text(t, "groupId") == "" {
				t["groupId"] = id
				if boolean(d, "deleteTasks") {
					t["deletedAt"] = now
				}
				touch(t)
			}
		}
	case "todo.create":
		x = Item{"id": ID(), "title": "", "notes": "", "status": "open", "priority": "medium", "reminderMinutes": 0, "dueReminder": false, "tagIds": []any{}, "sortOrder": len(v.Todos), "createdAt": now, "updatedAt": now, "revision": 1}
		copyFields(x, d)
		delete(x, "schedule")
		if text(x, "groupId") == "" {
			x["groupId"] = nullable(v.DefaultGroupID)
		}
		if id := text(x, "parentId"); id != "" {
			p := find(v.Todos, id)
			if p == nil || number(p, "deletedAt") != 0 {
				return nil, errCode("parent_missing_or_trashed")
			}
			x["groupId"] = p["groupId"]
		}
		v.Todos = append(v.Todos, x)
		if schedule := object(d, "schedule"); schedule != nil {
			if _, e := scheduleTask(v, x, schedule); e != nil {
				return nil, e
			}
		}
	case "todo.update":
		oldGroup := text(x, "groupId")
		copyFields(x, d)
		if _, ok := d["groupId"]; ok && text(x, "groupId") == "" {
			x["groupId"] = nullable(v.DefaultGroupID)
		}
		if text(x, "status") == "completed" {
			x["completedAt"] = now
		} else {
			x["completedAt"] = nil
		}
		touch(x)
		if text(x, "groupId") != oldGroup {
			x["parentId"] = nil
			for id := range descendants(v, m.ID) {
				if id != m.ID {
					t := find(v.Todos, id)
					t["groupId"] = x["groupId"]
					touch(t)
				}
			}
		}
		for _, e := range v.Events {
			if text(e, "todoId") == m.ID {
				e["title"] = x["title"]
				e["notes"] = x["notes"]
				touch(e)
			}
		}
	case "todo.move":
		if e := move(v, x, d); e != nil {
			return nil, e
		}
	case "todo.schedule":
		return scheduleTask(v, x, d)
	case "todo.delete", "todo.restore", "event.delete", "event.restore":
		restore := action == "restore"
		if restore != (number(x, "deletedAt") != 0) {
			return nil, errCode("item_changed")
		}
		if restore && kind == "event" {
			for _, key := range []string{"todoId", "seriesId"} {
				list := v.Events
				if key == "todoId" {
					list = v.Todos
				}
				if p := find(list, text(x, key)); p != nil && number(p, "deletedAt") != 0 {
					return nil, errCode("restore_linked_task")
				}
			}
		}
		if restore {
			x["deletedAt"] = nil
		} else {
			x["deletedAt"] = now
			if kind == "todo" {
				for _, t := range v.Todos {
					if text(t, "parentId") == m.ID {
						t["parentId"] = nil
						touch(t)
					}
				}
			}
		}
		touch(x)
	case "todo.purge", "event.purge":
		if number(x, "deletedAt") == 0 {
			return nil, errCode("purge_requires_trash")
		}
		removed := map[string]bool{m.ID: true}
		if kind == "todo" {
			v.Todos = remove(v.Todos, removed)
			for _, e := range v.Events {
				if text(e, "todoId") == m.ID {
					removed[text(e, "id")] = true
				}
			}
		}
		for _, e := range v.Events {
			if text(e, "seriesId") == m.ID {
				removed[text(e, "id")] = true
			}
		}
		v.Events = remove(v.Events, removed)
		x = nil
	case "event.create":
		if e := writable(v, text(d, "calendarId")); e != nil {
			return nil, e
		}
		x = newEvent(d)
		if id := text(x, "todoId"); id != "" {
			t := find(v.Todos, id)
			if t == nil || number(t, "deletedAt") != 0 {
				return nil, errCode("task_missing")
			}
			x["title"] = t["title"]
			x["notes"] = t["notes"]
		}
		v.Events = append(v.Events, x)
	case "event.update":
		if id := text(d, "calendarId"); id != "" {
			if e := writable(v, id); e != nil {
				return nil, e
			}
		}
		copyFields(x, d)
		touch(x)
		if t := find(v.Todos, text(x, "todoId")); t != nil {
			for _, key := range []string{"title", "notes"} {
				if value, ok := d[key]; ok {
					t[key] = value
					touch(t)
					for _, event := range v.Events {
						if text(event, "todoId") == text(t, "id") {
							event[key] = value
							touch(event)
						}
					}
				}
			}
		}
	case "event.exception", "event.split", "event.restoreException":
		return recurringMutation(v, m, x)
	case "calendar.import", "todo.import":
		return importEntries(v, m)
	case "reminder.create":
		target := lookup(v, text(d, "targetType")+".update", text(d, "targetId"))
		if target == nil {
			return nil, errCode("reminder_target_missing")
		}
		x = Item{"id": ID(), "targetType": d["targetType"], "targetId": d["targetId"], "title": target["title"], "origin": "manual", "triggerAt": d["triggerAt"], "status": "pending", "revision": 1, "attempts": 0, "nextAttemptAt": 0}
		v.Reminders = append(v.Reminders, x)
	case "reminder.acknowledge":
		x["status"] = "acknowledged"
		touch(x)
	case "reminder.snooze":
		n := number(d, "minutes")
		if n < 1 || n > 10080 {
			return nil, errCode("invalid_params:minutes")
		}
		x["snoozedUntil"] = now + n*60000
		x["status"] = "pending"
		x["notifiedAt"] = nil
		x["nextAttemptAt"] = 0
		x["leaseUntil"] = nil
		touch(x)
	case "reminder.delete":
		v.Reminders = remove(v.Reminders, map[string]bool{m.ID: true})
		x = nil
	case "source.create":
		x = Item{"id": ID(), "revision": 1}
		copyFields(x, d)
		v.Sources = append(v.Sources, x)
	default:
		return nil, errCode("unknown_action")
	}
	return x, nil
}
func copyFields(to, from Item) {
	for k, v := range from {
		to[k] = v
	}
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func newEvent(d Item) Item {
	now := time.Now().UnixMilli()
	x := Item{"id": ID(), "title": "", "notes": "", "revision": 1, "createdAt": now, "updatedAt": now, "tagIds": []any{}}
	copyFields(x, d)
	return x
}
func scheduleTask(v *Snapshot, t, d Item) (any, error) {
	if len(d) > 4 {
		return nil, errCode("task_schedule_invalid")
	}
	if e := writable(v, text(d, "calendarId")); e != nil {
		return nil, e
	}
	e := newEvent(Item{"calendarId": d["calendarId"], "time": d["time"], "title": t["title"], "notes": t["notes"], "todoId": t["id"]})
	v.Events = append(v.Events, e)
	return e, nil
}
func descendants(v *Snapshot, id string) map[string]bool {
	ids := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, t := range v.Todos {
			key := text(t, "id")
			if ids[text(t, "parentId")] && !ids[key] {
				ids[key] = true
				changed = true
			}
		}
	}
	return ids
}
func move(v *Snapshot, x, d Item) error {
	ids := descendants(v, text(x, "id"))
	if _, ok := d["parentId"]; ok {
		x["parentId"] = d["parentId"]
	}
	if _, ok := d["groupId"]; ok {
		x["groupId"] = d["groupId"]
	}
	if id := text(x, "parentId"); id != "" {
		if ids[id] {
			return errCode("task_cycle")
		}
		p := find(v.Todos, id)
		if p == nil || number(p, "deletedAt") != 0 {
			return errCode("parent_missing_or_trashed")
		}
		if _, explicit := d["groupId"]; explicit && text(p, "groupId") != text(x, "groupId") {
			x["parentId"] = nil
		} else {
			x["groupId"] = p["groupId"]
		}
	}
	before, after := text(d, "beforeId"), text(d, "afterId")
	if before != "" && after != "" {
		return errCode("invalid_params:relative")
	}
	relative := before
	if relative == "" {
		relative = after
	}
	siblings := []Item{}
	for _, t := range v.Todos {
		if text(t, "id") != text(x, "id") && text(t, "parentId") == text(x, "parentId") && text(t, "groupId") == text(x, "groupId") && number(t, "deletedAt") == 0 {
			siblings = append(siblings, t)
		}
	}
	sorted(siblings)
	pos := len(siblings)
	if relative != "" {
		pos = -1
		for i, t := range siblings {
			if text(t, "id") == relative {
				if number(d, "relativeRevision") != number(t, "revision") {
					return errCode("conflict")
				}
				pos = i
				if after != "" {
					pos++
				}
				break
			}
		}
		if pos < 0 {
			return errCode("relative_task_missing")
		}
	}
	ordered := append([]Item{}, siblings[:pos]...)
	ordered = append(ordered, x)
	ordered = append(ordered, siblings[pos:]...)
	for i, t := range ordered {
		t["sortOrder"] = i
		touch(t)
	}
	for id := range ids {
		t := find(v.Todos, id)
		t["groupId"] = x["groupId"]
		if id != text(x, "id") {
			touch(t)
		}
	}
	return nil
}
func importEntries(v *Snapshot, m Mutation) (any, error) {
	entries := array(m.Data, "entries")
	if len(entries) < 1 || len(entries) > 200 {
		return nil, errCode("import_count")
	}
	if m.Action == "calendar.import" {
		if e := writable(v, m.ID); e != nil {
			return nil, e
		}
	}
	imported, skipped := 0, 0
	for _, raw := range entries {
		d, ok := raw.(map[string]any)
		if !ok {
			return nil, errCode("import_invalid")
		}
		uid := text(d, "uid")
		if uid == "" {
			return nil, errCode("import_missing_uid")
		}
		provider, external := "google-tasks", uid
		if m.Action == "calendar.import" {
			provider = "ical"
			external = m.ID + ":" + uid
		}
		exists := false
		for _, src := range v.Sources {
			if text(src, "providerKind") == provider && text(src, "externalId") == external {
				exists = true
				break
			}
		}
		if exists {
			skipped++
			continue
		}
		var item Item
		target := "event"
		if m.Action == "calendar.import" {
			item = newEvent(Item{"title": d["title"], "notes": defaultText(d, "notes", ""), "time": d["time"], "calendarId": m.ID})
			v.Events = append(v.Events, item)
		} else {
			target = "todo"
			group := ""
			list := text(d, "list")
			if list != "" && list != "My Tasks" && list != "我的任务" {
				for _, g := range v.Groups {
					if text(g, "name") == list {
						group = text(g, "id")
					}
				}
				if group == "" {
					g := Item{"id": ID(), "name": list, "color": "#64748b", "sortOrder": len(v.Groups), "revision": 1}
					group = text(g, "id")
					v.Groups = append(v.Groups, g)
				}
			}
			args := Item{"title": d["title"], "notes": defaultText(d, "notes", ""), "groupId": nullable(group)}
			if due := text(d, "dueDate"); due != "" {
				args["dueDate"] = due
				args["dueTimeZone"] = v.TimeZone
			}
			if parent := text(d, "parentUid"); parent != "" {
				for _, src := range v.Sources {
					if text(src, "providerKind") == provider && text(src, "externalId") == parent {
						args["parentId"] = src["targetId"]
					}
				}
			}
			result, e := apply(v, Mutation{Action: "todo.create", Data: args})
			if e != nil {
				return nil, e
			}
			item = result.(Item)
			if text(d, "status") == "completed" {
				item["status"] = "completed"
				item["completedAt"] = d["completedAt"]
			}
		}
		v.Sources = append(v.Sources, Item{"id": ID(), "targetType": target, "targetId": item["id"], "providerKind": provider, "externalId": external, "title": item["title"], "revision": 1})
		imported++
	}
	return Item{"imported": imported, "skipped": skipped}, nil
}
func defaultText(m Item, k, fallback string) string {
	v, ok := m[k].(string)
	if !ok {
		return fallback
	}
	return v
}
func validate(v *Snapshot) error {
	if _, e := time.LoadLocation(v.TimeZone); e != nil {
		return errCode("timezone_invalid")
	}
	if len(v.Calendars) == 0 {
		return errCode("calendar_missing")
	}
	ids := map[string]bool{}
	defaults := 0
	for _, list := range [][]Item{v.Calendars, v.Groups, v.Tags, v.Todos, v.Events, v.Reminders, v.Sources} {
		for _, x := range list {
			if e := validateFields(clone(x)); e != nil {
				return e
			}
			id := text(x, "id")
			if id == "" || ids[id] {
				return errCode("duplicate_id")
			}
			ids[id] = true
			if number(x, "revision") < 1 {
				return errCode("invalid_revision")
			}
		}
	}
	for _, list := range [][]Item{v.Calendars, v.Groups, v.Tags} {
		names := map[string]bool{}
		for _, x := range list {
			name := strings.TrimSpace(text(x, "name"))
			if name == "" || len([]rune(name)) > 500 {
				return errCode("title_length")
			}
			if names[name] {
				return errCode("duplicate_name")
			}
			names[name] = true
			if !validColor(text(x, "color")) {
				return errCode("invalid_color")
			}
		}
	}
	for _, c := range v.Calendars {
		if c["reminderMinutes"] != nil && (number(c, "reminderMinutes") < 0 || number(c, "reminderMinutes") > 10080) {
			return errCode("reminder_minutes")
		}
		if boolean(c, "isDefault") {
			defaults++
		}
	}
	if defaults != 1 {
		return errCode("default_calendar")
	}
	for _, t := range v.Todos {
		if strings.TrimSpace(text(t, "title")) == "" || len([]rune(text(t, "title"))) > 500 {
			return errCode("title_length")
		}
		if text(t, "status") != "open" && text(t, "status") != "completed" {
			return errCode("task_status")
		}
		if id := text(t, "groupId"); id != "" && find(v.Groups, id) == nil {
			return errCode("category_missing")
		}
		if t["dueAt"] != nil && text(t, "dueDate") != "" {
			return errCode("deadline_exclusive")
		}
		if date := text(t, "dueDate"); date != "" {
			if _, e := time.Parse("2006-01-02", date); e != nil {
				return errCode("date_invalid")
			}
		}
		if z := text(t, "dueTimeZone"); z != "" {
			if _, e := time.LoadLocation(z); e != nil {
				return errCode("timezone_invalid")
			}
		}
		seen := map[string]bool{text(t, "id"): true}
		for id := text(t, "parentId"); id != ""; {
			if seen[id] {
				return errCode("task_cycle")
			}
			seen[id] = true
			p := find(v.Todos, id)
			if p == nil || number(p, "deletedAt") != 0 {
				return errCode("parent_missing_or_trashed")
			}
			if text(p, "groupId") != text(t, "groupId") {
				return errCode("task_group_mismatch")
			}
			id = text(p, "parentId")
		}
		if p := text(t, "priority"); p != "low" && p != "medium" && p != "high" {
			return errCode("invalid_priority")
		}
		if number(t, "reminderMinutes") < 0 || number(t, "reminderMinutes") > 10080 {
			return errCode("reminder_minutes")
		}
	}
	for _, e := range v.Events {
		if number(e, "reminderMinutes") < -1 || number(e, "reminderMinutes") > 10080 {
			return errCode("reminder_minutes")
		}
		if find(v.Calendars, text(e, "calendarId")) == nil {
			return errCode("calendar_missing")
		}
		if strings.TrimSpace(text(e, "title")) == "" || len([]rune(text(e, "title"))) > 500 {
			return errCode("title_length")
		}
		if _, _, err := bounds(object(e, "time")); err != nil {
			return err
		}
		if id := text(e, "todoId"); id != "" && find(v.Todos, id) == nil {
			return errCode("task_missing")
		}
		if r := object(e, "recurrence"); r != nil {
			f := text(r, "frequency")
			if f != "daily" && f != "weekly" && f != "monthly" && f != "yearly" {
				return errCode("recurrence_invalid")
			}
			if number(r, "interval") < 1 || number(r, "interval") > 365 {
				return errCode("recurrence_invalid")
			}
			if n, ok := r["count"]; ok && n != nil && number(r, "count") < 1 {
				return errCode("recurrence_invalid")
			}
			for _, value := range append(array(r, "excludedDates"), r["until"]) {
				if value == nil || value == "" {
					continue
				}
				if _, e := time.Parse("2006-01-02", value.(string)); e != nil {
					return errCode("date_invalid")
				}
			}
		}
	}
	for _, r := range v.Reminders {
		if text(r, "targetType") != "todo" && text(r, "targetType") != "event" {
			return errCode("reminder_target_missing")
		}
		if r["triggerAt"] == nil {
			return errCode("reminder_target_missing")
		}
	}
	return nil
}
