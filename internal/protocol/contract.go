package protocol

import (
	"regexp"
	"strings"
)

// Structured handshake fields defined by docs/compatibility.md section 2. The
// legacy Version string stays the primary v1 marker; Name/Major must agree with
// it or clients reject the health response as a broken contract.
const (
	ProtocolName  = "kbrain.agent"
	ProtocolMajor = 1
	// ProtocolMinor counts additive revisions inside major 1. It is for logs
	// only; clients gate features on capabilities, never on this number.
	ProtocolMinor = 1

	// StartupContract versions the `kn backend` flags, ready line and exit
	// conventions independently of the HTTP protocol.
	StartupContract = 1
)

type ProtocolInfo struct {
	Name  string `json:"name"`
	Major int    `json:"major"`
	Minor int    `json:"minor"`
}

func CurrentProtocol() ProtocolInfo {
	return ProtocolInfo{Name: ProtocolName, Major: ProtocolMajor, Minor: ProtocolMinor}
}

// Build is the immutable identity of the running binary as injected at link
// time. releaseSequence is deliberately absent until signed release metadata
// (S3) gives it a trustworthy source.
type Build struct {
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	Channel  string `json:"channel"`
}

// releaseTag matches the published tag form vMAJOR.MINOR.PATCH with an optional
// prerelease suffix. Anything else (commit SHAs, `git describe` output for
// untagged clones, local labels) is a dev build and never claims a channel.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// describeSuffix matches `git describe` output past a tag (`-<n>-g<sha>`) and
// `--dirty` builds, which reuse a tag prefix but are not that release.
var describeSuffix = regexp.MustCompile(`(-\d+-g[0-9a-f]{7,}|-dirty)$`)

// NewBuild normalizes link-time values. Release tags containing "-" are
// prereleases, matching the existing release workflow convention.
func NewBuild(version, revision string) Build {
	version = strings.TrimSpace(version)
	revision = strings.TrimSpace(revision)
	switch {
	case version == "":
		return Build{Version: "dev", Revision: revision, Channel: "dev"}
	case !releaseTag.MatchString(version) || describeSuffix.MatchString(version):
		return Build{Version: version, Revision: revision, Channel: "dev"}
	case strings.Contains(version, "-"):
		return Build{Version: version, Revision: revision, Channel: "prerelease"}
	default:
		return Build{Version: version, Revision: revision, Channel: "stable"}
	}
}
