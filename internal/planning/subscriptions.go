package planning

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Store) statuses() []Item {
	out := []Item{}
	for id, f := range s.disk.Feeds {
		u, _ := url.Parse(text(f, "url"))
		host := ""
		if u != nil {
			host = u.Hostname()
		}
		out = append(out, Item{"calendarId": id, "host": host, "refreshMinutes": f["refreshMinutes"], "nextAt": f["nextAt"], "lastSyncedAt": f["lastSyncedAt"], "lastError": f["lastError"]})
	}
	return out
}
func normalizedURL(raw string) (string, error) {
	if strings.HasPrefix(strings.ToLower(raw), "webcal://") {
		raw = "https://" + raw[9:]
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(raw) > 4096 {
		return "", errCode("subscription_url_invalid")
	}
	return u.String(), nil
}
func (s *Store) Subscription(action string, input Item) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.disk)
	id := text(input, "id")
	feed := next.Feeds[id]
	cal := find(next.Snapshot.Calendars, id)
	if action != "subscription.create" && (feed == nil || cal == nil) {
		return nil, errCode("subscription_missing")
	}
	minutes := number(input, "refreshMinutes")
	if minutes == 0 {
		minutes = 60
	}
	if !map[int64]bool{15: true, 30: true, 60: true, 180: true, 360: true, 720: true, 1440: true}[minutes] {
		return nil, errCode("subscription_interval_invalid")
	}
	switch action {
	case "subscription.create":
		u, e := normalizedURL(text(input, "url"))
		if e != nil {
			return nil, e
		}
		cal = calendar(text(input, "name"), defaultText(input, "color", "#0f766e"), false)
		cal["sourceKind"] = "subscription"
		cal["readOnly"] = true
		cal["reminderMinutes"] = nil
		id = text(cal, "id")
		next.Snapshot.Calendars = append(next.Snapshot.Calendars, cal)
		next.Feeds[id] = Item{"id": id, "url": u, "refreshMinutes": minutes, "nextAt": 0}
	case "subscription.update":
		for _, key := range []string{"name", "color"} {
			if value, ok := input[key]; ok {
				cal[key] = value
			}
		}
		if _, ok := input["refreshMinutes"]; ok {
			feed["refreshMinutes"] = minutes
		}
		if raw, ok := input["url"]; ok {
			u, e := normalizedURL(fmt.Sprint(raw))
			if e != nil {
				return nil, e
			}
			feed["url"] = u
			feed["nextAt"] = 0
		}
		touch(cal)
	case "subscription.refresh":
		feed["nextAt"] = 0
	case "subscription.delete":
		delete(next.Feeds, id)
		next.Snapshot.Calendars = remove(next.Snapshot.Calendars, map[string]bool{id: true})
		ids := map[string]bool{}
		for _, e := range next.Snapshot.Events {
			if text(e, "calendarId") == id {
				ids[text(e, "id")] = true
			}
		}
		next.Snapshot.Events = remove(next.Snapshot.Events, ids)
		cal = nil
	default:
		return nil, errCode("unknown_request")
	}
	if e := validate(&next.Snapshot); e != nil {
		return nil, e
	}
	reconcile(&next.Snapshot)
	next.Snapshot.Seq++
	if e := s.commit(next); e != nil {
		return nil, e
	}
	return cal, nil
}
func (s *Store) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()
	for {
		s.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Store) refreshDue(ctx context.Context) {
	s.mu.Lock()
	feeds := clone(s.disk.Feeds)
	s.mu.Unlock()
	for id, feed := range feeds {
		if ctx.Err() != nil {
			return
		}
		if number(feed, "nextAt") > time.Now().UnixMilli() {
			continue
		}
		entries, e := download(ctx, text(feed, "url"))
		s.mu.Lock()
		next := clone(s.disk)
		current := next.Feeds[id]
		if current == nil || text(current, "url") != text(feed, "url") {
			s.mu.Unlock()
			continue
		}
		current["nextAt"] = time.Now().UnixMilli() + number(current, "refreshMinutes")*60000
		if e != nil {
			current["lastError"] = "E:subscription_fetch_failed"
		} else {
			candidate := clone(next.Snapshot)
			if err := syncFeed(&candidate, id, entries); err != nil {
				current["lastError"] = "E:subscription_not_ics"
			} else {
				next.Snapshot = candidate
				current["lastError"] = nil
				current["lastSyncedAt"] = time.Now().UnixMilli()
			}
		}
		next.Snapshot.Seq++
		if validate(&next.Snapshot) == nil {
			_ = s.commit(next)
		}
		s.mu.Unlock()
	}
}
func download(ctx context.Context, raw string) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, e
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024+1))
	if e != nil || len(b) > 10*1024*1024 {
		return nil, errCode("subscription_too_large")
	}
	return parseICS(string(b))
}
func syncFeed(v *Snapshot, id string, entries []Item) error {
	keep := map[string]bool{}
	for _, entry := range entries {
		external := id + ":" + text(entry, "uid")
		var event Item
		for _, src := range v.Sources {
			if text(src, "providerKind") == "ics-subscription" && text(src, "externalId") == external {
				event = find(v.Events, text(src, "targetId"))
				break
			}
		}
		if event == nil {
			event = newEvent(Item{"calendarId": id, "title": entry["title"], "notes": entry["notes"], "time": entry["time"]})
			v.Events = append(v.Events, event)
			v.Sources = append(v.Sources, Item{"id": ID(), "targetType": "event", "targetId": event["id"], "providerKind": "ics-subscription", "externalId": external, "title": entry["title"], "revision": 1})
		} else {
			for _, key := range []string{"title", "notes", "time"} {
				event[key] = entry[key]
			}
			touch(event)
		}
		keep[text(event, "id")] = true
	}
	removed := map[string]bool{}
	for _, event := range v.Events {
		if text(event, "calendarId") == id && !keep[text(event, "id")] {
			removed[text(event, "id")] = true
		}
	}
	v.Events = remove(v.Events, removed)
	sources := []Item{}
	for _, src := range v.Sources {
		if !removed[text(src, "targetId")] {
			sources = append(sources, src)
		}
	}
	v.Sources = sources
	return validate(v)
}
