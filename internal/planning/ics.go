package planning

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

type icsDuration struct {
	days    int
	elapsed time.Duration
}

var icsDurationPattern = regexp.MustCompile(`^\+?P(?:(\d+)W|(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?)$`)

func parseICSDuration(raw string) (icsDuration, error) {
	m := icsDurationPattern.FindStringSubmatch(raw)
	if m == nil || raw == "P" || strings.HasSuffix(raw, "T") {
		return icsDuration{}, errCode("time_invalid")
	}
	v := make([]int64, 5)
	for i := range v {
		if m[i+1] != "" {
			n, e := strconv.ParseInt(m[i+1], 10, 32)
			if e != nil || n > 366000 {
				return icsDuration{}, errCode("time_invalid")
			}
			v[i] = n
		}
	}
	d := icsDuration{days: int(v[0]*7 + v[1]), elapsed: time.Duration(v[2])*time.Hour + time.Duration(v[3])*time.Minute + time.Duration(v[4])*time.Second}
	if d.days == 0 && d.elapsed == 0 {
		return d, errCode("time_invalid")
	}
	return d, nil
}
func (d icsDuration) end(start time.Time) time.Time {
	return start.AddDate(0, 0, d.days).Add(d.elapsed)
}

type icsReader struct{ zones map[string]*time.Location }

func (r icsReader) date(p *ical.Prop, fallback *time.Location) (time.Time, error) {
	if p == nil {
		return time.Time{}, errCode("time_invalid")
	}
	zone := p.Params.Get("TZID")
	if loc := r.zones[zone]; loc != nil {
		cp := *p
		cp.Params = make(ical.Params)
		for k, v := range p.Params {
			if k != "TZID" {
				cp.Params[k] = v
			}
		}
		return cp.DateTime(loc)
	}
	return p.DateTime(fallback)
}

func parseICS(raw string) ([]Item, error) {
	now := time.Now()
	return parseICSWindow(raw, now.AddDate(0, 0, -30), now.AddDate(1, 0, 0))
}

func parseICSWindow(raw string, from, to time.Time) ([]Item, error) {
	cal, err := ical.NewDecoder(strings.NewReader(strings.TrimPrefix(raw, "\ufeff"))).Decode()
	if err != nil {
		return nil, err
	}
	if method, _ := cal.Props.Text("METHOD"); method == "CANCEL" {
		return nil, errCode("subscription_not_ics")
	}
	zones, err := icsZones(cal, from.Year()-2, to.Year()+2)
	if err != nil {
		return nil, err
	}
	r := icsReader{zones: zones}
	out := []Item{}
	overrides := map[string]bool{}
	key := func(uid string, at time.Time) string { return uid + ":" + at.UTC().Format(time.RFC3339) }
	for _, c := range cal.Children {
		if c.Name == "VEVENT" {
			if p := c.Props.Get("RECURRENCE-ID"); p != nil {
				if p.Params.Get("RANGE") != "" {
					return nil, errCode("subscription_not_ics")
				}
				at, e := r.date(p, time.UTC)
				if e != nil {
					return nil, e
				}
				uid, _ := c.Props.Text("UID")
				overrides[key(uid, at)] = true
			}
		}
	}
	for _, c := range cal.Children {
		if c.Name != "VEVENT" {
			continue
		}
		p := c.Props
		uid, _ := p.Text("UID")
		if uid == "" {
			return nil, errCode("import_missing_uid")
		}
		status, _ := p.Text("STATUS")
		if status == "CANCELLED" {
			continue
		}
		start, e := r.date(p.Get("DTSTART"), time.UTC)
		if e != nil {
			return nil, e
		}
		allDay := p.Get("DTSTART").ValueType() == ical.ValueDate
		duration := icsDuration{elapsed: time.Hour}
		if allDay {
			duration = icsDuration{days: 1}
		}
		if endProp := p.Get("DTEND"); endProp != nil {
			if p.Get("DURATION") != nil {
				return nil, errCode("time_invalid")
			}
			end, e := r.date(endProp, start.Location())
			if e != nil || !end.After(start) || (endProp.ValueType() == ical.ValueDate) != allDay {
				return nil, errCode("time_invalid")
			}
			duration = icsDuration{elapsed: end.Sub(start)}
			if allDay {
				if endProp.ValueType() != ical.ValueDate {
					return nil, errCode("time_invalid")
				}
				a, _ := time.Parse("2006-01-02", start.Format("2006-01-02"))
				b, _ := time.Parse("2006-01-02", end.Format("2006-01-02"))
				duration = icsDuration{days: int(b.Sub(a) / (24 * time.Hour))}
			}
		} else if dp := p.Get("DURATION"); dp != nil {
			duration, e = parseICSDuration(dp.Value)
			if e != nil {
				return nil, e
			}
			if allDay && strings.Contains(dp.Value, "T") {
				return nil, errCode("time_invalid")
			}
		}
		dates := map[int64]time.Time{start.Unix(): start}
		periods := map[int64]icsDuration{}
		rule, e := p.RecurrenceRule()
		if e != nil {
			return nil, e
		}
		if rule != nil {
			rule.Dtstart = start
			rr, e := rrule.NewRRule(*rule)
			if e != nil {
				return nil, e
			}
			iter := rr.Iterator()
			for n := 0; ; n++ {
				at, ok := iter()
				if !ok || at.After(to) {
					break
				}
				if n >= 100000 {
					return nil, errCode("subscription_too_large")
				}
				if duration.end(at).After(from) {
					dates[at.Unix()] = at
				}
			}
		}
		for _, prop := range p["RDATE"] {
			for _, value := range strings.Split(prop.Value, ",") {
				cp := prop
				parts := strings.Split(value, "/")
				period := prop.ValueType() == ical.ValuePeriod
				if (period && len(parts) != 2) || (!period && len(parts) != 1) || (!period && (prop.ValueType() == ical.ValueDate) != allDay) {
					return nil, errCode("time_invalid")
				}
				cp.Value = parts[0]
				if len(parts) > 1 {
					cp.Params = ical.Params{}
					for k, v := range prop.Params {
						if k != "VALUE" {
							cp.Params[k] = v
						}
					}
				}
				at, e := r.date(&cp, start.Location())
				if e != nil {
					return nil, e
				}
				dates[at.Unix()] = at
				if len(parts) > 2 || (len(parts) > 1 && allDay) {
					return nil, errCode("time_invalid")
				}
				if len(parts) == 2 {
					d := icsDuration{}
					if strings.HasPrefix(parts[1], "P") || strings.HasPrefix(parts[1], "+P") {
						d, e = parseICSDuration(parts[1])
					} else {
						cp.Value = parts[1]
						var end time.Time
						end, e = r.date(&cp, at.Location())
						d.elapsed = end.Sub(at)
					}
					if e != nil || !d.end(at).After(at) {
						return nil, errCode("time_invalid")
					}
					periods[at.Unix()] = d
				}
			}
		}
		for _, prop := range p["EXDATE"] {
			for _, value := range strings.Split(prop.Value, ",") {
				cp := prop
				cp.Value = value
				at, e := r.date(&cp, start.Location())
				if e != nil {
					return nil, e
				}
				delete(dates, at.Unix())
			}
		}
		title, _ := p.Text("SUMMARY")
		if title == "" {
			title = "(untitled)"
		}
		notes, _ := p.Text("DESCRIPTION")
		ordered := make([]time.Time, 0, len(dates))
		for _, at := range dates {
			ordered = append(ordered, at)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })
		for _, at := range ordered {
			id := key(uid, at)
			if p.Get("RECURRENCE-ID") == nil && overrides[id] {
				continue
			}
			if op := p.Get("RECURRENCE-ID"); op != nil {
				original, e := r.date(op, start.Location())
				if e != nil {
					return nil, e
				}
				id = key(uid, original)
			}
			d := duration
			if custom, ok := periods[at.Unix()]; ok {
				d = custom
			}
			end := d.end(at)
			if !end.After(from) || at.After(to) {
				continue
			}
			zone := start.Location().String()
			if zones[zone] != nil {
				zone = "UTC"
			}
			tm := Item{"kind": "timed", "startAt": at.UnixMilli(), "endAt": end.UnixMilli(), "timeZone": zone}
			if allDay {
				tm = Item{"kind": "allDay", "startDate": at.Format("2006-01-02"), "endDateExclusive": end.Format("2006-01-02"), "timeZone": zone}
			}
			out = append(out, Item{"uid": id, "title": title, "notes": notes, "time": tm})
			if len(out) > 10000 {
				return nil, errCode("subscription_too_large")
			}
		}
	}
	return out, nil
}

func icsOffset(raw string) (int32, error) {
	if (len(raw) != 5 && len(raw) != 7) || (raw[0] != '+' && raw[0] != '-') {
		return 0, fmt.Errorf("invalid UTC offset")
	}
	h, e := strconv.Atoi(raw[1:3])
	m, e2 := strconv.Atoi(raw[3:5])
	s := 0
	var e3 error
	if len(raw) == 7 {
		s, e3 = strconv.Atoi(raw[5:7])
	}
	if e != nil || e2 != nil || e3 != nil || h < 0 || h > 23 || m < 0 || m > 59 || s < 0 || s > 59 {
		return 0, errCode("timezone_invalid")
	}
	n := int32(h*3600 + m*60 + s)
	if raw[0] == '-' {
		n = -n
	}
	return n, nil
}
