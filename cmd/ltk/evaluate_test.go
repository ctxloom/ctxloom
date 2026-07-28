package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// evaluate is the full decision path (decode → parse → rules → encode) minus
// process concerns (stdin, stream writes, exit), so it is testable end to end.
func TestEvaluateDeniesAndAllows(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := `version: 1
rules:
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "no force pushes"
    suggest: "git push --force-with-lease"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := func(command string) string {
		return `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	}

	t.Run("denied command produces a deny decision on stdout", func(t *testing.T) {
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload("git push --force")))
		if err != nil {
			t.Fatal(err)
		}
		if out.ExitCode != 0 {
			t.Errorf("claude-code denials exit 0, got %d", out.ExitCode)
		}
		if !strings.Contains(string(out.Stdout), `"deny"`) || !strings.Contains(string(out.Stdout), "no force pushes") {
			t.Errorf("stdout should carry the deny decision and reason, got %s", out.Stdout)
		}
	})

	t.Run("allowed command writes nothing", func(t *testing.T) {
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload("git status")))
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Stdout) != 0 || len(out.Stderr) != 0 || out.ExitCode != 0 {
			t.Errorf("allow must be a zero Output (pass-through), got %+v", out)
		}
	})

	t.Run("malformed payload fails closed (denies, not errors)", func(t *testing.T) {
		// A payload that can't be decoded must DENY, not exit non-zero: both hosts
		// treat a non-zero exit as a silent allow, so an undecodable/version-skewed
		// payload would otherwise sail past the guard ungated.
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader("not json"))
		if err != nil {
			t.Fatalf("malformed payload must not surface as an error: %v", err)
		}
		if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `"deny"`) {
			t.Errorf("malformed payload should produce a deny decision, got %+v", out)
		}
	})

	t.Run("wrapped denied command is denied (bash -ec cluster)", func(t *testing.T) {
		// `bash -ec 'git push --force'` bundles -c into a short-option cluster; it
		// must still be expanded and matched, or the wrapper is a trivial bypass.
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload("bash -ec 'git push --force'")))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), `"deny"`) || !strings.Contains(string(out.Stdout), "no force pushes") {
			t.Errorf("bash -ec wrapper must not bypass the rule, got %s", out.Stdout)
		}
	})
}

// A rules file that exists but cannot be parsed must DENY, not error: both hook
// hosts fail OPEN on a non-zero exit (Claude Code treats exit 1 as non-blocking,
// agy proceeds on any crashing hook), so a one-character typo in .ltk/config.yaml
// erroring out would silently disable every rule.
func TestEvaluateFailsClosedOnBrokenConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	// A typo'd key: rules.Parse rejects unknown fields.
	broken := "version: 1\nrulez:\n  - id: oops\n"
	if err := os.WriteFile(cfgPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	payloads := map[string]string{
		"claude-code": `{"tool_name":"Bash","tool_input":{"command":"git push --force"}}`,
		"antigravity": `{"toolCall":{"name":"run_command","args":{"CommandLine":"git push --force"}}}`,
	}
	for engineName, payload := range payloads {
		t.Run(engineName, func(t *testing.T) {
			out, err := evaluate(engineName, cfgPath, "", strings.NewReader(payload))
			if err != nil {
				t.Fatalf("a broken config must yield a decision, not an error (exit 1 fails open): %v", err)
			}
			if out.ExitCode != 0 {
				t.Errorf("fail-closed decision must exit 0, got %d", out.ExitCode)
			}
			if !strings.Contains(string(out.Stdout), `"deny"`) {
				t.Errorf("stdout should carry a deny decision, got %s", out.Stdout)
			}
			if !strings.Contains(string(out.Stdout), "rulez") {
				t.Errorf("the deny reason should carry the parse error so the user can fix it, got %s", out.Stdout)
			}
		})
	}

	t.Run("missing explicit --config also fails closed", func(t *testing.T) {
		// A typo'd --config path in an installed hook command is the same
		// persistent fail-open hole as a broken file.
		out, err := evaluate("claude-code", filepath.Join(t.TempDir(), "nope.yaml"), "",
			strings.NewReader(payloads["claude-code"]))
		if err != nil {
			t.Fatalf("missing --config must deny, not error: %v", err)
		}
		if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `"deny"`) {
			t.Errorf("want deny decision with exit 0, got %+v", out)
		}
	})
}

// An unknown --shell would otherwise hit ErrUnsupportedShell on every parse,
// which the default on_parse_error: allow converts into a permanent silent
// allow-all. A typo'd installed hook command is exactly the persistent case,
// so the hook path must deny.
func TestEvaluateFailsClosedOnUnknownShell(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := "version: 1\nrules:\n  - id: x\n    match: { command: [git, push, --force] }\n    message: no\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	payloads := map[string]string{
		"claude-code": `{"tool_name":"Bash","tool_input":{"command":"git status"}}`,
		"antigravity": `{"toolCall":{"name":"run_command","args":{"CommandLine":"git status"}}}`,
	}
	for engineName, payload := range payloads {
		t.Run(engineName, func(t *testing.T) {
			out, err := evaluate(engineName, cfgPath, "fish", strings.NewReader(payload))
			if err != nil {
				t.Fatalf("unknown --shell must deny, not error: %v", err)
			}
			if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `"deny"`) {
				t.Errorf("want deny decision with exit 0, got %+v", out)
			}
			if !strings.Contains(string(out.Stdout), "fish") {
				t.Errorf("the deny reason should name the bad shell, got %s", out.Stdout)
			}
		})
	}

	t.Run("a known shell still evaluates normally", func(t *testing.T) {
		out, err := evaluate("claude-code", cfgPath, "bash", strings.NewReader(payloads["claude-code"]))
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Stdout) != 0 || out.ExitCode != 0 {
			t.Errorf("git status under --shell bash must pass through, got %+v", out)
		}
	})
}

// An unknown --engine in the installed hook would otherwise exit 1, which both
// hosts treat as a silent allow. There is no adapter for the named engine, so
// evaluate falls back to claude-code's wire format purely to emit a deny.
func TestEvaluateFailsClosedOnUnknownEngine(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := "version: 1\nrules:\n  - id: x\n    match: { command: [git, push, --force] }\n    message: no\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"tool_name":"Bash","tool_input":{"command":"git status"}}`
	out, err := evaluate("not-an-engine", cfgPath, "", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("unknown --engine must deny, not error: %v", err)
	}
	if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `"deny"`) {
		t.Errorf("want deny decision with exit 0, got %+v", out)
	}
	if !strings.Contains(string(out.Stdout), "not-an-engine") {
		t.Errorf("the deny reason should name the bad engine, got %s", out.Stdout)
	}
}

// TestEvaluateUnknownEngineDetectsRealHostFromPayload pins U005-F04: the
// unknown-engine fail-closed branch must not blindly guess claude-code's wire
// format. Before the fix it always encoded the deny as claude-code — correct
// by accident when the real host IS claude-code, but on an actual Antigravity
// host (payload shaped `{"toolCall":{...}}`) the deny rode a format agy does
// not recognize, so agy's own doc says it "proceeds on any crashing/unreadable
// hook" — the fail-closed deny was silently invisible, i.e. failed OPEN in
// practice. With the fix, evaluate tries every registered engine's Decode
// against the actual payload and fails closed in the format that decoded it.
func TestEvaluateUnknownEngineDetectsRealHostFromPayload(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := "version: 1\nrules:\n  - id: x\n    match: { command: [git, push, --force] }\n    message: no\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// An antigravity-shaped payload, but the installed hook names a bogus
	// --engine (a stale/mistyped hook command).
	payload := `{"toolCall":{"name":"run_command","args":{"CommandLine":"git status"}}}`
	out, err := evaluate("not-an-engine", cfgPath, "", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("unknown --engine must deny, not error: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("want exit 0 (fail closed, not fail open), got %+v", out)
	}
	got := string(out.Stdout)
	if strings.Contains(got, "hookSpecificOutput") || strings.Contains(got, "permissionDecision") {
		t.Errorf("payload was antigravity-shaped but the deny rode claude-code's wire format (invisible to the real host): %s", got)
	}
	if !strings.Contains(got, `"decision":"deny"`) {
		t.Errorf("want antigravity's own deny shape, got %s", got)
	}
}

// TestEvaluateFindsConfigFromHookCwd pins the default-config search against
// Antigravity's hook environment: agy runs hooks with cwd <workspace>/.agents,
// so the search must walk up to the repository root and find the project's
// rules — falling back to the built-in allow-all config from there would make
// the guard silently approve everything.
func TestEvaluateFindsConfigFromHookCwd(t *testing.T) {
	ws := t.TempDir()
	// Mark the workspace as the repository root and place rules + an
	// out-of-repo decoy that the walk must NOT pick up.
	for _, dir := range []string{".git", ".ltk", ".agents"} {
		if err := os.MkdirAll(filepath.Join(ws, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `version: 1
rules:
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "no force pushes"
`
	if err := os.WriteFile(filepath.Join(ws, ".ltk", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	decoy := `version: 1
rules:
  - id: decoy
    match: { command: [git] }
    message: "decoy above the repo root must not load"
`
	if err := os.WriteFile(filepath.Join(filepath.Dir(ws), ".ltk.yaml"), []byte(decoy), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(filepath.Join(ws, ".agents"))

	agyPayload := `{"toolCall":{"name":"run_command","args":{"CommandLine":"git push --force","Cwd":"` + ws + `"}}}`
	out, err := evaluate("antigravity", "", "", strings.NewReader(agyPayload))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Stdout), `"deny"`) || !strings.Contains(string(out.Stdout), "no force pushes") {
		t.Fatalf("project rules must apply from the .agents hook cwd, got %+v", out)
	}

	allowOut, err := evaluate("antigravity", "", "", strings.NewReader(
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git status"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(allowOut.Stdout), "decoy") {
		t.Fatalf("config above the repository root must not be loaded: %+v", allowOut)
	}
}

// TestEvaluateNoConfigFoundAnywhereWarns is a DEFECT WITH NO CENSUS ROW found
// while walking the guardrail flow's fail-open paths: when no rules config
// exists anywhere from cwd up to the repository root, loadConfig silently
// falls back to the built-in zero-rule config — allow everything, no error,
// and (before this fix) no trace at all. That degrade is intentional (a
// project that never configured ltk is not gated), but it must not be
// INVISIBLE: an operator relying on the hook being active had no signal that
// it was gating nothing whatsoever. This pins the diagnostic added to the
// Stderr channel (previously produced by nothing at all — U067-F03) without
// changing the decision itself (still Allow).
func TestEvaluateNoConfigFoundAnywhereWarns(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)

	out, err := evaluate("claude-code", "", "",
		strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("an unconfigured project must still allow (fail-open by design), got %+v", out)
	}
	if !strings.Contains(string(out.Stderr), "no rules config found") {
		t.Errorf("a project with no .ltk/config.yaml anywhere must warn that nothing is being gated, got stderr=%q", out.Stderr)
	}
}

// The .git repository boundary in the config search distinguishes gitfile
// pointers from real .git directories: a submodule working tree (gitfile .git)
// must inherit the superproject's rules, while a worktree or submodule with its
// own .ltk keeps using it (the nearest config always wins).
func TestConfigSearchCrossesGitfileBoundaries(t *testing.T) {
	deny := func(message string) string {
		return "version: 1\nrules:\n  - id: no-force-push\n    match: { command: [git, push, --force] }\n    message: \"" + message + "\"\n"
	}
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force"}}`

	t.Run("superproject rules apply inside a submodule", func(t *testing.T) {
		super := t.TempDir()
		sub := filepath.Join(super, "vendored", "lib")
		for _, dir := range []string{filepath.Join(super, ".git"), filepath.Join(super, ".ltk"), sub} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(super, ".ltk", "config.yaml"), []byte(deny("superproject rules")), 0o644); err != nil {
			t.Fatal(err)
		}
		// A submodule working tree carries a gitfile-style .git FILE.
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../../.git/modules/lib\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(sub)

		out, err := evaluate("claude-code", "", "", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), "superproject rules") {
			t.Fatalf("superproject rules must apply inside the submodule, got %+v", out)
		}
	})

	t.Run("worktree with its own .ltk uses it, not an ancestor's", func(t *testing.T) {
		parent := t.TempDir()
		wt := filepath.Join(parent, "wt")
		if err := os.MkdirAll(filepath.Join(wt, ".ltk"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A linked worktree's root also carries a gitfile-style .git FILE.
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/wt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, ".ltk", "config.yaml"), []byte(deny("worktree rules")), 0o644); err != nil {
			t.Fatal(err)
		}
		// A config above the worktree must lose to the worktree's own.
		if err := os.WriteFile(filepath.Join(parent, ".ltk.yaml"), []byte(deny("ancestor rules")), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(wt)

		out, err := evaluate("claude-code", "", "", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), "worktree rules") {
			t.Fatalf("the worktree's own rules must win, got %+v", out)
		}
	})
}

// Override state lives inside a .ltk directory anchored to the resolved
// config — next to it when the config is already in a .ltk dir, otherwise in a
// .ltk/ beside the config file — never anywhere cwd-relative (hook hosts vary
// the cwd, and confirm-by-repeat must find the same state both times).
func TestStatePath(t *testing.T) {
	tests := []struct {
		name, config, want string
	}{
		{"ltk dir layout", filepath.Join(".ltk", "config.yaml"), filepath.Join(".ltk", "state.json")},
		{"absolute ltk dir", filepath.Join("/home/u/proj", ".ltk", "config.yaml"), filepath.Join("/home/u/proj", ".ltk", "state.json")},
		{"legacy flat config", ".ltk.yaml", filepath.Join(".ltk", "state.json")},
		{"absolute legacy flat config", filepath.Join("/home/u/proj", ".ltk.yaml"), filepath.Join("/home/u/proj", ".ltk", "state.json")},
		{"named flat config", "llm-tool-killer.yaml", filepath.Join(".ltk", "state.json")},
		{"nested non-ltk config", filepath.Join(".config", "llm-tool-killer.yaml"), filepath.Join(".config", ".ltk", "state.json")},
		{"relative agy hook config", filepath.Join("..", ".ltk", "config.yaml"), filepath.Join("..", ".ltk", "state.json")},
		{"no config resolved", "", filepath.Join(".ltk", "state.json")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statePath(tt.config); got != tt.want {
				t.Errorf("statePath(%q) = %q, want %q", tt.config, got, tt.want)
			}
		})
	}
}

// confirmByRepeat implements "run it again to permit": the first denial is
// remembered, and an identical re-run within the window is allowed.
func TestConfirmByRepeat(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), ".ltk", "state.json")
	denied := engine.Response{Allow: false, Reason: "use just test"}
	const cmd = "go test ./..."

	// First attempt: still denied, but the reason now tells the agent how to
	// proceed, and the override is armed on disk.
	first := confirmByRepeat(denied, cmd, stateFile, 0, 30*time.Second)
	if first.Allow {
		t.Fatal("first attempt must still be denied")
	}
	if !strings.Contains(first.Reason, "again") {
		t.Errorf("denial reason should explain the repeat override, got %q", first.Reason)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("state file should have been written: %v", err)
	}

	// Second attempt, same command: allowed.
	second := confirmByRepeat(denied, cmd, stateFile, 0, 30*time.Second)
	if !second.Allow {
		t.Error("repeating the exact command within the window must be allowed")
	}

	// The override is single-use: a third attempt re-arms and denies again.
	third := confirmByRepeat(denied, cmd, stateFile, 0, 30*time.Second)
	if third.Allow {
		t.Error("override is single-use; the next attempt must be denied again")
	}
}

// A different command does not consume another command's armed override.
func TestConfirmByRepeatIsPerCommand(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	denied := engine.Response{Allow: false, Reason: "nope"}

	confirmByRepeat(denied, "go test", stateFile, 0, 30*time.Second) // arm "go test"
	other := confirmByRepeat(denied, "git push --force", stateFile, 0, 30*time.Second)
	if other.Allow {
		t.Error("a different command must not be allowed by another's armed override")
	}
}

// A zero/expired window never confirms: every attempt re-arms and denies. (The
// caller only invokes confirmByRepeat when the window is > 0, but the boundary
// of an immediately-elapsed window should still deny.)
func TestConfirmByRepeatTinyWindowDenies(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	denied := engine.Response{Allow: false, Reason: "nope"}

	confirmByRepeat(denied, "go test", stateFile, 0, time.Nanosecond)
	time.Sleep(time.Millisecond)
	again := confirmByRepeat(denied, "go test", stateFile, 0, time.Nanosecond)
	if again.Allow {
		t.Error("an elapsed window must not allow the repeat")
	}
}

// TestEvaluateDeniesUnrecognizedToolName closes stark-boxer: ltk's tool
// matcher is a hand-maintained list over a VENDOR-OWNED, mutating tool set. If
// the vendor ships or renames a shell/file tool, the installed PreToolUse
// matcher can end up firing on it while Decode does not recognise the exact
// tool name (see claudecode.go's claudeMatcher comment for when Claude Code's
// matcher takes its regex path vs. its exact-list path; agy's matcher is
// unconditionally regex). Decode then reads only the payload field names it
// knows (tool_input.command/file_path/notebook_path) — an unrecognized tool
// using different field names (e.g. target_path/new_content) yields an empty
// Request, and an empty Request used to evaluate as an unconditional allow.
// This is the live demonstrated bypass: a tool named "SmartEdit" writing
// tool_input.target_path/new_content sailed straight through with no decision
// at all when piped directly into `ltk evaluate` (evaluate's own robustness —
// not trusting an unrecognised tool's payload shape — matters regardless of
// which path put the payload on its stdin: a live-firing matcher, a
// hand-broadened one, or a direct/manual invocation).
//
// The narrow posture: when the installed matcher fired (proving the tool name
// matched what ltk was told to gate) but the exact name is not recognised,
// deny — rather than the previous warn-and-allow.
func TestEvaluateDeniesUnrecognizedToolName(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := `version: 1
rules:
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "no force pushes"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("unrecognised tool with an unrecognised payload schema is denied", func(t *testing.T) {
		// The exact demonstrated bypass: "SmartEdit" is not in ltk's gated set;
		// its payload uses target_path/new_content, fields Decode does not read.
		payload := `{"tool_name":"SmartEdit","tool_input":{"target_path":"/repo/VERSION","new_content":"9.9.9"}}`
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), `"deny"`) {
			t.Errorf("an unrecognized tool that matched the hook must be denied, got stdout=%q", out.Stdout)
		}
		if !strings.Contains(string(out.Stdout), "SmartEdit") {
			t.Errorf("the deny reason must name the unrecognized tool, got stdout=%q", out.Stdout)
		}
		if out.ExitCode != 0 {
			t.Errorf("claude-code denials exit 0 (both hosts fail OPEN on non-zero), got %d", out.ExitCode)
		}
	})

	t.Run("recognised tool stays silent", func(t *testing.T) {
		payload := `{"tool_name":"Bash","tool_input":{"command":"git status"}}`
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Stdout) != 0 || len(out.Stderr) != 0 {
			t.Errorf("a gated tool must not be affected, got stdout=%q stderr=%q", out.Stdout, out.Stderr)
		}
	})

	t.Run("antigravity denies too", func(t *testing.T) {
		payload := `{"toolCall":{"name":"edit_file_v2","args":{}}}`
		out, err := evaluate("antigravity", cfgPath, "", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), "edit_file_v2") {
			t.Errorf("an ungated agy tool must be named in the deny reason, got stdout=%q", out.Stdout)
		}
		if !strings.Contains(string(out.Stdout), `"deny"`) {
			t.Errorf("agy must also deny, got stdout=%q", out.Stdout)
		}
	})
}

// The 2026-07-24 incident, end to end through the real `ltk evaluate` path: a
// bare redirection (`> /tmp/fsp.txt`) beside a `while` loop crashed the hook
// with "syntax.Walk: unexpected node type <nil>", which BLOCKS a valid command
// — worse than any missed rule. It must now pass through silently.
func TestEvaluateBareRedirectionIsAllowed(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := `version: 1
rules:
  - id: go-test-to-just
    match: { command: [go, test] }
    message: "run tests through the task runner"
    suggest: "just test"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := func(command string) string {
		return `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	}

	allowed := []string{
		"> /tmp/fsp.txt",
		"> /tmp/fsp.txt; while read -r p; do echo \"$p\" >> /tmp/fsp.txt; done < /tmp/list",
		"2>&1",
		"( > /tmp/fsp.txt ) && echo done",
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload(command)))
			if err != nil {
				t.Fatalf("evaluate(%q) errored: %v", command, err)
			}
			// A pass-through is a zero Output. This asserts the DECISION, not
			// merely the absence of a crash.
			if len(out.Stdout) != 0 || len(out.Stderr) != 0 || out.ExitCode != 0 {
				t.Errorf("evaluate(%q) should pass through silently, got %+v", command, out)
			}
		})
	}

	// A rule still fires when a bare redirection sits next to the denied command:
	// the nil-Cmd guard must not swallow the rest of the statement list.
	t.Run("rule still matches beside a bare redirection", func(t *testing.T) {
		out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload("> /tmp/fsp.txt; go test ./...")))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Stdout), `"deny"`) || !strings.Contains(string(out.Stdout), "task runner") {
			t.Errorf("go test after a bare redirection must still be denied, got %s", out.Stdout)
		}
	})
}
