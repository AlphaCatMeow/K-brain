package planning

import "time"

func bounds(t Item) (int64, int64, error) {
	zone, e := time.LoadLocation(text(t, "timeZone"))
	if e != nil {
		return 0, 0, errCode("timezone_invalid")
	}
	var a, b int64
	switch text(t, "kind") {
	case "timed":
		a = number(t, "startAt")
		b = number(t, "endAt")
	case "allDay":
		start, e := time.ParseInLocation("2006-01-02", text(t, "startDate"), zone)
		if e != nil {
			return 0, 0, errCode("date_invalid")
		}
		end, e := time.ParseInLocation("2006-01-02", text(t, "endDateExclusive"), zone)
		if e != nil {
			return 0, 0, errCode("date_invalid")
		}
		a = start.UnixMilli()
		b = end.UnixMilli()
	default:
		return 0, 0, errCode("time_invalid")
	}
	if b <= a {
		return 0, 0, errCode("time_invalid")
	}
	return a, b, nil
}
func active(v *Snapshot, e Item) bool {
	if number(e, "deletedAt") != 0 {
		return false
	}
	for _, key := range []string{"todoId", "seriesId"} {
		list := v.Events
		if key == "todoId" {
			list = v.Todos
		}
		if p := find(list, text(e, key)); p != nil && number(p, "deletedAt") != 0 {
			return false
		}
	}
	return true
}
func onDate(t Item, date string) (Item, error) {
	a, b, e := bounds(t)
	if e != nil {
		return nil, e
	}
	zone, _ := time.LoadLocation(text(t, "timeZone"))
	day, e := time.ParseInLocation("2006-01-02", date, zone)
	if e != nil {
		return nil, errCode("date_invalid")
	}
	out := clone(t)
	if text(t, "kind") == "allDay" {
		first, _ := time.Parse("2006-01-02", text(t, "startDate"))
		end, _ := time.Parse("2006-01-02", text(t, "endDateExclusive"))
		out["startDate"] = date
		out["endDateExclusive"] = day.AddDate(0, 0, int(end.Sub(first).Hours()/24)).Format("2006-01-02")
	} else {
		old := time.UnixMilli(a).In(zone)
		start := time.Date(day.Year(), day.Month(), day.Day(), old.Hour(), old.Minute(), old.Second(), old.Nanosecond(), zone)
		if start.Hour() != old.Hour() || start.Minute() != old.Minute() {
			return nil, errCode("recurrence_dst_gap")
		}
		out["startAt"] = start.UnixMilli()
		out["endAt"] = start.UnixMilli() + b - a
	}
	return out, nil
}
func occurrences(event Item, from, to int64) ([]Item, error) {
	result := []Item{}
	a, b, e := bounds(object(event, "time"))
	if e != nil {
		return nil, e
	}
	r := object(event, "recurrence")
	if r == nil {
		if a < to && b > from {
			result = append(result, clone(event))
		}
		return result, nil
	}
	z, _ := time.LoadLocation(text(object(event, "time"), "timeZone"))
	start := time.UnixMilli(a).In(z)
	first := time.Date(start.Year(), start.Month(), start.Day(), 12, 0, 0, 0, z)
	interval := int(number(r, "interval"))
	if interval < 1 {
		return nil, errCode("recurrence_invalid")
	}
	count := int64(0)
	for offset := 0; offset <= 36600; offset++ {
		day := first.AddDate(0, 0, offset)
		date := day.Format("2006-01-02")
		if day.UnixMilli() > to+86400000 {
			break
		}
		if until := text(r, "until"); until != "" && date > until {
			break
		}
		eligible := false
		switch text(r, "frequency") {
		case "daily":
			eligible = offset%interval == 0
		case "weekly":
			week := (offset + (int(first.Weekday())+6)%7) / 7
			weekday := day.Weekday() == first.Weekday()
			if len(array(r, "weekdays")) > 0 {
				weekday = false
				for _, w := range array(r, "weekdays") {
					if n, ok := w.(float64); ok && int(n) == (int(day.Weekday())+6)%7 {
						weekday = true
					}
				}
			}
			eligible = week%interval == 0 && weekday
		case "monthly":
			months := (day.Year()-first.Year())*12 + int(day.Month()-first.Month())
			eligible = months%interval == 0 && day.Day() == first.Day()
		case "yearly":
			eligible = (day.Year()-first.Year())%interval == 0 && day.Month() == first.Month() && day.Day() == first.Day()
		default:
			return nil, errCode("recurrence_invalid")
		}
		if !eligible {
			continue
		}
		count++
		if n := number(r, "count"); n > 0 && count > n {
			break
		}
		excluded := false
		for _, d := range array(r, "excludedDates") {
			if d == date {
				excluded = true
			}
		}
		if excluded {
			continue
		}
		mapped, e := onDate(object(event, "time"), date)
		if e != nil {
			continue
		}
		a, b, e = bounds(mapped)
		if e != nil {
			return nil, e
		}
		if a < to && b > from {
			instance := clone(event)
			instance["id"] = text(event, "id") + "@" + date
			instance["seriesId"] = event["id"]
			instance["originalDate"] = date
			instance["time"] = mapped
			result = append(result, instance)
			if len(result) > 10000 {
				return nil, errCode("query_range")
			}
		}
	}
	return result, nil
}
func expand(v *Snapshot, from, to int64) ([]Item, error) {
	out := []Item{}
	for _, event := range v.Events {
		if !active(v, event) {
			continue
		}
		items, e := occurrences(event, from, to)
		if e != nil {
			return nil, e
		}
		out = append(out, items...)
	}
	return out, nil
}
func recurringMutation(v *Snapshot, m Mutation, x Item) (any, error) {
	r := object(x, "recurrence")
	if r == nil {
		return nil, errCode("recurrence_required")
	}
	date := text(m.Data, "date")
	day, e := time.Parse("2006-01-02", date)
	if e != nil {
		return nil, errCode("date_invalid")
	}
	mapped, e := onDate(object(x, "time"), date)
	if e != nil {
		return nil, e
	}
	if m.Action != "event.restoreException" {
		probe := clone(x)
		rule := clone(r)
		rule["excludedDates"] = []any{}
		probe["recurrence"] = rule
		a, b, _ := bounds(mapped)
		list, err := occurrences(probe, a, b)
		if err != nil {
			return nil, err
		}
		valid := false
		for _, item := range list {
			if text(item, "originalDate") == date {
				valid = true
			}
		}
		if !valid {
			return nil, errCode("occurrence_missing")
		}
	}
	excluded := array(r, "excludedDates")
	exists := false
	for _, d := range excluded {
		exists = exists || d == date
	}
	if m.Action == "event.restoreException" {
		kept := []any{}
		for _, d := range excluded {
			if d != date {
				kept = append(kept, d)
			}
		}
		r["excludedDates"] = kept
		for _, ev := range v.Events {
			if text(ev, "seriesId") == m.ID && text(ev, "originalDate") == date {
				if text(m.Data, "exceptionId") != text(ev, "id") || number(m.Data, "exceptionRevision") != number(ev, "revision") {
					return nil, errCode("conflict")
				}
				v.Events = remove(v.Events, map[string]bool{text(ev, "id"): true})
				break
			}
		}
		touch(x)
		return x, nil
	}
	if exists {
		return nil, errCode("occurrence_excluded")
	}
	if m.Action == "event.split" {
		originalStart, _, _ := bounds(object(x, "time"))
		z, _ := time.LoadLocation(text(object(x, "time"), "timeZone"))
		first := time.UnixMilli(originalStart).In(z).Format("2006-01-02")
		if date <= first {
			return nil, errCode("split_first_occurrence")
		}
		next := clone(x)
		next["id"] = ID()
		next["revision"] = 1
		next["time"] = mapped
		for k, value := range m.Data {
			if k != "date" && k != "delete" {
				next[k] = value
			}
		}
		if count := number(r, "count"); count > 0 && m.Data["recurrence"] == nil {
			probe := clone(x)
			rule := clone(r)
			rule["excludedDates"] = []any{}
			probe["recurrence"] = rule
			cut, _, _ := bounds(mapped)
			prior, err := occurrences(probe, originalStart, cut)
			if err != nil {
				return nil, err
			}
			if nr := object(next, "recurrence"); nr != nil {
				nr["count"] = max(int64(1), count-int64(len(prior)))
			}
		}
		r["until"] = day.AddDate(0, 0, -1).Format("2006-01-02")
		delete(r, "count")
		touch(x)
		if boolean(m.Data, "delete") {
			ids := map[string]bool{}
			for _, ev := range v.Events {
				if text(ev, "seriesId") == m.ID && text(ev, "originalDate") >= date {
					ids[text(ev, "id")] = true
				}
			}
			v.Events = remove(v.Events, ids)
			return x, nil
		}
		for _, ev := range v.Events {
			if text(ev, "seriesId") == m.ID && text(ev, "originalDate") >= date {
				ev["seriesId"] = next["id"]
				touch(ev)
			}
		}
		v.Events = append(v.Events, next)
		return next, nil
	}
	r["excludedDates"] = append(excluded, date)
	touch(x)
	if boolean(m.Data, "delete") {
		return x, nil
	}
	instance := clone(x)
	instance["id"] = ID()
	instance["seriesId"] = m.ID
	instance["originalDate"] = date
	instance["recurrence"] = nil
	instance["time"] = mapped
	instance["revision"] = 1
	for _, k := range []string{"time", "title", "notes"} {
		if value, ok := m.Data[k]; ok {
			instance[k] = value
		}
	}
	v.Events = append(v.Events, instance)
	return instance, nil
}
func reconcile(v *Snapshot) {
	now := time.Now().UnixMilli()
	desired := map[string]bool{}
	kept := []Item{}
	for _, r := range v.Reminders {
		if lookup(v, text(r, "targetType")+".update", text(r, "targetId")) != nil {
			kept = append(kept, r)
		}
	}
	v.Reminders = kept
	for _, t := range v.Todos {
		if number(t, "deletedAt") != 0 || text(t, "status") == "completed" {
			continue
		}
		due := number(t, "dueAt")
		if date := text(t, "dueDate"); date != "" {
			z, e := time.LoadLocation(defaultText(t, "dueTimeZone", v.TimeZone))
			if e == nil {
				d, e := time.ParseInLocation("2006-01-02", date, z)
				if e == nil {
					due = d.UnixMilli()
				}
			}
		}
		if due > 0 && boolean(t, "dueReminder") {
			id := "todo:" + text(t, "id")
			desired[id] = true
			ensureReminder(v, id, "todo", t, due-number(t, "reminderMinutes")*60000, now)
		}
	}
	for _, ev := range v.Events {
		if !active(v, ev) {
			continue
		}
		if t := find(v.Todos, text(ev, "todoId")); t != nil && text(t, "status") == "completed" {
			continue
		}
		cal := find(v.Calendars, text(ev, "calendarId"))
		if cal["reminderMinutes"] == nil && ev["reminderMinutes"] == nil {
			continue
		}
		offset := number(cal, "reminderMinutes")
		if value, ok := ev["reminderMinutes"]; ok && value != nil {
			offset = number(ev, "reminderMinutes")
		}
		if offset < 0 {
			continue
		}
		instances := []Item{ev}
		if object(ev, "recurrence") != nil {
			instances, _ = occurrences(ev, now-86400000, now+32*86400000)
		}
		for _, instance := range instances {
			start, _, e := bounds(object(instance, "time"))
			if e == nil {
				id := "event:" + text(instance, "id")
				desired[id] = true
				ensureReminder(v, id, "event", ev, start-offset*60000, now)
			}
		}
	}
	for _, r := range v.Reminders {
		target := lookup(v, text(r, "targetType")+".update", text(r, "targetId"))
		complete := target == nil || number(target, "deletedAt") != 0 || text(target, "status") == "completed"
		if text(r, "targetType") == "event" {
			complete = complete || !active(v, target)
			if t := find(v.Todos, text(target, "todoId")); t != nil && text(t, "status") == "completed" {
				complete = true
			}
		}
		if text(r, "origin") != "manual" && !desired[text(r, "id")] {
			complete = true
		}
		if complete && text(r, "status") == "pending" {
			r["status"] = "completed"
			r["leaseUntil"] = nil
			touch(r)
		} else if !complete && text(r, "origin") == "manual" && text(r, "status") == "completed" && number(r, "triggerAt") > now {
			r["status"] = "pending"
			r["notifiedAt"] = nil
			r["leaseUntil"] = nil
			touch(r)
		}
	}
}
func ensureReminder(v *Snapshot, id, kind string, target Item, at, now int64) {
	r := find(v.Reminders, id)
	if r != nil {
		if text(r, "status") == "completed" && at > now {
			r["status"] = "pending"
			r["notifiedAt"] = nil
			r["leaseUntil"] = nil
			touch(r)
		}
		if text(r, "status") == "pending" && r["snoozedUntil"] == nil && number(r, "triggerAt") != at {
			r["triggerAt"] = at
			r["notifiedAt"] = nil
			if at < now {
				r["notifiedAt"] = now
			}
			r["leaseUntil"] = nil
			r["attempts"] = 0
			touch(r)
		}
		if r["title"] != target["title"] {
			r["title"] = target["title"]
			touch(r)
		}
		return
	}
	if at < now-86400000 {
		return
	}
	origin := "todo_due"
	if kind == "event" {
		origin = "event_start"
	}
	v.Reminders = append(v.Reminders, Item{"id": id, "targetType": kind, "targetId": target["id"], "title": target["title"], "origin": origin, "triggerAt": at, "status": "pending", "revision": 1, "attempts": 0, "nextAttemptAt": 0})
}
