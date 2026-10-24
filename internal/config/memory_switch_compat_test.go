package config

import (
	"strings"
	"testing"
)

func TestMemoryConfigOmitsDefaultOnSoOlderReleasesStillParse(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name    string
		enabled *bool
		written bool
	}{{"unset", nil, false}, {"on", &on, false}, {"off", &off, true}} {
		data, err := marshalConfig(&Config{Memory: MemoryConfig{Enabled: tc.enabled}, allowEmptySave: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(data), `"memory"`); got != tc.written {
			t.Fatalf("%s: memory key written=%v, want %v:\n%s", tc.name, got, tc.written, data)
		}
	}
}
