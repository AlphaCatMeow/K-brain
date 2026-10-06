// Package planning owns persistent calendars and tasks shared by all backend clients.
package planning

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type Item = map[string]any
type Snapshot struct {
	Seq            uint64 `json:"seq"`
	TimeZone       string `json:"timeZone"`
	Calendars      []Item `json:"calendars"`
	Todos          []Item `json:"todos"`
	Groups         []Item `json:"groups"`
	Tags           []Item `json:"tags"`
	Events         []Item `json:"events"`
	EventMasters   []Item `json:"eventMasters,omitempty"`
	TodoSchedules  []Item `json:"todoSchedules"`
	Reminders      []Item `json:"reminders"`
	Sources        []Item `json:"sources"`
	Subscriptions  []Item `json:"subscriptions"`
	DefaultGroupID string `json:"defaultGroupId,omitempty"`
	MyTasksColor   string `json:"myTasksColor,omitempty"`
}
type Mutation struct {
	RequestID        string  `json:"requestId"`
	Action           string  `json:"action"`
	ID               string  `json:"id,omitempty"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Data             Item    `json:"data"`
}
type Result struct {
	Status   string `json:"status"`
	Seq      uint64 `json:"seq"`
	Item     any    `json:"item"`
	Replayed bool   `json:"replayed"`
	Message  string `json:"message,omitempty"`
}
type receipt struct {
	Request Mutation `json:"request"`
	Result  Result   `json:"result"`
	At      int64    `json:"at"`
}
type disk struct {
	Version    int                `json:"version"`
	Snapshot   Snapshot           `json:"snapshot"`
	Requests   map[string]receipt `json:"requests"`
	Migrations map[string]bool    `json:"migrations"`
	Feeds      map[string]Item    `json:"feeds"`
}
type Store struct {
	mu   sync.Mutex
	path string
	disk disk
}

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}
func text(m Item, k string) string { s, _ := m[k].(string); return s }
func number(m Item, k string) int64 {
	switch n := m[k].(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case uint64:
		return int64(n)
	}
	return 0
}
func object(m Item, k string) Item  { v, _ := m[k].(map[string]any); return v }
func array(m Item, k string) []any  { v, _ := m[k].([]any); return v }
func boolean(m Item, k string) bool { v, _ := m[k].(bool); return v }
func clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
func errCode(code string) error { return errors.New("E:" + code) }
func find(items []Item, id string) Item {
	for _, x := range items {
		if text(x, "id") == id {
			return x
		}
	}
	return nil
}
func remove(items []Item, ids map[string]bool) []Item {
	out := make([]Item, 0, len(items))
	for _, x := range items {
		if !ids[text(x, "id")] {
			out = append(out, x)
		}
	}
	return out
}
func touch(x Item) {
	x["revision"] = number(x, "revision") + 1
	if _, ok := x["updatedAt"]; ok {
		x["updatedAt"] = time.Now().UnixMilli()
	}
}
func calendar(name, color string, def bool) Item {
	return Item{"id": ID(), "name": name, "color": color, "sortOrder": 0, "isDefault": def, "reminderMinutes": 0, "sourceKind": "local", "readOnly": false, "revision": 1}
}
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, e := os.ReadFile(path)
	if e == nil {
		if e = json.Unmarshal(b, &s.disk); e != nil {
			return nil, e
		}
		if s.disk.Version != 1 {
			return nil, errCode("data_version_newer")
		}
		normalize(&s.disk.Snapshot)
		if e = validate(&s.disk.Snapshot); e != nil {
			return nil, e
		}
		if s.disk.Requests == nil {
			s.disk.Requests = map[string]receipt{}
		}
		if s.disk.Migrations == nil {
			s.disk.Migrations = map[string]bool{}
		}
		if s.disk.Feeds == nil {
			s.disk.Feeds = map[string]Item{}
		}
		return s, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	z := time.Local.String()
	if _, e = time.LoadLocation(z); e != nil || z == "Local" {
		z = systemZone()
	}
	s.disk = disk{Version: 1, Snapshot: Snapshot{TimeZone: z, Calendars: []Item{calendar("工作", "#2563eb", true), calendar("个人", "#0f766e", false)}, Todos: []Item{}, Groups: []Item{}, Tags: []Item{}, Events: []Item{}, Reminders: []Item{}, Sources: []Item{}, Subscriptions: []Item{}, TodoSchedules: []Item{}}, Requests: map[string]receipt{}, Migrations: map[string]bool{}, Feeds: map[string]Item{}}
	if e = s.save(s.disk); e != nil {
		return nil, e
	}
	return s, nil
}

func systemZone() string {
	if z := os.Getenv("TZ"); z != "" {
		if _, e := time.LoadLocation(z); e == nil {
			return z
		}
	}
	if path, e := filepath.EvalSymlinks("/etc/localtime"); e == nil {
		if _, z, ok := strings.Cut(path, "zoneinfo/"); ok {
			if _, e = time.LoadLocation(z); e == nil {
				return z
			}
		}
	}
	return "UTC"
}
func (s *Store) save(next disk) error {
	if e := os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".planning-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), s.path)
}
func (s *Store) commit(next disk) error {
	if e := s.save(next); e != nil {
		return e
	}
	s.disk = next
	return nil
}
func (s *Store) Query(from, to int64) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := clone(s.disk.Snapshot)
	v.Subscriptions = s.statuses()
	v.TodoSchedules = []Item{}
	for _, t := range v.Todos {
		n, minutes := int64(0), int64(0)
		future := false
		for _, e := range v.Events {
			if text(e, "todoId") == text(t, "id") && active(&v, e) {
				a, b, err := bounds(object(e, "time"))
				if err != nil {
					return v, err
				}
				n++
				minutes += (b - a) / 60000
				future = future || b > time.Now().UnixMilli()
			}
		}
		v.TodoSchedules = append(v.TodoSchedules, Item{"todoId": t["id"], "count": n, "minutes": minutes, "hasFuture": future})
	}
	if from != 0 || to != 0 {
		if to <= from || to-from > 366*86400000 {
			return v, errCode("query_range")
		}
		v.EventMasters = clone(v.Events)
		var err error
		v.Events, err = expand(&v, from, to)
		if err != nil {
			return v, err
		}
	}
	return v, nil
}
func (s *Store) Mutate(m Mutation) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(m.RequestID) == "" || len(m.RequestID) > 200 {
		return Result{}, errCode("request_id_required")
	}
	if m.Data == nil {
		return Result{}, errCode("update_not_object")
	}
	m = clone(m)
	if e := validateFields(m.Data); e != nil {
		return Result{}, e
	}
	if old, ok := s.disk.Requests[m.RequestID]; ok {
		if !reflect.DeepEqual(clone(old.Request), clone(m)) {
			return Result{}, errCode("request_id_reused")
		}
		out := clone(old.Result)
		out.Replayed = true
		return out, nil
	}
	next := clone(s.disk)
	v := &next.Snapshot
	if !strings.HasSuffix(m.Action, ".create") && m.Action != "todo.import" && !strings.HasPrefix(m.Action, "mytasks.") {
		x := lookup(v, m.Action, m.ID)
		if x == nil {
			return Result{}, errCode("item_missing")
		}
		if m.ExpectedRevision == nil || *m.ExpectedRevision != uint64(number(x, "revision")) {
			return Result{Status: "conflict", Seq: v.Seq, Item: clone(x), Message: "E:conflict"}, nil
		}
	}
	item, e := apply(v, m)
	if e != nil {
		return Result{}, e
	}
	if e = validate(v); e != nil {
		return Result{}, e
	}
	reconcile(v)
	v.Seq++
	out := Result{Status: "ok", Seq: v.Seq, Item: clone(item)}
	next.Requests[m.RequestID] = receipt{m, out, time.Now().UnixMilli()}
	for id, r := range next.Requests {
		if r.At < time.Now().AddDate(0, 0, -30).UnixMilli() {
			delete(next.Requests, id)
		}
	}
	if e = s.commit(next); e != nil {
		return Result{}, e
	}
	return out, nil
}
func lookup(v *Snapshot, action, id string) Item {
	var list []Item
	switch strings.Split(action, ".")[0] {
	case "calendar":
		list = v.Calendars
	case "todo":
		list = v.Todos
	case "event":
		list = v.Events
	case "group":
		list = v.Groups
	case "tag":
		list = v.Tags
	case "reminder":
		list = v.Reminders
	case "source":
		list = v.Sources
	}
	return find(list, id)
}
func (s *Store) Import(in Snapshot, source string, subscriptions []Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.disk)
	if source != "" && next.Migrations[source] {
		return Item{"status": "already_imported", "seq": next.Snapshot.Seq}, nil
	}
	in = clone(in)
	normalize(&in)
	if err := validate(&in); err != nil {
		return nil, err
	}
	if source != "" {
		// First migration keeps every legacy ID. Subsequent sources merge only disjoint records.
		if next.Snapshot.Seq == 0 {
			in.Seq = 1
			next.Snapshot = in
		} else {
			pairs := [][2]*[]Item{{&next.Snapshot.Calendars, &in.Calendars}, {&next.Snapshot.Todos, &in.Todos}, {&next.Snapshot.Events, &in.Events}, {&next.Snapshot.Groups, &in.Groups}, {&next.Snapshot.Tags, &in.Tags}, {&next.Snapshot.Reminders, &in.Reminders}, {&next.Snapshot.Sources, &in.Sources}}
			for _, pair := range pairs {
				for _, item := range *pair[1] {
					if old := find(*pair[0], text(item, "id")); old != nil {
						if !reflect.DeepEqual(old, item) {
							return nil, errCode("migration_conflict")
						}
						continue
					}
					if pair[0] == &next.Snapshot.Calendars {
						item["isDefault"] = false
					}
					*pair[0] = append(*pair[0], item)
				}
			}
			next.Snapshot.Seq++
		}
		for _, feed := range subscriptions {
			id := text(feed, "id")
			if find(next.Snapshot.Calendars, id) == nil {
				return nil, errCode("subscription_missing")
			}
			next.Feeds[id] = feed
		}
		next.Migrations[source] = true
	} else {
		in.Seq = next.Snapshot.Seq + 1
		next.Snapshot = in
		next.Requests = map[string]receipt{}
		for id := range next.Feeds {
			if find(in.Calendars, id) == nil {
				delete(next.Feeds, id)
			}
		}
	}
	next.Snapshot.EventMasters = nil
	next.Snapshot.Subscriptions = []Item{}
	reconcile(&next.Snapshot)
	if err := validate(&next.Snapshot); err != nil {
		return nil, err
	}
	if err := s.commit(next); err != nil {
		return nil, err
	}
	return Item{"status": "ok", "seq": next.Snapshot.Seq}, nil
}
func sorted(items []Item) {
	sort.SliceStable(items, func(i, j int) bool { return number(items[i], "sortOrder") < number(items[j], "sortOrder") })
}
