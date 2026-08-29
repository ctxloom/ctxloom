package mockengine_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// writeFile writes rel (which may contain slashes) under dir, creating parents.
func writeFile(t *testing.T, dir, rel string, body []byte) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runOneshot drives a Runtime over claude's oneshot surface with the given
// vendor argv and stdin prompt, returning stdout, the extracted report, and the
// exit code.
func runOneshot(t *testing.T, cwd string, vendorArgv []string, prompt string, env map[string]string) (string, mockengine.Report, int) {
	t.Helper()
	cli := claudeOneshot(t)
	parsed, err := cli.ParseArgv(vendorArgv)
	if err != nil {
		t.Fatalf("parse argv %v: %v", vendorArgv, err)
	}
	var stdout, stderr bytes.Buffer
	rt := &mockengine.Runtime{
		CLI:    cli,
		Argv:   parsed,
		Res:    mockengine.Resolver{Cwd: cwd, Home: t.TempDir(), Getenv: func(string) string { return "" }},
		Getenv: func(k string) string { return env[k] },
		Stdin:  strings.NewReader(prompt),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	code := rt.Run()
	rep, err := mockengine.ExtractReport(stderr.String())
	if err != nil {
		t.Fatalf("extract report from stderr: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String(), rep, code
}

// envelope mirrors parseClaudeJSONResult's read (internal/claude): the driver
// picks the model with the most outputTokens and returns result. This test
// asserts the mock emits exactly what that decode expects under SkipSetup.
type envelope struct {
	Result     string `json:"result"`
	ModelUsage map[string]struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// TestRuntime_OneshotJSONEnvelopeUnderSkipSetup proves that when the argv
// carries --output-format json (the SkipSetup signal the driver keys its json
// decode on), stdout is the {result, modelUsage} envelope, attributed to the
// --model value — not plain text, which would make the driver's decode fail on
// a run it believed succeeded.
func TestRuntime_OneshotJSONEnvelopeUnderSkipSetup(t *testing.T) {
	cwd := t.TempDir()
	argv := []string{"--print", "--output-format", "json", "--model", "claude-sonnet-4-6"}
	stdout, _, code := runOneshot(t, cwd, argv, "review this diff", nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var env envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not the JSON envelope the driver parses: %v\nstdout: %s", err, stdout)
	}
	if env.Result == "" {
		t.Fatal("envelope result is empty")
	}
	if _, ok := env.ModelUsage["claude-sonnet-4-6"]; !ok {
		t.Fatalf("envelope did not attribute usage to the --model value: %+v", env.ModelUsage)
	}
}

// TestRuntime_OneshotPlainWithoutJSONFlag proves that without the json flag the
// oneshot result is plain text (the normal claude -p shape).
func TestRuntime_OneshotPlainWithoutJSONFlag(t *testing.T) {
	stdout, _, code := runOneshot(t, t.TempDir(), []string{"--print"}, "hello", nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("expected plain text without the json flag, got JSON: %s", stdout)
	}
	if stdout == "" {
		t.Fatal("plain oneshot produced no result")
	}
}

// codexOneshot is a HAND-BUILT replica of codex's real oneshot declaration
// (internal/codex/enginecli.go's CodexEngineCLIs: the "exec" subcommand, the
// POSITIONAL prompt, --model/--sandbox/--dangerously-bypass-approvals-and-
// sandbox, no --output-format flag).
//
// parked_engines: internal/codex is out of the default build, so this can no
// longer be fetched live the way the file's own precedent for an
// unavailable declaration (stripCLI, arch_test.go) already established for
// StripEnv. It goes stale the moment codex's real declaration diverges from
// it — re-derive it live once codex returns rather than trusting this copy.
func codexOneshot(t *testing.T) agent.EngineCLI {
	t.Helper()
	return agent.EngineCLI{
		Engine:     "codex",
		Surface:    agent.CLISurfaceOneshot,
		Binary:     "codex",
		Subcommand: "exec",
		Prompt:     agent.PromptPositional,
		Flags: []agent.CLIFlag{
			{Name: "--model", Value: agent.ValueString},
			{Name: "--sandbox", Value: agent.ValueString, Enum: []string{"read-only", "workspace-write", "danger-full-access"}},
			{Name: "--dangerously-bypass-approvals-and-sandbox", Value: agent.ValueNone, ConflictsWith: []string{"--sandbox"}},
		},
		SetEnv: []string{"CODEX_HOME", agent.SCMContextFileEnv, agent.SessionHarpEnv},
		Probes: []agent.CLIProbe{
			{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: "AGENTS.md"},
			{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: agent.SCMContextSubdir, Dir: true},
			{Kind: agent.ProbeKindSettings, Scope: agent.ScopeEnvDir, EnvVar: "CODEX_HOME", EnvHomeDefault: ".codex", Rel: "config.toml"},
			{Kind: agent.ProbeKindCommands, Scope: agent.ScopeEnvDir, EnvVar: "CODEX_HOME", EnvHomeDefault: ".codex", Rel: "prompts", Dir: true},
			{Kind: agent.ProbeKindSkills, Scope: agent.ScopeEnvDir, EnvVar: "CODEX_HOME", EnvHomeDefault: ".codex", Rel: "skills", Dir: true},
		},
	}
}

// runCodexOneshot drives a Runtime over codex's oneshot surface. The prompt is
// passed as the trailing argv POSITIONAL (codex's real delivery, PromptPositional
// in L1); the stdin here is deliberately DISTINCT garbage, so a runtime that
// wrongly read the prompt from stdin would produce a different promptSha256 and
// be caught. argvExtra is spliced after the `exec` subcommand so a test can add
// or omit posture flags.
func runCodexOneshot(t *testing.T, cwd string, argvExtra []string, prompt string) (string, mockengine.Report, int) {
	t.Helper()
	cli := codexOneshot(t)
	argv := append([]string{"exec"}, argvExtra...)
	argv = append(argv, prompt)
	parsed, err := cli.ParseArgv(argv)
	if err != nil {
		t.Fatalf("parse codex argv %v: %v", argv, err)
	}
	var stdout, stderr bytes.Buffer
	rt := &mockengine.Runtime{
		CLI:    cli,
		Argv:   parsed,
		Res:    mockengine.Resolver{Cwd: cwd, Home: t.TempDir(), Getenv: func(string) string { return "" }},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader("STDIN-MUST-BE-IGNORED-FOR-CODEX"),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	code := rt.Run()
	rep, err := mockengine.ExtractReport(stderr.String())
	if err != nil {
		t.Fatalf("extract report from stderr: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String(), rep, code
}

// TestRuntime_CodexOneshotIsPlainTextNotEnvelope proves codex's oneshot WIRE
// contract: codex's driver (internal/codex/backend.go:404 Execute →
// ExecuteCLI → RunNonInteractive at internal/shared/agent/launch_backend.go:141)
// streams the child's stdout straight through with NO envelope parsing — unlike
// claude, which buffers and decodes {result,modelUsage} under --output-format
// json (parseClaudeJSONResult). So the mock as codex must emit PLAIN assistant
// text; a JSON envelope here is bytes codex's driver would hand back verbatim as
// the "assistant reply". It also proves the prompt was read from the POSITIONAL
// (the promptSha256 matches the argv prompt, not the distinct stdin garbage).
func TestRuntime_CodexOneshotIsPlainTextNotEnvelope(t *testing.T) {
	prompt := "summarize the project rules"
	stdout, rep, code := runCodexOneshot(t, t.TempDir(), []string{"--sandbox", "read-only"}, prompt)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("codex oneshot emitted a JSON envelope; codex's driver has no decoder and would surface this verbatim: %s", stdout)
	}
	if stdout == "" {
		t.Fatal("codex oneshot produced no response text")
	}
	if rep.Engine != "codex" {
		t.Fatalf("report engine = %q, want codex", rep.Engine)
	}
	if want := sha256hex([]byte(prompt)); rep.PromptSHA256 != want {
		t.Fatalf("promptSha256 = %s, want %s (the POSITIONAL prompt, not stdin)", rep.PromptSHA256, want)
	}
}

// TestRuntime_CodexWireIndependentOfClaudeOutputFormat is the ROUTING guard and
// the red gate. The oneshot surface is shared, but the stdout contract is
// per-engine and MUST be selected by engine identity, never by claude's flag
// vocabulary. This constructs a codex ParsedArgv that (synthetically — codex's
// grammar would reject it, which is exactly why it is built directly) carries
// claude's --output-format json signal, and asserts the codex wire STILL emits
// plain text. Before the codex wire adapter existed, render routed every oneshot
// engine through claude's adapter, which keys the JSON envelope on that flag
// value — so codex would have emitted an envelope here. This pins that codex's
// wire cannot be recaptured by claude's --output-format convention.
func TestRuntime_CodexWireIndependentOfClaudeOutputFormat(t *testing.T) {
	cli := codexOneshot(t)
	// Built directly, not via ParseArgv: codex declares no --output-format, so
	// ParseArgv would (correctly) reject it. The point is to feed the claude
	// envelope trigger to codex's wire and prove the wire ignores it.
	argv := agent.ParsedArgv{
		Subcommand:  "exec",
		Positionals: []string{"do the thing"},
		Flags: []agent.ParsedFlag{
			{Flag: agent.CLIFlag{Name: "--output-format", Value: agent.ValueString}, Value: "json"},
		},
	}
	var stdout, stderr bytes.Buffer
	rt := &mockengine.Runtime{
		CLI:    cli,
		Argv:   argv,
		Res:    mockengine.Resolver{Cwd: t.TempDir(), Home: t.TempDir(), Getenv: func(string) string { return "" }},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if code := rt.Run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
		t.Fatalf("codex wire honored claude's --output-format json (emitted an envelope) — the wire is coupled to the wrong engine: %s", stdout.String())
	}
}

// TestRuntime_FailSentinelExitsNonzero proves the fault path: a prompt
// containing the fail sentinel (as a SUBSTRING — composed context is prepended)
// exits nonzero, and the discovery report is STILL emitted, because a failing
// run is exactly when the evidence matters most.
func TestRuntime_FailSentinelExitsNonzero(t *testing.T) {
	prompt := "here is a lot of composed context...\n" + mockengine.SentinelFail + "\n...and a task"
	_, rep, code := runOneshot(t, t.TempDir(), []string{"--print"}, prompt, nil)
	if code == 0 {
		t.Fatal("fail sentinel did not produce a nonzero exit")
	}
	// This used to read `rep.PromptSHA256 == ""`, which could not fail: the
	// hash was taken over a nil prompt too. Assert the PRESENCE bit and the
	// delivered bytes, both of which a missing report cannot fake.
	if !rep.PromptPresent {
		t.Fatal("report was not emitted with the prompt it received on the failing run")
	}
	if rep.PromptSHA256 != sha256hex([]byte(prompt)) {
		t.Fatalf("promptSha256 = %s, want the hash of the delivered bytes", rep.PromptSHA256)
	}
}

// TestRuntime_PromptHashMatchesReceivedBytes proves promptSha256 is over the
// EXACT bytes the child received on stdin — the check that a composed prompt
// reached the engine unmangled.
func TestRuntime_PromptHashMatchesReceivedBytes(t *testing.T) {
	prompt := "exact bytes 123\nwith newline"
	_, rep, _ := runOneshot(t, t.TempDir(), []string{"--print"}, prompt, nil)
	if want := sha256hex([]byte(prompt)); rep.PromptSHA256 != want {
		t.Fatalf("promptSha256 = %s, want %s", rep.PromptSHA256, want)
	}
	if rep.PromptSize != int64(len(prompt)) {
		t.Fatalf("promptSize = %d, want %d", rep.PromptSize, len(prompt))
	}
}

// TestReport_DigestExcludesAbsolutePaths proves the DiscoveryDigest is a
// reproducible fingerprint of WHAT was found, not WHERE on disk — the same
// bytes delivered to the same declared surfaces under two different roots yield
// the same digest. Without this a test could not assert one fixed digest value.
func TestReport_DigestExcludesAbsolutePaths(t *testing.T) {
	cli := claudeOneshot(t)
	argv, _ := cli.ParseArgv([]string{"--print"})
	body := []byte("# CLAUDE.md\nsame bytes\n")

	digest := func() string {
		cwd := t.TempDir()
		writeFile(t, cwd, "CLAUDE.md", body)
		recs := mockengine.Walk(cli, argv, mockengine.Resolver{Cwd: cwd, Home: t.TempDir(), Getenv: func(string) string { return "" }})
		return mockengine.BuildReport(cli, recs, nil, nil).DiscoveryDigest
	}
	if a, b := digest(), digest(); a != b {
		t.Fatalf("digest varied with the absolute root: %s != %s", a, b)
	}
}

// TestRuntime_UndeclaredFlagIsLoud proves the mock rejects an argv flag L1 does
// not declare, rather than tolerating drift silently. (Exercised via ParseArgv,
// the shared reader.)
func TestRuntime_UndeclaredFlagIsLoud(t *testing.T) {
	cli := claudeOneshot(t)
	if _, err := cli.ParseArgv([]string{"--print", "--totally-made-up"}); err == nil {
		t.Fatal("an undeclared flag parsed cleanly — drift would hide here")
	} else if _, ok := err.(*agent.UndeclaredFlagError); !ok {
		t.Fatalf("want UndeclaredFlagError, got %T: %v", err, err)
	}
}
