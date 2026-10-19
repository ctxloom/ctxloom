package testenv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxedCommand is a host command under a throwaway sandbox, for a test
// that needs a real child and no TestEnvironment of its own.
func sandboxedCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	return Sandbox(t.TempDir()).HostCommand(context.Background(), name, args...)
}

// realHome is the home the gate guards, or a skip on an account that has none.
func realHome(t *testing.T) string {
	t.Helper()
	home := guardedHome()
	if home == "" {
		t.Skip("this account has no home to guard (no passwd home, $HOME unset or /)")
	}
	return home
}

// The gate refuses every way a child's environment can lead back to the real
// home — the variables engines and ctxloom resolve their state from, and any
// other name too, because the refusal is by value, not by a list of names.
func TestAssertChildEnvSandboxed_RefusesARealHomePointer(t *testing.T) {
	home := realHome(t)
	sandbox := t.TempDir()
	base := []string{"PATH=/usr/bin:/bin", "HOME=" + sandbox}
	for name, bad := range map[string]string{
		"HOME is the real home":                      "HOME=" + home,
		"CLAUDE_CONFIG_DIR under the real home":      "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		"an XDG directory under the real home":       "XDG_STATE_HOME=" + filepath.Join(home, "x"),
		"a name nothing knows, under the real home":  "SOME_UNHEARD_OF_VAR=" + filepath.Join(home, "y"),
		"one entry of a list":                        "SOME_SEARCH_PATH=/usr/share:" + filepath.Join(home, "z"),
		"embedded in a URL":                          "SOME_URL=file://" + filepath.Join(home, "repo"),
		"embedded in a command line":                 "GIT_SSH_COMMAND=ssh -i " + filepath.Join(home, ".ssh", "id"),
		"the real home even when HOME is sandboxed":  "OTHER_HOME=" + home,
		"a duplicate HOME after the sandboxed value": "HOME=" + home,
	} {
		t.Run(name, func(t *testing.T) {
			key, _, _ := strings.Cut(bad, "=")
			err := AssertChildEnvSandboxed(append(append([]string{}, base...), bad))
			if err == nil {
				t.Fatalf("AssertChildEnvSandboxed accepted %s pointing into the real home", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("the refusal %q does not name %s", err, key)
			}
		})
	}
}

// A nil Env is not an empty one: exec hands such a child this process's whole
// environment, so it is refused whatever that environment holds.
func TestAssertChildEnvSandboxed_RefusesANilEnv(t *testing.T) {
	if err := AssertChildEnvSandboxed(nil); !errors.Is(err, errNilChildEnv) {
		t.Fatalf("AssertChildEnvSandboxed(nil) = %v, want errNilChildEnv", err)
	}
}

// The refusal names the variable and never its value, which may be a
// credential that happens to sit in a path.
func TestAssertChildEnvSandboxed_NamesTheVariableNotItsValue(t *testing.T) {
	home := realHome(t)
	value := filepath.Join(home, "value-that-must-not-be-printed")
	err := AssertChildEnvSandboxed([]string{"LEAKY=" + value})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if strings.Contains(err.Error(), "value-that-must-not-be-printed") {
		t.Errorf("the refusal printed the value: %q", err)
	}
}

// A fully sandboxed environment passes, including the exempt toolchain
// variables pointing into the real home and paths that merely share its
// prefix.
func TestAssertChildEnvSandboxed_AcceptsASandboxedEnv(t *testing.T) {
	home := realHome(t)
	sandbox := t.TempDir()
	env := []string{
		"HOME=" + sandbox,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(sandbox, ".claude"),
		"XDG_CONFIG_HOME=" + filepath.Join(sandbox, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(sandbox, ".local", "state"),
		"PATH=" + filepath.Join(home, ".local", "bin") + ":/usr/bin",
		"GOPATH=" + filepath.Join(home, "go"),
		"GOMODCACHE=" + filepath.Join(home, "go", "pkg", "mod"),
		"GOCACHE=" + filepath.Join(home, ".cache", "go-build"),
		"GOENV=" + filepath.Join(home, ".config", "go", "env"),
		"GOTMPDIR=" + filepath.Join(home, ".cache", "gotmp"),
		"SIBLING=" + home + "2/x",
		"NESTED_ELSEWHERE=/srv" + home,
		"TERM=dumb",
		"EMPTY=",
	}
	if err := AssertChildEnvSandboxed(env); err != nil {
		t.Fatalf("a sandboxed environment was refused: %v", err)
	}
	if err := AssertChildEnvSandboxed([]string{}); err != nil {
		t.Fatalf("an empty, non-nil environment was refused: %v", err)
	}
}

// What the sandbox builders hand a child passes the gate on THIS machine,
// whatever its ambient environment points at (the PWD of a checkout under the
// home, at the least): the builder drops what the gate would refuse.
func TestSandboxBuilders_PassTheGate(t *testing.T) {
	realHome(t)
	sb := Sandbox(t.TempDir())
	for name, env := range map[string][]string{
		"HostCommand":      sb.HostCommand(context.Background(), "true").Env,
		"GitCommand":       sb.GitCommand(t.TempDir(), "status").Env,
		"GoCombinedOutput": goCommandEnv([]string{"CGO_ENABLED=0"}),
	} {
		if err := AssertChildEnvSandboxed(env); err != nil {
			t.Errorf("%s built an environment the gate refuses: %v", name, err)
		}
	}
}

func TestNamesHome(t *testing.T) {
	const home = "/home/u"
	for value, want := range map[string]bool{
		"/home/u":               true,
		"/home/u/":              true,
		"/home/u/.claude":       true,
		"/usr/bin:/home/u/bin":  true,
		"file:///home/u/repo":   true,
		"ssh -i /home/u/.ssh/k": true,
		`{"cwd":"/home/u"}`:     true,
		"/home/u2":              false,
		"/home/u.bak":           false,
		"/home/user":            false,
		"/srv/home/u":           false,
		"":                      false,
		"u":                     false,
	} {
		if got := namesHome(value, home); got != want {
			t.Errorf("namesHome(%q, %q) = %v, want %v", value, home, got, want)
		}
	}
}

// startRefusalCases are every entry to the gate a caller has: the exported
// Start family and the internal routes that start this package's own
// children.
func startRefusalCases(e *TestEnvironment) map[string]func(*exec.Cmd) error {
	return map[string]func(*exec.Cmd) error{
		"Start":          e.Start,
		"RunCmd":         e.RunCmd,
		"Output":         func(c *exec.Cmd) error { _, err := e.Output(c); return err },
		"CombinedOutput": func(c *exec.Cmd) error { _, err := e.CombinedOutput(c); return err },
		"RunHistory.Exec": func(c *exec.Cmd) error {
			var h RunHistory
			return h.Exec(c)
		},
	}
}

// A refused child never runs: not started late, not started and killed —
// never started. The child would touch a marker file; after the refusal the
// marker is absent. The same command with a sandboxed environment does touch
// it, so the absence is the refusal's doing and not a child that could not
// have run anyway.
func TestStart_ARefusedChildNeverRuns(t *testing.T) {
	home := realHome(t)
	e := Sandbox(t.TempDir())
	for name, start := range startRefusalCases(e) {
		t.Run(name, func(t *testing.T) {
			for bad, mutate := range map[string]func(*exec.Cmd){
				"real HOME": func(c *exec.Cmd) { c.Env = append(c.Env, "HOME="+home) },
				"nil Env":   func(c *exec.Cmd) { c.Env = nil },
			} {
				marker := filepath.Join(t.TempDir(), "ran")
				cmd := e.HostCommand(context.Background(), "sh", "-c", `touch "$MARKER"`)
				cmd.Env = append(cmd.Env, "MARKER="+marker)
				mutate(cmd)
				if bad == "nil Env" {
					// The marker path must still reach the child were it to
					// run, or its absence would prove nothing.
					t.Setenv("MARKER", marker)
				}

				if err := start(cmd); err == nil {
					t.Fatalf("%s: a child with %s was started", name, bad)
				}
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
					t.Fatalf("%s: a refused child (%s) has a process", name, bad)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s: a refused child (%s) ran: marker stat = %v", name, bad, err)
				}
			}

			marker := filepath.Join(t.TempDir(), "ran")
			cmd := e.HostCommand(context.Background(), "sh", "-c", `touch "$MARKER"`)
			cmd.Env = append(cmd.Env, "MARKER="+marker)
			if err := start(cmd); err != nil {
				t.Fatalf("%s refused a sandboxed child: %v", name, err)
			}
			if name == "Start" {
				if err := cmd.Wait(); err != nil {
					t.Fatalf("sandboxed child: %v", err)
				}
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("%s: the sandboxed control child did not run, so the refusals above prove nothing: %v", name, err)
			}
		})
	}
}

// The pty path builds its own command type, so it asserts the gate itself.
func TestStartPTY_RefusesAnUnsandboxedEnv(t *testing.T) {
	home := realHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := startPTY(ptyHooks{}, t.TempDir(), []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "MARKER=" + marker},
		"/bin/sh", 80, 24, "-c", `touch "$MARKER"`)
	if err == nil {
		t.Fatal("startPTY started a child whose HOME is the real home")
	}
	if _, serr := os.Stat(marker); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("the refused pty child ran: marker stat = %v", serr)
	}
}

// The go tool runs behind the gate with no HOME at all: the toolchain's own
// locations are resolved in-process and handed over explicitly.
func TestGoCombinedOutput_RunsTheToolchainBehindTheGate(t *testing.T) {
	out, err := GoCombinedOutput(nil, "env", "GOMODCACHE")
	if err != nil {
		t.Fatalf("go env through the gate: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got == "" || !filepath.IsAbs(got) {
		t.Fatalf("go resolved no module cache without HOME: %q", got)
	}
}
