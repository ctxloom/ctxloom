package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain_AHookRunThatLogsNothingWritesNothing pins the laziness of ltk's
// log. ltk is a pre-tool hook: it runs before EVERY shell command an agent
// issues, and nearly all of those runs have nothing to log. Its main installs a
// logger over ~/.ctxloom/logs/ltk.log so a stalled lock wait leaves a durable
// record, but a logger that opened its file at install time would create the
// directory and the file on every one of those runs — a write on the hook path
// for no record at all. So an ordinary evaluate, allowed or denied, must leave
// HOME exactly as it found it: not the log, not ~/.ctxloom/logs, not
// ~/.ctxloom.
//
// The child runs ltk's real main (TestMain's exitPinArgvEnv branch), so a main
// that installs its logger eagerly fails this, not only a Lazy that stopped
// being lazy.
func TestMain_AHookRunThatLogsNothingWritesNothing(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(cfg, []byte("version: 1\nrules:\n  - id: no-push\n    match: { command: [git, push] }\n    message: \"no pushing\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary to re-exec: %v", err)
	}

	for _, arm := range []struct{ name, command, wantStdout string }{
		{"allowed", "git status", ""},
		{"denied", "git push", "no pushing"},
	} {
		t.Run(arm.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(exe)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), exitPinArgvEnv+"=evaluate --config "+cfg, "HOME="+home)
			cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"` + arm.command + `"}}`)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ltk evaluate: %v\nstderr: %s", err, stderr.String())
			}
			// Guards against a vacuous pass: a run that died before evaluating
			// would also leave HOME untouched.
			if arm.wantStdout == "" && stdout.Len() != 0 || !strings.Contains(stdout.String(), arm.wantStdout) {
				t.Fatalf("evaluate did not reach its decision: stdout %q, want it to contain %q", stdout.String(), arm.wantStdout)
			}

			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				t.Errorf("a hook run that logged nothing left %s in HOME", filepath.Join(home, e.Name()))
			}
		})
	}
}
