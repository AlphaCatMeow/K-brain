package planning

import (
	"bytes"
	"encoding/binary"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

type icsTransition struct {
	at       int64
	from, to int32
	daylight bool
}

func icsZones(cal *ical.Calendar, firstYear, lastYear int) (map[string]*time.Location, error) {
	zones := map[string]*time.Location{}
	for _, c := range cal.Children {
		if c.Name != "VTIMEZONE" {
			continue
		}
		name, _ := c.Props.Text("TZID")
		if name == "" || zones[name] != nil {
			return nil, errCode("timezone_invalid")
		}
		var transitions []icsTransition
		for _, obs := range c.Children {
			if obs.Name != "STANDARD" && obs.Name != "DAYLIGHT" {
				return nil, errCode("timezone_invalid")
			}
			fp, tp := obs.Props.Get("TZOFFSETFROM"), obs.Props.Get("TZOFFSETTO")
			if fp == nil || tp == nil {
				return nil, errCode("timezone_invalid")
			}
			from, e := icsOffset(fp.Value)
			if e != nil {
				return nil, e
			}
			to, e := icsOffset(tp.Value)
			if e != nil {
				return nil, e
			}
			start, e := obs.Props.DateTime("DTSTART", time.UTC)
			if e != nil {
				return nil, e
			}
			dates := []time.Time{start}
			opt, e := obs.Props.RecurrenceRule()
			if e != nil {
				return nil, e
			}
			if opt != nil {
				opt.Dtstart = start
				rule, e := rrule.NewRRule(*opt)
				if e != nil {
					return nil, e
				}
				iter := rule.Iterator()
				var prior time.Time
				for n := 0; ; n++ {
					at, ok := iter()
					if !ok || at.Year() > lastYear {
						break
					}
					if n >= 10000 {
						return nil, errCode("subscription_too_large")
					}
					if at.Year() >= firstYear {
						dates = append(dates, at)
					} else {
						prior = at
					}
				}
				if !prior.IsZero() {
					dates = append(dates, prior)
				}
			}
			for _, p := range obs.Props["RDATE"] {
				for _, v := range strings.Split(p.Value, ",") {
					cp := p
					cp.Value = v
					at, e := cp.DateTime(time.UTC)
					if e != nil {
						return nil, e
					}
					dates = append(dates, at)
				}
			}
			for _, at := range dates {
				transitions = append(transitions, icsTransition{at.Unix() - int64(from), from, to, obs.Name == "DAYLIGHT"})
			}
		}
		if len(transitions) == 0 {
			return nil, errCode("timezone_invalid")
		}
		sort.Slice(transitions, func(i, j int) bool { return transitions[i].at < transitions[j].at })
		unique := transitions[:0]
		for _, v := range transitions {
			if len(unique) > 0 && unique[len(unique)-1].at == v.at {
				if unique[len(unique)-1] != v {
					return nil, errCode("timezone_invalid")
				}
				continue
			}
			unique = append(unique, v)
		}
		loc, e := time.LoadLocationFromTZData(name, icsTZData(unique))
		if e != nil {
			return nil, e
		}
		zones[name] = loc
	}
	return zones, nil
}

// TZif v2 carries 64-bit transition instants and does not depend on installed zone files.
func icsTZData(transitions []icsTransition) []byte {
	var b bytes.Buffer
	write := func(v any) { _ = binary.Write(&b, binary.BigEndian, v) }
	header := func(count, types int) {
		b.WriteString("TZif2")
		b.Write(make([]byte, 15))
		for _, n := range []int{0, 0, 0, count, types, 4} {
			write(int32(n))
		}
	}
	header(0, 1)
	write(transitions[0].from)
	b.Write([]byte{0, 0})
	b.WriteString("LOC\x00")
	types := []icsTransition{{to: transitions[0].from}}
	indices := make([]byte, len(transitions))
	for i, t := range transitions {
		index := -1
		for j, v := range types {
			if v.to == t.to && v.daylight == t.daylight {
				index = j
				break
			}
		}
		if index < 0 {
			index = len(types)
			types = append(types, t)
		}
		if index > 255 {
			return nil
		}
		indices[i] = byte(index)
	}
	header(len(transitions), len(types))
	for _, t := range transitions {
		write(t.at)
	}
	b.Write(indices)
	for _, t := range types {
		write(t.to)
		dst := byte(0)
		if t.daylight {
			dst = 1
		}
		b.Write([]byte{dst, 0})
	}
	b.WriteString("LOC\x00\n\n")
	return b.Bytes()
}
