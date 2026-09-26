package cumulite

import (
	"runtime/debug"
)

// modulePath is this package's Go module path, the key the build info and
// dependency lists record it under.
const modulePath = "github.com/willove/cumulite"

// version is the seed of the version Health reports. A release build pins it
// at link time, so the binary's self-report follows the tag instead of a
// string a human had to remember to bump:
//
//	go build -ldflags "-X github.com/willove/cumulite.version=$(git describe --tags --always)"
//
// Unset, resolution falls through to the module information the consuming
// binary already carries — the version recorded for this dependency — and
// only a bare checkout build reports "dev". The old hardcoded literal was the
// trap this replaces: the tag moved to v0.2.0 and Health kept saying 0.1.0,
// and the self-report is the only signal an operator has for judging which
// capabilities a running store actually has.
var version = "dev"

// engineVersion resolves the version Health reports, in order of authority:
// the ldflags injection (a release build that pinned it), the module version
// (what the consuming binary's build info records for this package, or the
// VCS tag Go 1.24+ stamps when building this repo at a tagged commit), and
// "dev" when nothing knows. The "cumulite/" prefix is Health's formatting,
// not this value's.
func engineVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if v := moduleVersion(); v != "" {
		return v
	}
	return "dev"
}

// moduleVersion reads this module's version out of the running binary's build
// information: bi.Main when the binary is built from this module at a tagged
// commit, bi.Deps when this module is a dependency (the embedded-engine case).
// A local checkout build has no version to report — "(devel)" is the build's
// own admission of it.
func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, dep := range bi.Deps {
		if dep.Path == modulePath && dep.Version != "" && dep.Version != "(devel)" {
			return dep.Version
		}
	}
	return ""
}
