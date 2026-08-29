//go:build parked_engines

package kiro

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestKiro_SetupExecute_AgreeOnAgentName is the end-to-end PAYLOAD test for a
// real bug: a configured `agent:` override used to reach buildArgs'
// `--agent <name>` while Setup's writeAgentConfig kept writing the
// materialized custom-agent JSON under the hardcoded defaultAgentName, both
// as the file's path AND its "name" field — so kiro-cli was told to select an
// agent that was never materialized (or a stale/default one), a broken
// launch. Setup and buildArgs must resolve to the SAME name.
func TestKiro_SetupExecute_AgreeOnAgentName(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{Agent: "myagent"})

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:  work,
		CellKind: agent.CellKindDirectoryIsolated,
		Managed:  &agent.ManagedConfig{},
	}))

	// buildArgs launches with the configured override.
	args := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "hi"}}, "")
	require.Contains(t, args, "--agent")
	idx := -1
	for i, a := range args {
		if a == "--agent" {
			idx = i
		}
	}
	require.NotEqual(t, -1, idx)
	launchedName := args[idx+1]
	assert.Equal(t, "myagent", launchedName)

	// The file Setup actually materialized must exist AT that exact name, and
	// its own "name" field (what kiro-cli itself uses to identify the agent)
	// must agree too.
	path := filepath.Join(work, ".kiro", "agents", launchedName+".json")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the agent Setup materialized must be the one buildArgs selects with --agent")

	var decoded struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "myagent", decoded.Name, "the materialized agent's own name field must match what --agent selects")
}

func TestKiroConfig_BackendType(t *testing.T) {
	assert.Equal(t, "kiro", KiroConfig{}.BackendType())
}

func TestNewKiro_Defaults(t *testing.T) {
	b := NewKiro()
	assert.Equal(t, "kiro", b.Name())
	assert.Equal(t, "kiro-cli", b.BinaryPath)
}

// TestNewKiro_HasNoSessionHistoryReader pins the fact several comments in this
// package rest on: kiro's sessions/cli/*.jsonl scraper was DELETED, not
// demoted — it was confirmed broken against the v2 SQLite store a real
// `kiro-cli chat --no-interactive` writes, and canonical capture is the only
// transcript source now. Anything reintroducing a reader here must revisit
// those comments, and KIRO_HOME's scope with them.
func TestNewKiro_HasNoSessionHistoryReader(t *testing.T) {
	assert.Nil(t, NewKiro().History(), "kiro declares no session history; it must not answer an empty list nobody can tell from 'genuinely none'")
}

func TestKiro_Configure(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{
		BinaryPath:  "/opt/kiro-cli",
		Args:        []string{"-v"},
		Env:         map[string]string{"FOO": "bar"},
		Effort:      "high",
		Agent:       "myagent",
		AgentEngine: "v3",
	})
	assert.Equal(t, "/opt/kiro-cli", b.BinaryPath)
	assert.Equal(t, []string{"-v"}, b.Args)
	assert.Equal(t, "bar", b.Env["FOO"])
	assert.Equal(t, "high", b.effort)
	assert.Equal(t, "myagent", b.agentName)
	assert.Equal(t, "v3", b.agentEngine)
}

// TestKiro_ConfigureThinkingIsWarnedNoOp pins the honest-no-op contract: kiro
// has no wired mechanism for the cross-engine normalized thinking level
// (unlike claude/codex — see internal/claude/chat.go, internal/codex/chat.go),
// so an explicit `thinking` setting must WARN rather than silently vanish.
func TestKiro_ConfigureThinkingIsWarnedNoOp(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	b := NewKiro()
	b.Configure(&KiroConfig{Thinking: "high"})
	_ = w.Close()
	os.Stderr = orig

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "thinking", "an explicit setting must be surfaced, not silently swallowed")
	assert.Contains(t, string(out), "high")
}

func TestKiro_ConfigureIgnoresForeignConfig(t *testing.T) {
	b := NewKiro()
	b.Configure(nil)
	assert.Equal(t, "kiro-cli", b.BinaryPath)
	assert.Equal(t, defaultAgentName, b.agentName)
}

// foreignConfig is a BackendConfig this backend cannot read — the mis-wiring
// shape Configure's type assertion guards against.
type foreignConfig struct{}

func (foreignConfig) BackendType() string { return "not-kiro" }

// TestKiro_ConfigureForeignConfigIsWarned pins a real bug: a config Configure
// cannot read drops EVERY override (binary path, args, env, effort, agent,
// agent-engine) and the run then launches on defaults. Staying silent about
// that surfaces the mis-wiring a whole session later as a launch that ignored
// the user's config, naming neither the cause nor the config that caused it —
// the same reason internal/acp's Configure warns on its own foreign-config arm.
// A nil config is the same class and must not panic while saying so.
func TestKiro_ConfigureForeignConfigIsWarned(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  agent.BackendConfig
		want string
	}{
		{"foreign typed config", foreignConfig{}, "not-kiro"},
		{"nil config", nil, "<nil>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			require.NoError(t, err)
			orig := os.Stderr
			os.Stderr = w

			b := NewKiro()
			b.Configure(tt.cfg)
			_ = w.Close()
			os.Stderr = orig

			out, err := io.ReadAll(r)
			require.NoError(t, err)
			assert.Contains(t, string(out), tt.want,
				"a config this backend cannot read must be reported, not silently dropped")
			assert.Equal(t, "kiro-cli", b.BinaryPath, "no override may be applied from an unreadable config")
		})
	}
}

// TestKiro_Execute_RefusesEmptyOneshotPrompt pins the headless-turn invariant
// the shared ACP-shaped path already enforces (agent.RunOneshotTurn: "an empty
// prompt now refuses before the engine is ever invoked"). kiro's exec-style
// oneshot appends `--no-interactive` unconditionally but appends the prompt only
// when non-empty, so a blank prompt launched `kiro-cli chat --no-interactive`
// with NO input positional and nil stdin — a headless turn that asks nothing,
// the exit-0-with-zero-bytes shape. The engine must not be spawned at all.
func TestKiro_Execute_RefusesEmptyOneshotPrompt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prompt *agent.Fragment
	}{
		{"nil prompt", nil},
		{"empty prompt", &agent.Fragment{}},
		{"whitespace-only prompt", &agent.Fragment{Content: " \n\t "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			launched := false
			b := NewKiro()
			b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
				launched = true
				return 0, nil
			})

			_, err := b.Execute(context.Background(),
				&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: tt.prompt},
				io.Discard, io.Discard)

			require.Error(t, err, "a oneshot that asks nothing must be refused, not launched")
			assert.False(t, launched, "the engine must never be spawned for an empty oneshot prompt")
		})
	}
}

// TestKiro_Execute_InteractiveEmptyPromptIsLegitimate is the other half: an
// interactive launch with no prompt opens a session for the human to drive, so
// it must keep working (cf. internal/cli/run.go's identical --print-only floor).
func TestKiro_Execute_InteractiveEmptyPromptStillLaunches(t *testing.T) {
	launched := false
	b := NewKiro()
	b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
		launched = true
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeInteractive}, io.Discard, io.Discard)

	require.NoError(t, err)
	assert.True(t, launched, "an interactive session with no opening prompt is legitimate")
}

func TestKiro_BuildArgs(t *testing.T) {
	tests := []struct {
		name      string
		configure *KiroConfig
		req       *agent.ExecuteRequest
		model     string
		want      []string
	}{
		{
			name: "oneshot prompt is headless positional",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "do it"}},
			want: []string{"chat", "--agent", "ctxloom", "--no-interactive", "do it"},
		},
		{
			name: "interactive prompt stays in session",
			req:  &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "do it"}},
			want: []string{"chat", "--agent", "ctxloom", "do it"},
		},
		{
			name:  "model pinned",
			req:   &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}},
			model: "claude-sonnet-5",
			want:  []string{"chat", "--agent", "ctxloom", "--model", "claude-sonnet-5", "--no-interactive", "x"},
		},
		{
			name:      "effort and agent-engine from config",
			configure: &KiroConfig{Effort: "high", AgentEngine: "v3"},
			req:       &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}},
			want:      []string{"chat", "--agent", "ctxloom", "--effort", "high", "--agent-engine", "v3", "--no-interactive", "x"},
		},
		{
			name: "auto approve trusts all tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-all-tools", "--no-interactive", "x"},
		},
		{
			// SkipSetup (minimal/distill) always maps to the read-only allowlist,
			// overriding a requested bypass and also suppressing agent selection
			// — matching codex's identical SkipSetup-wins switch shape.
			name: "skip setup forces read-only trust-tools even over requested bypass",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--trust-tools=fs_read", "--no-interactive", "x"},
		},
		{
			name: "plan trusts read-only tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-tools=fs_read", "--no-interactive", "x"},
		},
		{
			name: "acceptEdits trusts read+write tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-tools=fs_read,fs_write", "--no-interactive", "x"},
		},
		{
			name:      "custom agent name",
			configure: &KiroConfig{Agent: "reviewer"},
			req:       &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "hi"}},
			want:      []string{"chat", "--agent", "reviewer", "hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewKiro()
			if tt.configure != nil {
				b.Configure(tt.configure)
			}
			assert.Equal(t, tt.want, b.buildArgs(tt.req, tt.model))
		})
	}
}

// singleArgLimitForTest mirrors, independently of the production code, the
// largest byte length one argv element may carry on this host: Linux caps a
// SINGLE argument at MAX_ARG_STRLEN = 32 * PAGE_SIZE bytes INCLUDING the
// terminating NUL, regardless of the far larger total ARG_MAX. Probed on this
// box (4096-byte pages): a 131071-byte argument execs, 131072 fails E2BIG.
func singleArgLimitForTest() int { return 32*os.Getpagesize() - 1 }

// TestKiro_Execute_RefusesOversizedPromptBeforeExec pins the fail-loud half of
// the argv capacity limit. kiro-cli takes the prompt as the trailing INPUT
// positional (buildArgs), so a prompt past MAX_ARG_STRLEN cannot exec at all —
// and before this refusal the user saw only os/exec's generic
// "fork/exec /path/to/kiro-cli: argument list too long", which names neither
// the prompt nor its length.
//
// Truncating is not an option: a silently shortened prompt would run, answer a
// question nobody asked, and say nothing about it.
func TestKiro_Execute_RefusesOversizedPromptBeforeExec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux per-argument cap; other platforms limit only the total")
	}
	oversize := singleArgLimitForTest() + 1
	prompt := strings.Repeat("x", oversize)

	launched := false
	b := NewKiro()
	b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
		launched = true
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: prompt}},
		io.Discard, io.Discard)

	require.Error(t, err, "a prompt past the single-argument limit cannot exec — it must be refused, not attempted")
	assert.False(t, launched, "the refusal must happen BEFORE exec, not as a launch failure")
	assert.Contains(t, err.Error(), strconv.Itoa(oversize),
		"the refusal must name the PROMPT'S OWN LENGTH as the cause; %q does not", err.Error())
	assert.Contains(t, err.Error(), "prompt",
		"the refusal must name the prompt as the oversized payload; %q does not", err.Error())
	assert.NotContains(t, err.Error(), "truncat",
		"nothing here may offer to shorten the prompt: %q", err.Error())
}

// TestKiro_Execute_PromptAtTheLimitReachesArgvUnchanged is the other half. A
// refusal that fires early breaks every ordinary run, and a prompt of EXACTLY
// the largest length exec accepts must still travel byte-for-byte.
func TestKiro_Execute_PromptAtTheLimitReachesArgvUnchanged(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux per-argument cap; other platforms limit only the total")
	}
	prompt := strings.Repeat("y", singleArgLimitForTest())

	var got []string
	b := NewKiro()
	b.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		got = spec.Args
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: prompt}},
		io.Discard, io.Discard)

	require.NoError(t, err, "a prompt at exactly the single-argument limit still execs and must not be refused")
	require.NotEmpty(t, got, "the engine must actually have been launched")
	last := got[len(got)-1]
	require.True(t, last == prompt,
		"the prompt must reach argv byte-for-byte: got %d bytes, want %d", len(last), len(prompt))
}
