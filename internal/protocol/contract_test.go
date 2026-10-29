package protocol

import "testing"

func TestNewBuildChannels(t *testing.T) {
	cases := []struct{ version, wantVersion, wantChannel string }{
		{"v1.0.0", "v1.0.0", "stable"},
		{"v0.107.6-beta.23", "v0.107.6-beta.23", "prerelease"},
		{"v0.200.0-rc.1", "v0.200.0-rc.1", "prerelease"},
		{"", "dev", "dev"},
		{" dev ", "dev", "dev"},
		{"51c827d3d56533550e67bc4f78ff7673fa7bd9a3", "51c827d3d56533550e67bc4f78ff7673fa7bd9a3", "dev"},
		{"894612f", "894612f", "dev"},
		{"v0.107.6-beta.23-1-g894612f", "v0.107.6-beta.23-1-g894612f", "dev"},
		{"v0.107.6-beta.23-dirty", "v0.107.6-beta.23-dirty", "dev"},
		{"k0-local", "k0-local", "dev"},
	}
	for _, tc := range cases {
		got := NewBuild(tc.version, "abc")
		if got.Version != tc.wantVersion || got.Channel != tc.wantChannel || got.Revision != "abc" {
			t.Errorf("NewBuild(%q) = %+v, want version=%q channel=%q", tc.version, got, tc.wantVersion, tc.wantChannel)
		}
	}
}
