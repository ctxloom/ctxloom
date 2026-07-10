package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChildVerbosity pins the env-only diagnostics knob: CTXLOOM_VERBOSE=1
// turns the child plugin/adapter stderr trail on at trace; anything else
// keeps the default-quiet launch.
func TestChildVerbosity(t *testing.T) {
	t.Setenv("CTXLOOM_VERBOSE", "")
	assert.Equal(t, 0, childVerbosity())
	t.Setenv("CTXLOOM_VERBOSE", "1")
	assert.Equal(t, 3, childVerbosity())
}

// TestViaStartRunBackends pins the Wave C3 spawn-cutover gate: claude-code
// (C1), codex, kiro, and the generic "acp" entry (C3) all route their
// delegated children over StartRun — every backend whose Chat rides the
// shared internal/acp driver, verified per-backend in the C3 recon.
// Backends NOT reviewed onto the migrated path (antigravity, mock, and any
// unknown/future backend name) stay on the legacy go-plugin Chat dial — this
// is a deliberate allowlist, not "implements StructuredChat", so a new
// backend never gets swept onto StartRun unreviewed.
func TestViaStartRunBackends(t *testing.T) {
	cases := map[string]bool{
		"claude-code":  true,
		"codex":        true,
		"kiro":         true,
		"acp":          true,
		"antigravity":  false,
		"mock":         false,
		"":             false,
		"unknown-type": false,
	}
	for backend, want := range cases {
		assert.Equal(t, want, viaStartRunBackends[backend], "backend %q", backend)
	}
}
