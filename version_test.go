package cumulite

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hardcodedVersionLiteral is the bug shape this file guards against: a semver
// literal spelled next to the cumulite/ prefix in source. The formatting
// prefix ("cumulite/" + engineVersion()) is fine; digits inside the literal
// are how the tag moved to v0.2.0 while Health kept saying 0.1.0.
var hardcodedVersionLiteral = regexp.MustCompile(`"cumulite/v?\d`)

func TestVersionIsNeverALiteral(t *testing.T) {
	dirs := []string{".", "../cmd/cumulite"}
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if hardcodedVersionLiteral.MatchString(line) {
					t.Fatalf("%s:%d hardcodes a version (%s) — resolve it through engineVersion() instead",
						f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}

func TestHealthVersionFollowsTheTag(t *testing.T) {
	e := openEngine(t)
	ctx := context.Background()

	// The hardcoded literal is gone: no build path can report the retired
	// release string, whichever way resolution falls.
	h, err := e.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.Version == "cumulite/0.1.0" {
		t.Fatalf("Health reports the retired hardcoded literal: %q", h.Version)
	}
	if !strings.HasPrefix(h.Version, "cumulite/") {
		t.Fatalf("version = %q, want the cumulite/ prefix", h.Version)
	}

	// The ldflags injection is what a release build pins; it wins outright.
	withSeed(t, "v0.2.1", func() {
		h, err := e.Health(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if h.Version != "cumulite/v0.2.1" {
			t.Fatalf("version = %q, want the injected release value", h.Version)
		}
	})

	// With no injection the module information answers; a plain checkout
	// build has none and reports dev — honest about being a dev build.
	withSeed(t, "dev", func() {
		h, err := e.Health(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		got := strings.TrimPrefix(h.Version, "cumulite/")
		want := moduleVersion()
		if want == "" {
			want = "dev"
		}
		if got != want {
			t.Fatalf("version = %q, want module fallback %q", h.Version, want)
		}
	})
}

// withSeed sets the build-time version var (same package, the exact write
// -ldflags -X performs at link time) and restores it after fn.
func withSeed(t *testing.T, v string, fn func()) {
	t.Helper()
	old := version
	defer func() { version = old }()
	version = v
	fn()
}
