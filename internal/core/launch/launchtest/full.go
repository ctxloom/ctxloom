package launchtest

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// FullLaunch is a Launch with EVERY field populated by a non-zero value, for
// the wire codec's population and round-trip tests: a wire field the
// encoder never writes from this value is a field no runner can receive.
// It is a fixture, not a resolved launch — nothing here went through Resolve.
func FullLaunch(t *testing.T) launch.Launch {
	t.Helper()
	enc, err := composite.Encode(composite.Package{Context: composite.Context{Text: "ctx", Hash: "h"}})
	require.NoError(t, err)
	paths := present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: "/proj/.worktrees/harp-1", Engine: "/work"},
		SessionHome: present.Root{Host: "/home/u/.ctxloom/sessions/harp-1/home/.engine", Engine: "/home/agent/.engine"},
	})
	return launch.Launch{
		Identity:   sessions.Identity{Harp: "harp-1", RunID: "run-1", Depth: 1, OneShot: true, Project: "proj-1"},
		Engine:     EngineName,
		Label:      engine.LabelConfig{Label: "primary", Model: "fixture-fast-2", Binary: "fixture", Args: []string{"--flag"}, Body: map[string]any{"model": "fixture-fast-2", "thinking": "low"}},
		Mode:       engine.Structured,
		Permission: engine.PermissionPlan,
		Axes:       launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeRootless},
		Cell: launch.Cell{
			Placement: launch.Placement{
				Paths: paths,
				Env:   map[string]string{"WS_VAR": "ws"},
				Home:  []engine.HomeBinding{{Var: "FIXTURE_HOME", Path: "/home/agent/.engine"}},
			},
			Workspace: "/proj/.worktrees/harp-1",
		},
		Home:    []engine.HomeBinding{{Var: "FIXTURE_HOME", Path: "/home/agent/.engine"}},
		Package: composite.Carrier{Inline: enc.Bytes, Digest: enc.Digest},
		Exports: engine.Exports{
			Commands:  []engine.CommandExport{{Name: "b-c", Body: []byte("cmd"), Enabled: true, Description: "d", ArgumentHint: "[x]", AllowedTools: []string{"Read"}, Model: "m"}},
			Skills:    []engine.SkillExport{{Name: "s", Description: "sd", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Digest: "dg", Size: 3, Mode: 0o755, Bytes: []byte("abc")}}}},
			HookEvent: map[string]string{"session_start": "SessionStart"},
			DenyTools: []string{"Task"},
		},
		Plan: delivery.Plan{
			Static:  []delivery.StaticItem{{Kind: present.Context, Approach: "fixture-file", Root: present.RootSessionHome, Traits: present.Traits{Roots: []present.RootKind{present.RootSessionHome}, Channel: present.ChannelFile, LaunchOnly: true, Persists: true}}},
			Dynamic: []string{"b/premised"},
			Losses:  []delivery.Loss{{Kind: present.Skills}},
		},
		Index:  composite.Index{Entries: []composite.IndexEntry{{Ref: "b/f", Kind: trust.KindFragment, Description: "desc", Premise: "when x"}}},
		MCP:    sessions.Endpoint{URL: "http://127.0.0.1:41234/mcp", Credential: "bearer"},
		Prompt: "do the thing",
		Resume: sessions.ResumeRef{Harp: "harp-1", NativeKey: "native-1"},
		Env:    map[string]string{"USER_VAR": "u"},
	}
}

// Comparable is a Launch with its process-local handles cleared: the cell's
// Cleanup and Handle exist only where the cell was prepared and never cross
// the wire, so a round-trip equality reads everything but them.
func Comparable(l launch.Launch) launch.Launch {
	l.Cell.Cleanup = nil
	l.Cell.Handle = nil
	return l
}

var _ = sha256.Sum256
