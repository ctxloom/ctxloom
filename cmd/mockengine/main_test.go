package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
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
