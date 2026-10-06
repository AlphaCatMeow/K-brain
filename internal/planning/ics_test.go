package planning

import (
	"strings"
	"testing"
	"time"
)

func calendarICS(body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\nEND:VCALENDAR\r\n"
}
func parseTestICS(t *testing.T, body string) []Item {
	t.Helper()
	from, _ := time.Parse("2006-01-02", "2026-01-01")
	out, e := parseICSWindow(calendarICS(body), from, from.AddDate(1, 0, 0))
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestICSRDatesPeriodsAndExclusions(t *testing.T) {
	out := parseTestICS(t, `BEGIN:VEVENT
UID:dates
DTSTART:20261006T090000Z
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=2
RDATE:20261007T090000Z,20261008T090000Z
RDATE;VALUE=PERIOD:20261009T090000Z/PT2H
EXDATE:20261007T090000Z
END:VEVENT`)
	if len(out) != 3 {
		t.Fatal(out)
	}
	last := object(out[2], "time")
	if number(last, "endAt")-number(last, "startAt") != 7200000 {
		t.Fatal(last)
	}
}

func TestICSDurationAcrossDSTAndAllDay(t *testing.T) {
	for _, tc := range []struct {
		duration string
		hours    int64
	}{{"P1D", 23}, {"PT24H", 24}} {
		out := parseTestICS(t, "BEGIN:VEVENT\nUID:dst\nDTSTART;TZID=America/New_York:20260307T120000\nDURATION:"+tc.duration+"\nEND:VEVENT")
		tm := object(out[0], "time")
		if number(tm, "endAt")-number(tm, "startAt") != tc.hours*3600000 {
			t.Fatal(tm)
		}
	}
	out := parseTestICS(t, "BEGIN:VEVENT\nUID:day\nDTSTART;VALUE=DATE:20261006\nDURATION:P2D\nRDATE;VALUE=DATE:20261010\nEND:VEVENT")
	if len(out) != 2 || text(object(out[1], "time"), "endDateExclusive") != "2026-10-12" {
		t.Fatal(out)
	}
}

const customZone = `BEGIN:VTIMEZONE
TZID:Custom/Eastern
BEGIN:DAYLIGHT
DTSTART:20250309T020000
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20251102T020000
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
END:STANDARD
END:VTIMEZONE`

func TestICSCustomTimezoneAndCancelledOverride(t *testing.T) {
	out := parseTestICS(t, customZone+`
BEGIN:VEVENT
UID:custom
DTSTART;TZID=Custom/Eastern:20260307T120000
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:custom
RECURRENCE-ID;TZID=Custom/Eastern:20260309T120000
STATUS:CANCELLED
END:VEVENT`)
	if len(out) != 2 {
		t.Fatal(out)
	}
	for i, want := range []string{"2026-03-07T17:00:00Z", "2026-03-08T16:00:00Z"} {
		tm := object(out[i], "time")
		if got := time.UnixMilli(number(tm, "startAt")).UTC().Format(time.RFC3339); got != want {
			t.Fatalf("got %s want %s", got, want)
		}
		if text(tm, "timeZone") != "UTC" {
			t.Fatal(tm)
		}
	}
}

func TestICSRejectsInvalidDurationAndEnd(t *testing.T) {
	for _, body := range []string{"DURATION:P", "DURATION:PT", "DURATION:-PT1H", "DURATION:PT0S", "DURATION:P9999999999D", "DTEND:broken", "DTEND:20261006T100000Z\nDURATION:PT1H", "RDATE;VALUE=PERIOD:20261009T090000Z/-PT1H"} {
		_, e := parseICSWindow(calendarICS("BEGIN:VEVENT\nUID:bad\nDTSTART:20261006T090000Z\n"+body+"\nEND:VEVENT"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
		if e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestICSIncludesOverlappingRecurrence(t *testing.T) {
	from := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	out, e := parseICSWindow(calendarICS("BEGIN:VEVENT\nUID:overlap\nDTSTART:20261001T090000Z\nDURATION:P2D\nRRULE:FREQ=DAILY;COUNT=5\nEND:VEVENT"), from, from.AddDate(0, 0, 1))
	if e != nil || len(out) != 2 {
		t.Fatalf("%v %v", out, e)
	}
}

func TestICSRejectsMismatchedDateTypes(t *testing.T) {
	for _, line := range []string{"DTEND;VALUE=DATE:20261007", "RDATE;VALUE=DATE:20261007", "RDATE;VALUE=PERIOD:20261007T090000Z", "RDATE:20261007T090000Z/PT1H"} {
		_, e := parseICSWindow(calendarICS("BEGIN:VEVENT\nUID:bad\nDTSTART:20261006T090000Z\n"+line+"\nEND:VEVENT"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
		if e == nil {
			t.Fatal("accepted", line)
		}
	}
}

func TestICSHistoricalCustomZone(t *testing.T) {
	zone := `BEGIN:VTIMEZONE
TZID:Past/Zone
BEGIN:STANDARD
DTSTART:20001029T020000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU;UNTIL=20021027T020000Z
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:20000326T020000
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;UNTIL=20030330T020000Z
END:DAYLIGHT
END:VTIMEZONE`
	out := parseTestICS(t, zone+"\nBEGIN:VEVENT\nUID:past\nDTSTART;TZID=Past/Zone:20261006T090000\nDURATION:PT1H\nEND:VEVENT")
	got := time.UnixMilli(number(object(out[0], "time"), "startAt")).UTC().Format(time.RFC3339)
	if got != "2026-10-06T07:00:00Z" {
		t.Fatal(got)
	}
}

func TestICSRejectsUnsupportedRangeAndMalformedZones(t *testing.T) {
	for _, body := range []string{
		"BEGIN:VEVENT\nUID:range\nRECURRENCE-ID;RANGE=THISANDFUTURE:20261006T090000Z\nDTSTART:20261006T100000Z\nEND:VEVENT",
		"BEGIN:VTIMEZONE\nTZID:Empty\nEND:VTIMEZONE",
		"METHOD:CANCEL\nBEGIN:VEVENT\nUID:cancel\nDTSTART:20261006T090000Z\nEND:VEVENT",
	} {
		_, e := parseICSWindow(calendarICS(body), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
		if e == nil {
			t.Fatal("accepted", body)
		}
	}
	for _, offset := range []string{"+00-1", "+2400", "+0060", "+010060", "garbage"} {
		if _, e := icsOffset(offset); e == nil {
			t.Fatal("accepted", offset)
		}
	}
}
