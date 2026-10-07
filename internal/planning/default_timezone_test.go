package planning

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestNewStoreDefaultsToAutomaticTimeZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")
	s := openTest(t)
	assertZone := func(store *Store, zone string) {
		t.Helper()
		settings := store.TimeZoneSettings()
		if settings["preference"] != "" || settings["timeZone"] != zone {
			t.Fatalf("automatic settings: %+v", settings)
		}
		data, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		var saved disk
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.TimeZonePreference == nil || *saved.TimeZonePreference != "" {
			t.Fatal("automatic preference not persisted")
		}
	}
	assertZone(s, "Asia/Shanghai")
	t.Setenv("TZ", "America/New_York")
	assertZone(s, "Asia/Shanghai")
	next, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	assertZone(next, "America/New_York")
}

func TestExistingCustomAndLegacyTimeZonesArePreserved(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "legacy"}[legacy], func(t *testing.T) {
			t.Setenv("TZ", "Asia/Shanghai")
			s := openTest(t)
			if _, err := s.UpdateTimeZone("Europe/Paris", nil); err != nil {
				t.Fatal(err)
			}
			if legacy {
				s.disk.TimeZonePreference = nil
				if err := s.save(s.disk); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TZ", "Asia/Tokyo")
			next, err := Open(s.path)
			if err != nil {
				t.Fatal(err)
			}
			settings := next.TimeZoneSettings()
			if settings["preference"] != "Europe/Paris" || settings["timeZone"] != "Europe/Paris" {
				t.Fatalf("custom zone changed: %+v", settings)
			}
			after, err := os.ReadFile(s.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("existing store rewritten")
			}
		})
	}
}
