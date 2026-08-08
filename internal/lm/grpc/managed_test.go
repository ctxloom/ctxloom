package grpc

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The host ships AssembleManagedConfig's payload to the plugin over RunStart, so
// every field the launch path depends on must survive the proto round-trip.
// BundleMCP carries the profile/builtin bundle servers (taskloom,
// sequential-thinking, …); if it's dropped here, the plugin-side Flush writes
// .mcp.json without them and they never register — the wire-boundary twin of the
// Flush(nil) leak.
func TestManagedConfig_ProtoRoundTrip_PreservesBundleMCP(t *testing.T) {
	in := &agent.ManagedConfig{
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{}},
		BundleMCP: map[string]wire.MCPServer{
			"taskloom": {Command: "taskloom", Args: []string{"mcp"}, SCM: "bundle:builtin:taskloom"},
		},
		ManageStatusline: true,
	}

	got := managedConfigFromProto(ManagedConfigToProto(in))

	require.NotNil(t, got)
	require.NotNil(t, got.BundleMCP, "BundleMCP dropped across the proto round-trip")
	require.Contains(t, got.BundleMCP, "taskloom")
	assert.Equal(t, "taskloom", got.BundleMCP["taskloom"].Command)
	assert.Equal(t, []string{"mcp"}, got.BundleMCP["taskloom"].Args)
	assert.Equal(t, "bundle:builtin:taskloom", got.BundleMCP["taskloom"].SCM)
}

// Protobuf cannot distinguish an EMPTY repeated field from an ABSENT one: both
// serialize to no bytes and decode back to nil. So "empty" and "nil" are the
// same fact at this boundary, and every converter here must answer it the same
// way — the way mcpServerMapToProto already documents (nil, so the wire stays
// minimal and the rebuilt Go value matches the host's "none" shape).
//
// Two guard styles for one question is how the pairs drifted apart: an
// empty-but-non-nil slice went in one direction as an empty non-nil slice and
// came back as nil, so a value could not survive its own round trip unchanged.
func TestManagedConverters_EmptyAndNilAreOneAnswer(t *testing.T) {
	t.Run("to-proto direction", func(t *testing.T) {
		assert.Nil(t, commandExportsToProto([]agent.CommandExport{}))
		assert.Nil(t, commandExportsToProto(nil))
		assert.Nil(t, skillExportsToProto([]agent.SkillExport{}))
		assert.Nil(t, skillExportsToProto(nil))
		assert.Nil(t, packageFilesToProto([]agent.PackageFile{}))
		assert.Nil(t, packageFilesToProto(nil))
		assert.Nil(t, hooksToProto([]wire.Hook{}))
		assert.Nil(t, hooksToProto(nil))
		assert.Nil(t, planFilesToProto([]agent.PlanFile{}))
		assert.Nil(t, planFilesToProto(nil))
		assert.Nil(t, mcpServerMapToProto(map[string]wire.MCPServer{}))
	})

	t.Run("from-proto direction", func(t *testing.T) {
		assert.Nil(t, commandExportsFromProto([]*CommandExport{}))
		assert.Nil(t, commandExportsFromProto(nil))
		assert.Nil(t, skillExportsFromProto([]*SkillExport{}))
		assert.Nil(t, skillExportsFromProto(nil))
		assert.Nil(t, packageFilesFromProto([]*PackageFile{}))
		assert.Nil(t, packageFilesFromProto(nil))
		assert.Nil(t, hooksFromProto([]*Hook{}))
		assert.Nil(t, hooksFromProto(nil))
		assert.Nil(t, planFilesFromProto([]*PlanFile{}))
		assert.Nil(t, planFilesFromProto(nil))
		assert.Nil(t, mcpServerMapFromProto(map[string]*MCPServer{}))
	})

	// A config whose slices are empty-but-non-nil must round-trip to the same
	// shape a config with nil slices does, because that is what the wire will
	// deliver either way.
	t.Run("round trip normalizes empty to nil", func(t *testing.T) {
		got := managedConfigFromProto(ManagedConfigToProto(&agent.ManagedConfig{
			Commands: []agent.CommandExport{},
			Skills:   []agent.SkillExport{},
			Hooks:    &wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{}}},
		}))
		require.NotNil(t, got)
		assert.Nil(t, got.Commands)
		assert.Nil(t, got.Skills)
		require.NotNil(t, got.Hooks)
		assert.Nil(t, got.Hooks.Unified.PreTool)
	})

	// Populated input is unaffected by the guard: every element still crosses.
	t.Run("populated slices still cross", func(t *testing.T) {
		got := managedConfigFromProto(ManagedConfigToProto(&agent.ManagedConfig{
			Commands: []agent.CommandExport{{Name: "review", Content: "body", Enabled: true}},
			Skills:   []agent.SkillExport{{Name: "humanize", Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("x"), Mode: 0o644}}}},
			Hooks:    &wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Matcher: "Bash", Command: "ltk"}}}},
		}))
		require.Len(t, got.Commands, 1)
		assert.Equal(t, "review", got.Commands[0].Name)
		require.Len(t, got.Skills, 1)
		require.Len(t, got.Skills[0].Files, 1)
		assert.Equal(t, "SKILL.md", got.Skills[0].Files[0].RelPath)
		require.Len(t, got.Hooks.Unified.PreTool, 1)
		assert.Equal(t, "Bash", got.Hooks.Unified.PreTool[0].Matcher)
	})
}

// TestMCPConfig_ProtoRoundTrip_PreservesTriState pins the unset/true/false
// tri-state of wire.MCPConfig.AutoRegisterCtxloom across the wire.
//
// The proto field is `optional bool` precisely so "absent" stays
// distinguishable from "false": ShouldAutoRegisterCtxloom resolves an ABSENT
// value to true (auto-register ctxloom's own MCP server), so collapsing absent
// to false silently launches every child WITHOUT agent_send / agent_recv /
// agent_report — the stranding coord/spawner.go warns about when the setting is
// genuinely off.
//
// The generated getter GetAutoRegisterCtxloom() performs exactly that collapse:
// it returns bool, not *bool, so a nil field reads as false. Neither converter
// may use it; both must carry the pointer through. This hazard is unguarded
// by the package-wide parity sweep in arch_test.go, which fills every field
// with a NON-ZERO value and so never exercises the nil arm.
func TestMCPConfig_ProtoRoundTrip_PreservesTriState(t *testing.T) {
	tru, fls := true, false
	for _, tc := range []struct {
		name string
		in   *bool
	}{
		{"absent", nil},
		{"explicit true", &tru},
		{"explicit false", &fls},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := &wire.MCPConfig{AutoRegisterCtxloom: tc.in}
			back := mcpConfigFromProto(mcpConfigToProto(sent))
			require.NotNil(t, back)
			if tc.in == nil {
				assert.Nil(t, back.AutoRegisterCtxloom,
					"absent must stay absent — collapsing it to a pointer makes the config's default unreachable")
			} else {
				require.NotNil(t, back.AutoRegisterCtxloom, "an explicit value must survive as an explicit value")
				assert.Equal(t, *tc.in, *back.AutoRegisterCtxloom)
			}
			// The payload assertion: what the plugin ASKS is the only thing the
			// tri-state exists to protect.
			assert.Equal(t, sent.ShouldAutoRegisterCtxloom(), back.ShouldAutoRegisterCtxloom(),
				"the decoded config must answer ShouldAutoRegisterCtxloom identically to the one that was sent")
		})
	}
}

// TestManagedConfig_SurfacePreferenceSurvivesTheWire guards the failure this
// file's own comments record twice: Skills and DenyTools each existed
// host-side with no proto field, so "the host wrote it into the payload and the
// wire dropped it". A delivery preference dropped the same way would be worse
// than absent — the agent would run with a delivery it did not choose, and the
// only symptom is context arriving by a different route.
func TestManagedConfig_SurfacePreferenceSurvivesTheWire(t *testing.T) {
	in := &agent.ManagedConfig{
		Surfaces: map[agent.SurfaceKind]agent.Approach{
			agent.SurfaceContext: agent.ApproachSystemPrompt,
			agent.SurfaceSkills:  agent.ApproachUnsafeFile,
		},
	}
	out := managedConfigFromProto(ManagedConfigToProto(in))
	require.NotNil(t, out)
	assert.Equal(t, in.Surfaces, out.Surfaces, "the preference must round-trip unchanged")
}

// An unparseable label costs the caller its PREFERENCE, not its session — but
// never silently: the engine default is a legitimate fallback, running with a
// delivery nobody chose while reporting nothing is not.
func TestManagedConfig_UnparseableSurfaceLabelDegradesRatherThanCorrupting(t *testing.T) {
	out := managedConfigFromProto(&ManagedConfig{
		Surfaces: map[string]string{"context": "telepathy", "skills": "unsafe-file"},
	})
	require.NotNil(t, out)
	assert.NotContains(t, out.Surfaces, agent.SurfaceContext,
		"an unknown approach must be dropped, never resolved to iota 0 (unsafe-file, the least safe)")
	assert.Equal(t, agent.ApproachUnsafeFile, out.Surfaces[agent.SurfaceSkills],
		"the readable pairs still apply")
}
