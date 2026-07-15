package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---- Approach ---------------------------------------------------------------

func TestApproach_String(t *testing.T) {
	assert.Equal(t, "unsafe-file", ApproachUnsafeFile.String())
	assert.Equal(t, "system-prompt", ApproachSystemPrompt.String())
	assert.Equal(t, "hook", ApproachHook.String())
}

// ---- ContextWrite -------------------------------------------------------------

func TestContextWrite_StringAndApproach(t *testing.T) {
	cases := []struct {
		name     string
		val      ContextWrite
		wantStr  string
		wantAppr Approach
	}{
		{"unsafe-file", ContextWriteUnsafeFile, "unsafe-file", ApproachUnsafeFile},
		{"system-prompt", ContextWriteSystemPrompt, "system-prompt", ApproachSystemPrompt},
		{"hook", ContextWriteHook, "hook", ApproachHook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantStr, tc.val.String())
			assert.Equal(t, tc.wantAppr, tc.val.approach())
		})
	}
}

// ---- MCPWrite / SettingsWrite / CommandsWrite ------------------------------------

// Every one of these single-valued enums converts to ApproachUnsafeFile — the
// only approach any of these surfaces offers today (a claude-only extension point
// for symmetry with ContextWrite).
func TestSingleValueWrites_StringAndApproach(t *testing.T) {
	assert.Equal(t, "unsafe-file", MCPWriteUnsafeFile.String())
	assert.Equal(t, ApproachUnsafeFile, MCPWriteUnsafeFile.approach())

	assert.Equal(t, "unsafe-file", SettingsWriteUnsafeFile.String())
	assert.Equal(t, ApproachUnsafeFile, SettingsWriteUnsafeFile.approach())

	assert.Equal(t, "unsafe-file", CommandsWriteUnsafeFile.String())
	assert.Equal(t, ApproachUnsafeFile, CommandsWriteUnsafeFile.approach())
}

// ---- ApproachTable (shared per-provider dispatch mechanics) -----------------

// The shared table helpers implement the mechanical halves of the dispatch: the
// declared slice is Supported verbatim, the FIRST entry is the Default, an
// absent/folded kind reports nil/false, and SurfaceFor validates the approach
// against the declaration before resolving the kind→surface map.
func TestApproachTable_SupportedDefaultAndSurfaceFor(t *testing.T) {
	table := ApproachTable{
		SurfaceContext:  {ApproachHook, ApproachUnsafeFile}, // Hook first = default
		SurfaceSettings: {ApproachUnsafeFile},
		// SurfaceMCP absent: folded.
	}

	assert.Equal(t, []Approach{ApproachHook, ApproachUnsafeFile}, table.Supported(SurfaceContext))
	assert.Nil(t, table.Supported(SurfaceMCP), "an absent/folded kind reports no approaches")

	a, ok := table.Default(SurfaceContext)
	assert.True(t, ok)
	assert.Equal(t, ApproachHook, a, "the FIRST declared entry is the default")
	_, ok = table.Default(SurfaceMCP)
	assert.False(t, ok, "an absent/folded kind has no default")

	stub := recordingDelivery{handle: stubHandle{}}
	surfaces := map[SurfaceKind]Delivery{SurfaceContext: stub, SurfaceSettings: stub}

	d, err := table.SurfaceFor("enginex", surfaces, SurfaceContext, ApproachHook)
	assert.NoError(t, err)
	assert.Equal(t, stub, d)

	_, err = table.SurfaceFor("enginex", surfaces, SurfaceSettings, ApproachSystemPrompt)
	assert.ErrorContains(t, err, "enginex: no settings surface via system-prompt",
		"an undeclared approach is rejected, naming backend/surface/approach")

	// A declared kind missing from the surface map is a loud mismatch.
	mismatched := ApproachTable{SurfaceCommands: {ApproachUnsafeFile}}
	_, err = mismatched.SurfaceFor("enginex", map[SurfaceKind]Delivery{}, SurfaceCommands, ApproachUnsafeFile)
	assert.ErrorContains(t, err, "enginex: no commands surface")
}
