// Tests for cmd/agent.go's extracted renderers. The cobra wrappers route
// list/show through internal/operations (covered by operations/agents_test.go);
// the CLI-local testable surface is the formatting in renderAgentList /
// renderAgentShow.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

func TestRenderAgentList_EngineAndDefault(t *testing.T) {
	list := []operations.AgentEntry{
		{Name: "dev", LLM: "claude-code", Profiles: []string{"go-developer", "go-style"}, Runtime: "container"},
		{Name: "finder", Profiles: []string{"finder"}}, // no engine, no isolation
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "Agents (2):")
	assert.Contains(t, out, "dev (llm: claude-code)")
	assert.Contains(t, out, "profiles: go-developer, go-style")
	assert.Contains(t, out, "runtime: container")
	assert.Contains(t, out, "finder (llm: project default)", "unset llm shows the project-default hint")
	assert.Equal(t, 1, strings.Count(out, "runtime:"), "unset runtime prints nothing (inherits the project default)")
}

// TestRenderAgentList_Driving proves a declared `driving:` value renders in
// the list, and an unset one prints nothing (mirroring Runtime's existing
// omit-when-empty convention).
func TestRenderAgentList_Driving(t *testing.T) {
	list := []operations.AgentEntry{
		{Name: "shooter", LLM: "claude-code", Driving: "oneshot"},
		{Name: "chatter", LLM: "claude-code"}, // driving unset
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "driving: oneshot")
	assert.Equal(t, 1, strings.Count(out, "driving:"), "unset driving prints nothing")
}

func TestRenderAgentList_Empty(t *testing.T) {
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, nil))
	assert.Contains(t, buf.String(), "No agents defined.")
}

func TestRenderAgentShow_Resolved(t *testing.T) {
	def := &operations.AgentEntry{Name: "dev", LLM: "slow", Profiles: []string{"p1", "p2"}, Runtime: "container"}
	resolved := &operations.ResolvedAgent{
		Name: "dev", Label: "slow", Backend: "mock", Model: "m-slow",
		Fragments: []string{"a", "b"},
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, resolved, nil, nil))
	out := buf.String()

	assert.Contains(t, out, "Agent: dev")
	assert.Contains(t, out, "Engine (declared): slow")
	assert.Contains(t, out, "Runtime: container")
	assert.Contains(t, out, "Resolved llm: slow (backend: mock, model: m-slow)")
	assert.Contains(t, out, "Composed fragments: 2")
}

// TestRenderAgentShow_Driving proves a declared `driving:` value renders in
// `agent show`.
func TestRenderAgentShow_Driving(t *testing.T) {
	def := &operations.AgentEntry{Name: "shooter", LLM: "slow", Driving: "oneshot"}
	resolved := &operations.ResolvedAgent{Name: "shooter", Label: "slow", Backend: "mock"}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, resolved, nil, nil))
	assert.Contains(t, buf.String(), "Driving: oneshot")
}

func TestRenderAgentShow_ResolutionFailureStillPrintsDefinition(t *testing.T) {
	def := &operations.AgentEntry{Name: "dev", Profiles: []string{"missing"}}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, nil, errors.New("profile missing not found"), nil))
	out := buf.String()

	assert.Contains(t, out, "Agent: dev")
	assert.Contains(t, out, "Engine (declared): (project default)")
	assert.Contains(t, out, "Resolved llm: unavailable (profile missing not found)")
}

// TestSetupPrompt_EmitsPrompt proves the setup-prompt body — the SCAN →
// DISCUSS → WRITE instructions the LLM follows — reaches stdout. It used to be
// asserted through the deprecated `agent setup` alias, deleted by the
// verb-spine reorg; `ctxloom init prompt` is the surviving door onto the SAME
// body (runSetupPromptCmd). The prompt is a markdown resource, so the
// assertions pin only the load-bearing, name-agnostic mechanics (not any
// role/lens names).
func TestSetupPrompt_EmitsPrompt(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	assert.NoError(t, runSetupPromptCmd(cmd, nil))
	out := buf.String()

	assert.Contains(t, out, "ctxloom llm list", "prompt scans engines at runtime")
	assert.Contains(t, out, "ctxloom profile list", "prompt scans profiles at runtime")
	assert.Contains(t, out, "ctxloom agent create", "prompt writes bindings via agent create")
	assert.Contains(t, out, "search_library", "prompt discovers the cr-* lenses from the library")
}

// `agent show` resolves the engine fault-tolerantly: a failure (e.g. a missing
// constituent profile) still prints the definition, with the reason. The TEXT
// renderer says so — "Resolved llm: unavailable (…)" — but the --format json
// payload carried only `resolved` omitted, so a structured consumer saw an
// agent with no resolution and no way to learn why. Two formats of the same
// command must not disagree about whether anything went wrong.
func TestAgentShow_JSONCarriesTheResolutionFailure(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"),
		[]byte("version: 5\nagents:\n  broken:\n    profiles: [no-such-profile]\n"), 0o644))
	t.Chdir(root)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	cmd, out := formatCmd("json")
	cmd.SetContext(context.Background())
	require.NoError(t, agentShowCmd.RunE(cmd, []string{"broken"}))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Contains(t, payload, "resolution_error",
		"a resolution failure the text view reports must not vanish from --format json")
	assert.NotEmpty(t, payload["resolution_error"])

	// Text and JSON must agree: the text renderer reports the same failure.
	var text bytes.Buffer
	require.NoError(t, renderAgentShow(&text, &operations.AgentEntry{Name: "broken"}, nil,
		errors.New("no-such-profile not found"), nil))
	assert.Contains(t, text.String(), "Resolved llm: unavailable")
}

// The key is absent — not present-and-empty — when resolution succeeded, so a
// consumer can test for it directly.
func TestAgentShow_JSONOmitsTheErrorKeyWhenResolutionSucceeded(t *testing.T) {
	payload, err := json.Marshal(agentShowJSON{
		Definition: &operations.AgentEntry{Name: "dev"},
		Resolved:   &operations.ResolvedAgent{Label: "claude-code"},
	})
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "resolution_error")
}

// renderAgentShow is a run of sequential optional-field arms plus a resolution
// branch. This drives EVERY arm — each optional field present in one case and
// absent in the other — so the rendering can be split up without changing a
// byte of what `agent show` prints.
func TestRenderAgentShow_EveryOptionalArm(t *testing.T) {
	t.Run("everything declared and resolved", func(t *testing.T) {
		def := &operations.AgentEntry{
			Name:        "full",
			LLM:         "claude-code",
			Profiles:    []string{"p1", "p2"},
			Runtime:     "container",
			Permissions: "acceptEdits",
			Driving:     "oneshot",
			Escalation: []agents.EscalationRung{
				{Kinds: []string{"COMMAND_EXECUTION", "FILE_CHANGE"}, Action: "surface_to_human"},
				{Action: "auto_accept"},
			},
		}
		resolved := &operations.ResolvedAgent{
			Label: "claude-code", Backend: "claude", Model: "opus",
			EffectivePermissions: "bypass",
			Fragments:            []string{"a", "b", "c"},
		}

		var buf bytes.Buffer
		require.NoError(t, renderAgentShow(&buf, def, resolved, nil, nil))
		out := buf.String()

		for _, want := range []string{
			"Agent: full\n",
			"Engine (declared): claude-code\n",
			"Runtime: container\n",
			"Permissions: acceptEdits\n",
			"Driving: oneshot\n",
			"Escalation: 2 rung(s)\n",
			"  - COMMAND_EXECUTION,FILE_CHANGE: surface_to_human\n",
			"  - all kinds: auto_accept\n",
			"Resolved llm: claude-code (backend: claude, model: opus)\n",
			"Resolved permissions: bypass\n",
			"Composed fragments: 3\n",
		} {
			assert.Contains(t, out, want)
		}
	})

	t.Run("nothing optional declared, minimal resolution", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, renderAgentShow(&buf, &operations.AgentEntry{Name: "bare"},
			&operations.ResolvedAgent{Label: "default"}, nil, nil))
		out := buf.String()

		assert.Contains(t, out, "Agent: bare\n")
		assert.Contains(t, out, "Engine (declared): (project default)\n")
		assert.Contains(t, out, "Resolved llm: default\n")
		assert.Contains(t, out, "Composed fragments: 0\n")
		for _, unwanted := range []string{"Runtime:", "Permissions:", "Driving:", "Escalation:", "backend:", "Resolved permissions:"} {
			assert.NotContains(t, out, unwanted)
		}
	})

	t.Run("a backend without a model omits the model clause", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, renderAgentShow(&buf, &operations.AgentEntry{Name: "b"},
			&operations.ResolvedAgent{Label: "l", Backend: "mock"}, nil, nil))
		assert.Contains(t, buf.String(), "Resolved llm: l (backend: mock)\n")
	})

	t.Run("a failed resolution stops after the declaration", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, renderAgentShow(&buf, &operations.AgentEntry{Name: "x", Profiles: []string{"p"}},
			nil, errors.New("boom"), nil))
		out := buf.String()
		assert.Contains(t, out, "Resolved llm: unavailable (boom)\n")
		assert.NotContains(t, out, "Composed fragments:")
	})

	t.Run("a write failure is reported, not swallowed", func(t *testing.T) {
		err := renderAgentShow(&failingWriter{err: errWriteRefused}, &operations.AgentEntry{Name: "x"},
			&operations.ResolvedAgent{Label: "l"}, nil, nil)
		assert.ErrorIs(t, err, errWriteRefused)
	})
}

// --- Phase 2: direct tests for the extracted `agent` RunE bodies -------------
//
// Every `agent` leaf's body used to be a func literal inside a
// &cobra.Command{} composite literal, which lizard's Go parser does not see as
// a function at all — so none of them were measurable by the complexity gate,
// and none could be called without going through cobra's argument plumbing.
// These call the extracted functions DIRECTLY.

// agentProject stands a temp project up with a real config.yaml and chdirs
// into it, so the extracted bodies run against a genuine config.Config rather
// than a mock.
func agentProject(t *testing.T, configYAML string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ctxloom", "config.yaml"), []byte(configYAML), 0o644))
	t.Chdir(root)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	return root
}

// textCmd is a bare cobra command wired to a buffer, for calling an extracted
// RunE without registering anything on the root tree.
func textCmd() (*cobra.Command, *bytes.Buffer) {
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c, &buf
}

func TestRunAgentList_EmptyAndPopulated(t *testing.T) {
	t.Run("no agents", func(t *testing.T) {
		agentProject(t, "version: 6\n")
		cmd, out := textCmd()
		require.NoError(t, runAgentList(cmd, nil))
		assert.Contains(t, out.String(), "No agents defined.")
	})

	t.Run("one agent", func(t *testing.T) {
		agentProject(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    profiles: [default]\n")
		cmd, out := textCmd()
		require.NoError(t, runAgentList(cmd, nil))
		got := out.String()
		assert.Contains(t, got, "Agents (1):")
		assert.Contains(t, got, "dev")
		assert.Contains(t, got, "llm: claude-code")
	})
}

// The courtesy "help" shortcut only fires for an ABSENT agent — a DEFINED
// agent literally named "help" must still be showable. Both directions are
// driven here because the guard is a single `if` that reads correctly either
// way if you only test one of them.
func TestRunAgentShow_HelpShortcutOnlyWhenAbsent(t *testing.T) {
	t.Run("absent help renders command help", func(t *testing.T) {
		agentProject(t, "version: 6\n")
		// The REAL command, not a bare one: cobra's help template renders from
		// Use/Short/Long, so a stub command would "pass" by printing nothing.
		var out bytes.Buffer
		agentShowCmd.SetOut(&out)
		agentShowCmd.SetContext(context.Background())
		t.Cleanup(func() { agentShowCmd.SetOut(nil); agentShowCmd.SetContext(context.Background()) })

		require.NoError(t, runAgentShow(agentShowCmd, []string{"help"}),
			"an ABSENT agent named help is the courtesy help request, not an error")
		assert.Contains(t, out.String(), "Usage")
		assert.Contains(t, out.String(), "agent show")
	})

	t.Run("an agent named help is shown, not swallowed", func(t *testing.T) {
		agentProject(t, "version: 6\nagents:\n  help:\n    profiles: [default]\n")
		cmd, out := textCmd()
		require.NoError(t, runAgentShow(cmd, []string{"help"}))
		assert.Contains(t, out.String(), "Agent: help")
	})
}

// checkAgentExistence is the whole point of splitting the old upsert `agent
// set` into create + edit: each verb refuses exactly the case the other owns.
func TestCheckAgentExistence_EachVerbRefusesTheOthersCase(t *testing.T) {
	agentProject(t, "version: 6\nagents:\n  dev:\n    profiles: [default]\n")
	cfg, err := GetConfig()
	require.NoError(t, err)

	assert.NoError(t, checkAgentExistence(cfg, "brand-new", false), "create accepts an unused name")
	assert.NoError(t, checkAgentExistence(cfg, "dev", true), "edit accepts an existing name")

	err = checkAgentExistence(cfg, "dev", false)
	require.Error(t, err, "create must refuse a name that already exists")
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "ctxloom agent edit dev", "the refusal must name the verb that does apply")

	err = checkAgentExistence(cfg, "nope", true)
	require.Error(t, err, "edit must refuse a name nothing defines")
	assert.Contains(t, err.Error(), "no agent named")
	assert.Contains(t, err.Error(), "ctxloom agent create nope")
}

// buildSetAgentRequest must send ONLY the flags the caller typed: a nil field
// means "not named", which SetAgent keeps at its existing value. An unset flag
// leaking through as a non-nil zero value is how `agent edit dev --runtime
// container` used to wipe dev's engine, profiles and escalation ladder.
func TestBuildSetAgentRequest_OnlySendsChangedFlags(t *testing.T) {
	cmd := &cobra.Command{}
	registerAgentWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse([]string{"--runtime", "container"}))

	req := buildSetAgentRequest(cmd, "dev")
	assert.Equal(t, "dev", req.Name)
	require.NotNil(t, req.Runtime, "the flag that WAS typed must be sent")
	assert.Equal(t, "container", *req.Runtime)
	assert.Nil(t, req.LLM, "an untyped flag must stay nil so SetAgent preserves it")
	assert.Nil(t, req.Profiles)
	assert.Nil(t, req.Permissions)
	assert.Nil(t, req.Driving)
}

// An explicitly-supplied empty value is a CLEAR, not a "not named" — the two
// are the same string at the flag layer and only Changed() tells them apart.
func TestBuildSetAgentRequest_ExplicitEmptyIsSentAsAClear(t *testing.T) {
	cmd := &cobra.Command{}
	registerAgentWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse([]string{"--llm", ""}))

	req := buildSetAgentRequest(cmd, "dev")
	require.NotNil(t, req.LLM, `--llm "" must be sent, not treated as unnamed`)
	assert.Equal(t, "", *req.LLM)
}

func TestRenderAgentWritten_NamesWhichVerbRan(t *testing.T) {
	entry := &operations.AgentEntry{Name: "dev", Profiles: []string{"a", "b"}, Runtime: "container"}

	var created bytes.Buffer
	require.NoError(t, renderAgentWritten(&created, entry, false))
	assert.Contains(t, created.String(), `Created agent "dev"`)
	assert.Contains(t, created.String(), "profiles: a, b")
	assert.Contains(t, created.String(), "runtime: container")

	var edited bytes.Buffer
	require.NoError(t, renderAgentWritten(&edited, entry, true))
	assert.Contains(t, edited.String(), `Updated agent "dev"`)
}

// An agent with no declared engine reads as the project default, not as an
// empty string the user has to interpret.
func TestRenderAgentWritten_BlankEngineReadsAsProjectDefault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderAgentWritten(&buf, &operations.AgentEntry{Name: "dev"}, false))
	assert.Contains(t, buf.String(), "llm: project default")
}

func TestRenderDefaultAgent_BothArms(t *testing.T) {
	var unset bytes.Buffer
	require.NoError(t, renderDefaultAgent(iox.NewErrWriter(&unset), ""))
	assert.Contains(t, unset.String(), "No default agent set.")

	var set bytes.Buffer
	require.NoError(t, renderDefaultAgent(iox.NewErrWriter(&set), "dev"))
	assert.Contains(t, set.String(), "Default agent: dev")
}

// `agent default <name>` is advisory about an unknown name (it warns and binds
// anyway) but must still PERSIST the choice — a warn-and-do-nothing would look
// identical on stdout.
func TestRunAgentDefault_PersistsTheBinding(t *testing.T) {
	root := agentProject(t, "version: 6\nagents:\n  dev:\n    profiles: [default]\n")
	cmd, out := textCmd()
	require.NoError(t, runAgentDefault(cmd, []string{"dev"}))
	assert.Contains(t, out.String(), `Set default agent to "dev"`)

	raw, err := os.ReadFile(filepath.Join(root, ".ctxloom", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "dev", "the binding must reach disk, not just stdout")

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	assert.Equal(t, "dev", cfg.GetDefaultAgent())
}

// TestRunAgentRemove_BareReportsAndDestroysNothing pins the report side: a
// bare `agent remove` (agentRemoveYes == false) must say plainly that nothing
// was removed, name the --yes command to apply it, and — the assertion that
// actually catches a broken guard — leave the agent in config afterward. A
// "preview" that quietly removes anyway passes a test that only checks
// output text; this one re-reads config.
func TestRunAgentRemove_BareReportsAndDestroysNothing(t *testing.T) {
	agentProject(t, "version: 6\nagents:\n  dev:\n    profiles: [default]\n")
	agentRemoveYes = false
	cmd, out := textCmd()
	require.NoError(t, runAgentRemove(cmd, []string{"dev"}))
	assert.Contains(t, out.String(), "Nothing was removed")
	assert.Contains(t, out.String(), "--yes")

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.Agent("dev")
	assert.True(t, ok, "the bare (no --yes) path must leave the agent in config")
}

// TestRunAgentRemove_YesRemovesAndReports pins the apply side: --yes must
// actually remove the agent from config, not just print that it did. Paired
// with the bare-path test above so a regression in either direction — bare
// destroys, or --yes no-ops — is caught by an assertion on the agent's
// continued (non-)existence in config.
func TestRunAgentRemove_YesRemovesAndReports(t *testing.T) {
	agentProject(t, "version: 6\nagents:\n  dev:\n    profiles: [default]\n")
	agentRemoveYes = true
	t.Cleanup(func() { agentRemoveYes = false })
	cmd, out := textCmd()
	require.NoError(t, runAgentRemove(cmd, []string{"dev"}))
	assert.Contains(t, out.String(), `Removed agent "dev"`)

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.Agent("dev")
	assert.False(t, ok, "--yes must actually remove the agent from config, not merely report it gone")
}
