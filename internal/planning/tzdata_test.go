package planning

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDesktopTimeZones(t *testing.T) {
	t.Setenv("ZONEINFO", filepath.Join(t.TempDir(), "missing-zoneinfo"))
	s := openTest(t)
	for _, zone := range []string{"Asia/Shanghai", "Asia/Kolkata", "Europe/Paris", "America/New_York", "Pacific/Auckland", "UTC"} {
		if _, err := s.SetTimeZone(zone); err != nil {
			t.Fatalf("%s: %v", zone, err)
		}
		next, err := Open(s.path)
		if err != nil {
			t.Fatalf("reopen %s: %v", zone, err)
		}
		if got := text(next.TimeZoneSettings(), "timeZone"); got != zone {
			t.Fatalf("timezone = %s, want %s", got, zone)
		}
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_, winter := time.Date(2026, 1, 1, 12, 0, 0, 0, loc).Zone()
	_, summer := time.Date(2026, 7, 1, 12, 0, 0, 0, loc).Zone()
	if winter != -5*3600 || summer != -4*3600 {
		t.Fatal("DST offsets lost")
	}
	if _, err := s.SetTimeZone("not/a-zone"); err == nil {
		t.Fatal("invalid zone accepted")
	}
}
