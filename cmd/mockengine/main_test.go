package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// TestRun_PersonalityFromEnv pins U006-F03. envPersonality
// (MOCKENGINE_PERSONALITY) is documented as "the clean channel when the mock
// is installed via a config `env:` block and the driver owns the argv" — but
// before this test, nothing exercised it: every real invocation, including
// the docker integration test, selects the personality via argv
// (--claude/--personality). The main/run split (main.go:32) exists exactly so
// this channel can be driven as a pure []string -> int function with a
// stubbed environment; this was simply never done.
//
// Drives run with EMPTY argv and MOCKENGINE_PERSONALITY set to an unknown
// backend name. If the env var were not being read at all, personality would
// stay "" and the mock would report "no personality selected" instead — so
// asserting the message names the offending env value proves the env
// channel, not the flag path, put it there.
func TestRun_PersonalityFromEnv(t *testing.T) {
	t.Setenv(envPersonality, "not-a-real-backend")

	var code int
	stderr := captureStderr(t, func() {
		code = run(nil)
	})

	if code != 2 {
		t.Fatalf("run(nil) = %d, want 2 (unknown backend)", code)
	}
	if !strings.Contains(stderr, "not-a-real-backend") {
		t.Fatalf("stderr = %q, want it to name the personality read from %s (proving the env channel, not just the missing-personality path, fired)",
			stderr, envPersonality)
	}
}

// TestRun_NoPersonality_NamesTheEnvVar pins the companion path: with NO flag
// and NO env var, the "no personality selected" message must itself name
// envPersonality, so a user installing the mock via a config env: block knows
// which variable to set.
func TestRun_NoPersonality_NamesTheEnvVar(t *testing.T) {
	t.Setenv(envPersonality, "")

	var code int
	stderr := captureStderr(t, func() {
		code = run(nil)
	})

	if code != 2 {
		t.Fatalf("run(nil) = %d, want 2 (no personality)", code)
	}
	if !strings.Contains(stderr, envPersonality) {
		t.Fatalf("stderr = %q, want it to name %s", stderr, envPersonality)
	}
}

// The mock's leading flags carried their own engine-name table: --claude and
// --claude-code mapped to the literal "claude-code", --codex to "codex". The
// repo already has exactly one such table — agent.CanonicalEngineName, whose
// own doc says engine names are shared user-facing vocabulary and that "two
// tables drift into one spelling resolving under one binary and erroring under
// the other". That drift was live: --claude selected claude-code, while
// --personality claude (and MOCKENGINE_PERSONALITY=claude) hit
// backends.Get's exact-match lookup and died with "declares no engine CLI to
// impersonate", for the same spelling the shared table declares.
func TestRun_PersonalitySpellingsAgreeAcrossChannels(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{"leading alias flag", "", []string{"--claude", "--print"}},
		{"leading canonical flag", "", []string{"--claude-code", "--print"}},
		{"--personality with an alias", "", []string{"--personality", "claude", "--print"}},
		{"--personality canonical", "", []string{"--personality", "claude-code", "--print"}},
		{"env var with an alias", "claude", []string{"--print"}},
		{"env var canonical", "claude-code", []string{"--print"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envPersonality, tc.env)

			var code int
			stderr := captureStderr(t, func() { code = run(tc.args) })
			if code != 0 {
				t.Fatalf("run(%v) = %d, want 0 — every channel must accept the spellings the shared alias table declares; stderr: %s",
					tc.args, code, stderr)
			}
		})
	}
}

// Selectability by a leading --<engine> flag must come from the registry, not
// from a switch in this file: a third impersonable backend should not need
// this main package edited to be reachable the way claude-code and codex are.
func TestPersonalityFromFlag_CoversEveryImpersonableBackend(t *testing.T) {
	var checked int
	for _, name := range backends.List() {
		if _, ok := backends.EngineCLIsFor(name); !ok {
			continue // ACP-only backend: nothing for the mock to impersonate
		}
		got, ok := personalityFromFlag("--" + name)
		if !ok || got != name {
			t.Errorf("--%s does not select the %s personality (got %q, ok=%v)", name, name, got, ok)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no impersonable backend was checked: this test validated nothing")
	}

	// A vendor flag must still terminate the mock's own flag run rather than
	// being swallowed as a personality.
	if _, ok := personalityFromFlag("--print"); ok {
		t.Error("a vendor flag was consumed as a personality selector")
	}
	if _, ok := personalityFromFlag("not-a-flag"); ok {
		t.Error("a bare token was consumed as a personality selector")
	}
}

// The two discovery ROOTS the mock reports against — Resolver.Cwd and
// Resolver.Home — had their errors discarded. The zero value that results does
// not disable a probe: filepath.Join("", rel) yields rel, so every HOME-scoped
// probe silently re-roots onto the process working directory and the mock
// reports the PROJECT's .claude/CLAUDE.md as the file it found in $HOME, with
// rec.Root recorded as "". An unset HOME is the ordinary case here, not an
// exotic one: this binary is installed under a vendor's name inside container
// fixtures, and ctxloom rewrites HOME deliberately to isolate engines.
//
// A fake whose entire product is a faithful observation report must refuse
// rather than answer a different question.
func TestRun_UnresolvableHomeIsRefusedNotSubstituted(t *testing.T) {
	t.Setenv("HOME", "") // os.UserHomeDir: "$HOME is not defined"

	var code int
	stderr := captureStderr(t, func() {
		code = run([]string{"--claude", "--print"})
	})

	if code != 2 {
		t.Fatalf("run = %d, want 2: an unresolvable home root must be refused, not substituted with \"\"", code)
	}
	if !strings.Contains(stderr, "home directory") {
		t.Fatalf("stderr = %q, want it to say which discovery root could not be resolved", stderr)
	}
}

// captureStderr redirects the process-global os.Stderr for the duration of
// fn and returns everything written to it. run writes via fmt.Fprintf(os.
// Stderr, ...), reading the global at call time, so this substitution is
// visible to it without any signature change.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return buf.String()
}
