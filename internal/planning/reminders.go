package planning

import (
	"reflect"
	"time"
)

// Claim leases due notifications so multiple desktop clients cannot deliver the same reminder.
func (s *Store) Claim() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.disk)
	before := clone(next.Snapshot)
	reconcile(&next.Snapshot)
	now := time.Now().UnixMilli()
	out := []Item{}
	for _, r := range next.Snapshot.Reminders {
		target := lookup(&next.Snapshot, text(r, "targetType")+".update", text(r, "targetId"))
		if target == nil || number(target, "deletedAt") != 0 || text(target, "status") == "completed" {
			continue
		}
		if text(r, "status") != "pending" || number(r, "notifiedAt") > 0 {
			continue
		}
		due := number(r, "triggerAt")
		if number(r, "snoozedUntil") > due {
			due = number(r, "snoozedUntil")
		}
		if due > now || number(r, "leaseUntil") > now || number(r, "nextAttemptAt") > now {
			continue
		}
		r["leaseUntil"] = now + 60000
		r["attempts"] = number(r, "attempts") + 1
		touch(r)
		out = append(out, clone(r))
	}
	if !reflect.DeepEqual(next.Snapshot, before) {
		next.Snapshot.Seq++
		if e := s.commit(next); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) Finish(in Item) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.disk)
	r := find(next.Snapshot.Reminders, text(in, "id"))
	if r == nil {
		return nil, errCode("item_missing")
	}
	if number(in, "leaseUntil") != number(r, "leaseUntil") || number(in, "revision") != number(r, "revision") {
		return nil, errCode("conflict")
	}
	if boolean(in, "success") {
		r["notifiedAt"] = time.Now().UnixMilli()
		r["status"] = "notified"
	} else {
		r["nextAttemptAt"] = time.Now().Add(time.Minute).UnixMilli()
	}
	r["leaseUntil"] = nil
	touch(r)
	next.Snapshot.Seq++
	return nil, s.commit(next)
}
func (s *Store) SetTimeZone(zone string) (any, error) {
	return s.UpdateTimeZone(zone, nil)
}

func (s *Store) TimeZoneSettings() Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeZoneSettings()
}

func (s *Store) timeZoneSettings() Item {
	preference := s.disk.Snapshot.TimeZone
	if s.disk.TimeZonePreference != nil {
		preference = *s.disk.TimeZonePreference
	}
	return Item{"preference": preference, "timeZone": s.disk.Snapshot.TimeZone, "revision": s.disk.TimeZoneRevision, "systemTimeZone": systemZone()}
}

func (s *Store) UpdateTimeZone(preference string, expected *uint64) (any, error) {
	zone := preference
	if zone == "" {
		zone = systemZone()
	}
	if _, e := time.LoadLocation(zone); e != nil {
		return nil, errCode("timezone_invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != nil && *expected != s.disk.TimeZoneRevision {
		return nil, errCode("conflict")
	}
	if s.disk.Snapshot.TimeZone == zone && s.disk.TimeZonePreference != nil && *s.disk.TimeZonePreference == preference {
		return s.timeZoneSettings(), nil
	}
	next := clone(s.disk)
	next.Snapshot.TimeZone = zone
	next.TimeZonePreference = &preference
	next.TimeZoneRevision++
	next.Snapshot.Seq++
	reconcile(&next.Snapshot)
	if e := s.commit(next); e != nil {
		return nil, e
	}
	return s.timeZoneSettings(), nil
}
