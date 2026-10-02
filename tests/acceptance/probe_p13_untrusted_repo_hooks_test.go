package acceptance

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The P13 verdicts, argv and fixture wire, exercised without an engine. Frames
// are shaped like the stream-json claude 2.1.286 emitted for this fixture.

const p13TestCall = "toolu_p13"

// p13TestStream renders a run: the init frame (listing the repo's skill and
// agent when listed), one Bash tool_use, its tool_result, and a result frame.
func p13TestStream(t *testing.T, text string, isError, listed bool) string {
	t.Helper()
	skills, agents := []string{"update-config"}, []string{"general-purpose"}
	if listed {
		skills, agents = append(skills, p13RepoSkill), append(agents, p13RepoAgent)
	}
	init := map[string]any{"type": "system", "subtype": "init", "skills": skills, "agents": agents}
	use := map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": p13TestCall, "name": p12GatedTool, "input": map[string]string{"command": "echo hi"}},
	}}}
	res := map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "tool_use_id": p13TestCall, "content": text, "is_error": isError},
	}}}
	result := map[string]any{"type": "result", "subtype": "success"}
	return strings.Join([]string{p12Line(t, init), p12Line(t, use), p12Line(t, res), p12Line(t, result)}, "\n")
}

func p13TestOutcome(v p13Variant, stdout string, fired map[string]bool) p13Outcome {
	return p13Outcome{
		Cell:        probeCellID{Probe: probeP13, Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: string(v)},
		Variant:     v,
		Started:     true,
		Run:         probeRun{Stdout: stdout},
		Fired:       fired,
		Frontmatter: map[string]bool{},
	}
}

var (
	p13AllFired  = map[string]bool{p13PreToolEvent: true, p13SessionStartEvent: true}
	p13NoneFired = map[string]bool{}
	// The echo's output as claude 2.1.286 relayed it, shell startup noise first.
	p13Echoed = "/home/u/.zshenv:.:1: no such file or directory: /tmp/x/home/.cargo/env\nhi"
)

func TestP13_UntrustedFires(t *testing.T) {
	t.Run("both hooks fired and the echo ran is green", func(t *testing.T) {
		require.NoError(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, p13Echoed, false, true), p13AllFired)))
	})
	for event := range p13Markers {
		t.Run("a silent "+event+" hook is REPO-HOOK-SILENT", func(t *testing.T) {
			fired := map[string]bool{p13PreToolEvent: true, p13SessionStartEvent: true}
			fired[event] = false
			p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, p13Echoed, false, true), fired)), shapeRepoHookSilent)
		})
	}
}

func TestP13_SettingSourcesSuppresses(t *testing.T) {
	t.Run("no hook fired and the echo ran is green", func(t *testing.T) {
		require.NoError(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false, false), p13NoneFired)))
	})
	for event := range p13Markers {
		t.Run("a leaked "+event+" hook is REPO-HOOK-LEAKED", func(t *testing.T) {
			p12RequireShape(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false, false), map[string]bool{event: true})), shapeRepoHookLeaked)
		})
	}
	t.Run("no hook fired but the echo never ran is ECHO-NOT-RUN, not green", func(t *testing.T) {
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, "Permission denied", true, false), p13NoneFired)), shapeEchoNotRun)
	})
}

// The fires arm's second premise: without the flags claude LOADS a never-trusted
// repo's committed skill and agent, so they are there to be invoked.
func TestP13_UntrustedFiresListsRepoSurfaces(t *testing.T) {
	p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, p13Echoed, false, false), p13AllFired)), shapeRepoSurfaceUnlisted)
}

// The defence for skills and agents: under the flags the repo's committed
// skill and agent are not loaded at all, and no frontmatter hook or MCP server
// leaves its marker.
func TestP13_SettingSourcesSuppressesFrontmatter(t *testing.T) {
	t.Run("a repo skill or agent listed in the init frame is REPO-SURFACE-LOADED", func(t *testing.T) {
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false, true), p13NoneFired)), shapeRepoSurfaceLoaded)
	})
	for surface := range p13FrontmatterMarkers {
		t.Run("a leaked "+surface+" marker is REPO-HOOK-LEAKED", func(t *testing.T) {
			o := p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false, false), p13NoneFired)
			o.Frontmatter[surface] = true
			p12RequireShape(t, p13Assert(o), shapeRepoHookLeaked)
		})
	}
}

// The positive control: in a TRUSTED repo the fixture's agent frontmatter
// really executes, so the suppressing arm's silence is the flags' doing and
// not a fixture claude never honours.
func TestP13_TrustedFrontmatterFires(t *testing.T) {
	allControls := func() map[string]bool {
		m := map[string]bool{}
		for _, s := range p13ControlSurfaces {
			m[s] = true
		}
		return m
	}
	t.Run("every control surface fired and both are listed is green", func(t *testing.T) {
		o := p13TestOutcome(p13TrustedFrontmatter, p13TestStream(t, p13Echoed, false, true), p13NoneFired)
		o.Frontmatter = allControls()
		require.NoError(t, p13Assert(o))
	})
	t.Run("an unlisted repo skill or agent is REPO-SURFACE-UNLISTED", func(t *testing.T) {
		o := p13TestOutcome(p13TrustedFrontmatter, p13TestStream(t, p13Echoed, false, false), p13NoneFired)
		o.Frontmatter = allControls()
		p12RequireShape(t, p13Assert(o), shapeRepoSurfaceUnlisted)
	})
	for _, surface := range p13ControlSurfaces {
		t.Run("a silent "+surface+" is FRONTMATTER-SILENT", func(t *testing.T) {
			o := p13TestOutcome(p13TrustedFrontmatter, p13TestStream(t, p13Echoed, false, true), p13NoneFired)
			o.Frontmatter = allControls()
			o.Frontmatter[surface] = false
			p12RequireShape(t, p13Assert(o), shapeFrontmatterSilent)
		})
	}
	require.NotEmpty(t, p13ControlSurfaces)
	for _, s := range p13ControlSurfaces {
		require.Contains(t, p13FrontmatterMarkers, s, "a control surface must have a marker")
	}
}

func TestP13_CommonHalf(t *testing.T) {
	t.Run("output that only CONTAINS the letters is not the echo", func(t *testing.T) {
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, "this is not it", false, true), p13AllFired)), shapeEchoNotRun)
	})
	t.Run("no Bash call is NOT-ATTEMPTED", func(t *testing.T) {
		stream := p12Line(t, map[string]any{"type": "result", "subtype": "success"})
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, stream, p13AllFired)), shapeNotAttempted)
	})
	t.Run("a timed-out run is a RUN failure", func(t *testing.T) {
		o := p13TestOutcome(p13Fires, "", p13AllFired)
		o.TimedOut = true
		p12RequireShape(t, p13Assert(o), shapeRunFailed)
	})
	t.Run("an unreadable marker is a RUN failure, never a verdict on the hooks", func(t *testing.T) {
		o := p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false, false), p13NoneFired)
		o.MarkerErr = errors.New("permission denied")
		p12RequireShape(t, p13Assert(o), shapeRunFailed)
	})
}

// TestP13_Args: the suppressing arm differs from the firing arm by EXACTLY the
// flags ctxloom's repo trust launches an untrusted repo with, and both carry
// the flag-scope allow, the prompt and the pinned model.
func TestP13_Args(t *testing.T) {
	fires, supp := p13Args(p13Fires), p13Args(p13Suppresses)
	require.Equal(t, append(slices.Clone(fires), "--setting-sources", "user", "--strict-mcp-config"), supp)
	for _, want := range [][]string{{"-p", p13Prompt}, {"--settings", p13FlagSettings}, {"--model", liveClaudeModel}} {
		i := slices.Index(fires, want[0])
		require.GreaterOrEqual(t, i, 0, "%s missing", want[0])
		require.Equal(t, want[1], fires[i+1])
	}
	require.NotContains(t, fires, "--setting-sources")
	require.Equal(t, fires, p13Args(p13TrustedFrontmatter), "the trusted control differs from the firing arm only in its config dir")
}

// TestP13_FixtureWire: the committed settings register one command hook per
// p13Markers event, each touching its marker under the cell dir (outside the
// repo), with the PreToolUse hook matched on the gated tool.
func TestP13_FixtureWire(t *testing.T) {
	raw, err := p13RepoSettingsJSON("/cell")
	require.NoError(t, err)
	var got p12Settings
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Hooks, len(p13Markers))
	for event, marker := range p13Markers {
		ms := got.Hooks[event]
		require.Len(t, ms, 1, event)
		require.Len(t, ms[0].Hooks, 1, event)
		require.Equal(t, "command", ms[0].Hooks[0].Type)
		require.Equal(t, "touch "+p12ShellQuote("/cell/"+marker), ms[0].Hooks[0].Command)
		if event == p13PreToolEvent {
			require.Equal(t, p12GatedTool, ms[0].Matcher)
		}
	}
	var flag map[string]any
	require.NoError(t, json.Unmarshal([]byte(p13FlagSettings), &flag), "the flag-scope settings must be valid JSON")
}

// TestP13_FrontmatterFixtureWire: the committed skill and agent carry their
// names and frontmatter whose every command touches its own marker under the
// cell dir, and the trusted arm's .claude.json trusts exactly the repo.
func TestP13_FrontmatterFixtureWire(t *testing.T) {
	skill, err := p13SkillMD("/cell")
	require.NoError(t, err)
	agent, err := p13AgentMD("/cell")
	require.NoError(t, err)
	for name, doc := range map[string][]byte{p13RepoSkill: skill, p13RepoAgent: agent} {
		require.True(t, strings.HasPrefix(string(doc), "---\nname: "+name+"\n"), "%s frontmatter opens with its name:\n%s", name, doc)
	}
	for surface, marker := range p13FrontmatterMarkers {
		doc := agent
		if strings.HasPrefix(surface, "skill") {
			doc = skill
		}
		require.Contains(t, string(doc), "touch "+p12ShellQuote("/cell/"+marker), surface)
	}
	raw, err := p13TrustJSON("/cell/repo")
	require.NoError(t, err)
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Len(t, cfg.Projects, 1)
	require.True(t, cfg.Projects["/cell/repo"].HasTrustDialogAccepted)
}
