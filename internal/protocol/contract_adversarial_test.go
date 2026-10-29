package protocol

import "testing"

// Adversarial review: build.channel is published in /v1/health and is the field
// LiveAgent will later use for channel-based candidate selection (LA A06). Two
// existing producers inject a version that is not a release tag:
//   - LiveAgent scripts/release/build-kbrain-source.mjs (`pnpm build:kbrain:source`)
//     passes `-X main.version=<lock.sourceRevision>` (a 40-hex commit) and no
//     main.revision;
//   - Taskfile.yaml passes `git describe --tags --always --dirty`, which is a bare
//     abbreviated commit on a clone without tags.
//
// docs/liveagent-backend.md promises local builds report dev; NewBuild instead
// labels both as "stable".
func TestNewBuildDoesNotLabelUntaggedSourceBuildsStable(t *testing.T) {
	for _, version := range []string{
		"51c827d3d56533550e67bc4f78ff7673fa7bd9a3", // LiveAgent kbrain.lock.json sourceRevision
		"894612f", // git describe --always without tags
	} {
		build := NewBuild(version, "")
		if build.Channel == "stable" {
			t.Errorf("NewBuild(%q) = %+v; an untagged source build must not be reported as the stable channel", version, build)
		}
	}
}
