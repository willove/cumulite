package cumulite

import (
	"context"
	"strings"
	"testing"
)

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
