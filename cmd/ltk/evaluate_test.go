package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
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

// A rules file that exists but cannot be parsed must DENY, not error: the hook
// host fails OPEN on a non-zero exit (Claude Code treats exit 1 as
// non-blocking), so a one-character typo in .ltk/config.yaml
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

// The hook path claims to be fail-CLOSED: never an error return, because both
// hosts read a non-zero hook exit as an allow. evaluate and failClosed
// nevertheless still carry `engine.Output{}, err` returns, which read as
// residual fail-open holes. They are not reachable — but "not
// reachable" is a property of the adapters, not of this function, so it is
// pinned here rather than asserted in a comment.
//
// Every arm below is a way the hook path can be driven, including a rule whose
// message is hostile bytes. All must produce a DECISION and a nil error.
func TestEvaluate_HookPathNeverReturnsAPlainError(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "rules.yaml")
	// An invalid UTF-8 byte, a NUL, control characters and a long run — the
	// inputs that would break a fallible encoder if one existed.
	hostile := "bad \xff byte, NUL \x00, ctrl \x01\x02, " + strings.Repeat("x", 4096)
	if err := os.WriteFile(good, []byte("version: 1\nrules:\n  - id: hostile\n    match: { command: [git, push] }\n    message: "+strconv.Quote(hostile)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("version: 1\nrulez:\n  - id: oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const bash = `{"tool_name":"Bash","tool_input":{"command":"git push"}}`

	arms := []struct {
		name       string
		engineName string
		cfgPath    string
		shell      string
		stdin      string
	}{
		{"deny with a hostile rule message", "claude-code", good, "", bash},
		{"allow", "claude-code", good, "", `{"tool_name":"Bash","tool_input":{"command":"git status"}}`},
		{"unknown engine", "no-such-engine", good, "", bash},
		{"unknown engine, unreadable payload", "no-such-engine", good, "", ""},
		{"unknown shell", "claude-code", good, "no-such-shell", bash},
		{"broken config", "claude-code", broken, "", bash},
		{"missing config", "claude-code", filepath.Join(dir, "nope.yaml"), "", bash},
		{"undecodable payload", "claude-code", good, "", "not json"},
		{"ungated tool name", "claude-code", good, "", `{"tool_name":"Unheard0f","tool_input":{"weird":"x"}}`},
	}
	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			out, err := evaluate(a.engineName, a.cfgPath, ir.Shell(a.shell), strings.NewReader(a.stdin))
			if err != nil {
				t.Fatalf("the hook path returned a plain error, which exits 1 and both hosts read as an allow: %v", err)
			}
			if out.ExitCode != 0 {
				t.Errorf("a hook decision must exit 0, got %d", out.ExitCode)
			}
		})
	}

	// The two properties the residual error returns rest on.
	t.Run("the last-resort fallback engine always resolves", func(t *testing.T) {
		if _, err := engine.Get("claude-code"); err != nil {
			t.Fatalf("evaluate's unknown-engine fallback has nothing to deny with: %v", err)
		}
	})

	// Measured: the end-to-end arms above cannot actually deliver invalid
	// UTF-8 to an encoder, because the YAML decoder sanitises a rule message
	// on the way in. So this subtest — not the hostile-message arm — is the
	// one carrying the totality invariant, and it is the one that goes red
	// when an adapter grows a fallible encoder.
	t.Run("every adapter's Encode is total", func(t *testing.T) {
		for _, e := range engine.All() {
			a, ok := e.(engine.Adapter)
			if !ok {
				t.Fatalf("%T does not implement engine.Adapter", e)
			}
			for _, resp := range []engine.Response{
				{Allow: true},
				{Reason: hostile, Suggest: hostile},
				{},
			} {
				if _, err := a.Encode(resp); err != nil {
					t.Fatalf("%s.Encode failed, so the hook path is no longer fail-closed: %v", a.Name(), err)
				}
			}
		}
	})
}

// failingWriter fails every write, standing in for a closed or broken stream.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The two stream writes are deliberately asymmetric. stdout carries the
// DECISION: if it cannot be delivered the hook host never sees the deny, so
// the failure must surface. stderr carries only a diagnostic, and the sole
// channel on which a failed stderr write could be reported is stderr itself —
// promoting it to an error would turn a lost diagnostic into a non-zero exit,
// which both hook hosts read as an allow. A broken diagnostic pipe must never
// be able to disable the guard.
func TestEmitDecision_StreamFailuresAreAsymmetric(t *testing.T) {
	t.Run("a lost diagnostic never becomes an error exit", func(t *testing.T) {
		out := engine.Output{Stderr: []byte("ltk: no rules config found\n")}
		if err := emitDecision(out, &strings.Builder{}, failingWriter{}); err != nil {
			t.Fatalf("a failed diagnostic write must not surface as an error (exit 1 fails open): %v", err)
		}
	})

	t.Run("an undeliverable decision does surface", func(t *testing.T) {
		out := engine.Output{Stdout: []byte(`{"decision":"deny"}`)}
		if err := emitDecision(out, failingWriter{}, &strings.Builder{}); err == nil {
			t.Fatal("a decision that could not be written must not be reported as delivered")
		}
	})

	t.Run("a healthy decision reaches both streams", func(t *testing.T) {
		var stdout, stderr strings.Builder
		out := engine.Output{Stdout: []byte("D"), Stderr: []byte("E")}
		if err := emitDecision(out, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != "D" || stderr.String() != "E" {
			t.Fatalf("streams = %q / %q", stdout.String(), stderr.String())
		}
	})
}

// A working directory that cannot be determined is not "this repo has no
// submodules": it means the `@submodules` sentinel is never expanded, so a
// rule written to block every submodule keeps the literal token in its path
// list, matches no real path, and silently guards nothing. Both surfaces must
// report it — evaluate by denying (the hook fails closed), check by erroring
// (the diagnostic surface fails loud) — never by returning a clean allow over
// an expansion that never happened.
func TestExpandSubmodules_UnknownWorkingDirectoryIsReported(t *testing.T) {
	cfg, err := rules.Parse([]byte("version: 1\nrules:\n  - id: no-submodule-edits\n    match: { path: [\"@submodules\"] }\n    message: \"don't edit submodules\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	broken := func() (string, error) { return "", errors.New("getwd: no such file or directory") }

	if err := expandSubmodules(cfg, broken); err == nil {
		t.Fatal("an unresolvable working directory was reported as a successful @submodules expansion")
	}
	// The sentinel is still sitting unexpanded in the rule — which is exactly
	// why silence here is a guard that gates nothing.
	if got := cfg.Rules[0].Match.Path; len(got) != 1 || got[0] != "@submodules" {
		t.Fatalf("expected the sentinel left unexpanded, got %v", got)
	}
}

// The hook surface turns that failure into a deny, not an allow.
func TestEvaluateFailsClosedOnUnresolvableSubmodules(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "rules.yaml")
	// .gitmodules present but unreadable as a file (it is a directory), which
	// scm.SubmodulePaths reports rather than treating as "no submodules".
	if err := os.WriteFile(cfgPath, []byte("version: 1\nrules:\n  - id: no-submodule-edits\n    match: { path: [\"@submodules\"] }\n    message: \"don't edit submodules\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".gitmodules"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := evaluate("claude-code", cfgPath, "",
		strings.NewReader(`{"tool_name":"Edit","tool_input":{"file_path":"vendor/x.go"}}`))
	if err != nil {
		t.Fatalf("an unresolvable sentinel must yield a decision, not an error (exit 1 fails open): %v", err)
	}
	if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `"deny"`) {
		t.Fatalf("want a deny decision with exit 0, got %+v", out)
	}
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

// TestEvaluateUnknownEngineDetectsRealHostFromPayload and
// TestEvaluateFindsConfigFromHookCwd used to pin two-host (Claude Code +
// Antigravity) behavior — the unknown-engine fail-closed branch picking the
// wire format that actually decoded the payload (rather than blindly
// guessing claude-code), and the default-config search walking up from
// Antigravity's `.agents` hook cwd to find project rules. Both were deleted
// with antigravity in 0.7.0: ltk now has exactly one registered host
// (engine.engines() == []Engine{ClaudeCode{}}), so there is no second wire
// format for the first test to disambiguate and no second hook cwd for the
// second to search from. denyUnknownEngine's every-registered-engine loop and
// loadConfig's ancestor walk both remain general-purpose (see their own
// docs) for whichever engine is added next; this is not a design reversion,
// only the loss of the second data point that made these two tests
// meaningful.

// TestEvaluateNoConfigFoundAnywhereWarns is a DEFECT WITH NO CENSUS ROW found
// while walking the guardrail flow's fail-open paths: when no rules config
// exists anywhere from cwd up to the repository root, loadConfig silently
// falls back to the built-in zero-rule config — allow everything, no error,
// and (before this fix) no trace at all. That degrade is intentional (a
// project that never configured ltk is not gated), but it must not be
// INVISIBLE: an operator relying on the hook being active had no signal that
// it was gating nothing whatsoever. This pins the diagnostic added to the
// Stderr channel (previously produced by nothing at all) without
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

// TestEvaluateDeniesUnrecognizedToolName closes a real bypass: ltk's tool
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
