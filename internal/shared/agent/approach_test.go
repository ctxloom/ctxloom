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

// ContextWrite/MCPWrite/SettingsWrite/CommandsWrite/SkillsWrite no longer have
// their own String() (all five were deleted as test-only forwarders to
// Approach.String(), which TestApproach_String above already covers). Only
// approach() — the compile-time cross-surface guard — remains to test here.
func TestContextWrite_Approach(t *testing.T) {
	cases := []struct {
		name     string
		val      ContextWrite
		wantAppr Approach
	}{
		{"unsafe-file", ContextWriteUnsafeFile, ApproachUnsafeFile},
		{"system-prompt", ContextWriteSystemPrompt, ApproachSystemPrompt},
		{"hook", ContextWriteHook, ApproachHook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantAppr, tc.val.approach())
		})
	}
}

// ---- MCPWrite / SettingsWrite / CommandsWrite ------------------------------------

// Every one of these single-valued enums converts to ApproachUnsafeFile — the
// only approach any of these surfaces offers today (a claude-only extension point
// for symmetry with ContextWrite).
func TestSingleValueWrites_Approach(t *testing.T) {
	assert.Equal(t, ApproachUnsafeFile, MCPWriteUnsafeFile.approach())
	assert.Equal(t, ApproachUnsafeFile, SettingsWriteUnsafeFile.approach())
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

// Approach.String must not render an OUT-OF-RANGE value as the enum's zero
// value. Both this enum and CellKind spell their zero value as the LEAST safe
// option — ApproachUnsafeFile ("unsafe-file"), CellKindShared ("shared") — so a
// default: arm that returns it makes an unrecognised value read as a decided,
// unsafe choice in the very error text a reader uses to diagnose it
// ("approach unsafe-file not supported"). SurfaceKind.String, in the same file,
// already spells the honest answer ("unknown").
func TestApproachString_UnknownValueIsNotRenderedAsUnsafeFile(t *testing.T) {
	// (1) every DECLARED value keeps its stable label — the characterization
	// half, green before and after.
	assert.Equal(t, "unsafe-file", ApproachUnsafeFile.String())
	assert.Equal(t, "system-prompt", ApproachSystemPrompt.String())
	assert.Equal(t, "hook", ApproachHook.String())

	// (2) an undeclared value renders as unknown, not as the zero value.
	got := Approach(99).String()
	assert.NotEqual(t, "unsafe-file", got, "an unrecognised approach must not impersonate the unsafe-file choice")
	assert.Contains(t, got, "unknown")
	assert.Contains(t, got, "99", "the honest label carries the numeric value a reader needs")
}

// CellKind.String has the same shape and the same hazard: an unrecognised cell
// rendering as "shared" reports NO isolation for a run whose isolation is simply
// unknown to this build.
func TestCellKindString_UnknownValueIsNotRenderedAsShared(t *testing.T) {
	assert.Equal(t, "shared", CellKindShared.String())
	assert.Equal(t, "directory-isolated", CellKindDirectoryIsolated.String())
	assert.Equal(t, "process-isolated", CellKindProcessIsolated.String())

	got := CellKind(99).String()
	assert.NotEqual(t, "shared", got, "an unrecognised cell must not report the run as un-isolated")
	assert.Contains(t, got, "unknown")
	assert.Contains(t, got, "99")
}
