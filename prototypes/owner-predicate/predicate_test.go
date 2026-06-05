package harness

import "testing"

func grp(matcher string, cmds ...string) HookGroup {
	g := HookGroup{Matcher: matcher}
	for _, c := range cmds {
		g.Hooks = append(g.Hooks, HookEntry{Type: "command", Command: c})
	}
	return g
}

func count(s *Settings, cmd string) int {
	n := 0
	for _, c := range Commands(s) {
		if c == cmd {
			n++
		}
	}
	return n
}

func has(s *Settings, cmd string) bool { return count(s, cmd) > 0 }

func TestExecToken(t *testing.T) {
	cases := map[string]string{
		"ctxloom hook hud":                          "ctxloom",
		`"/usr/bin/ctxloom" hook hud`:               "ctxloom",
		"/home/me/go/bin/ctxloom session bind":      "ctxloom",
		`"C:\Tools\ctxloom.exe" hook stamp-plan`:    "ctxloom",
		`'/Apps/My Tools/ctxloom' mcp`:              "ctxloom",
		"ltk evaluate --config .ctxloom/ltk/c.yaml": "ltk",
		"my-linter --fix":                           "my-linter",
		"":                                          "",
	}
	for in, want := range cases {
		if got := execToken(in); got != want {
			t.Errorf("execToken(%q) = %q; want %q", in, got, want)
		}
	}
}

// Executable-token identity migrates a verb-drifted callback forward.
func TestReconcile_MigratesVerbDrift(t *testing.T) {
	s := &Settings{Hooks: map[string][]HookGroup{
		"SessionStart": {grp("", "ctxloom session bind")}, // legacy verb
	}}
	Reconcile(s, Owner{Bin: "ctxloom"}, map[string][]HookGroup{
		"SessionStart": {grp("", "ctxloom hook session-bind")},
	})

	if has(s, "ctxloom session bind") {
		t.Error("legacy verb form should have been migrated, not orphaned")
	}
	if !has(s, "ctxloom hook session-bind") {
		t.Error("current command should be present")
	}
}

// Re-running converges to a single entry — no bookkeeping needed.
func TestReconcile_Idempotent(t *testing.T) {
	s := &Settings{}
	o := Owner{Bin: "ctxloom"}
	desired := map[string][]HookGroup{"SessionStart": {grp("", "ctxloom hook session-bind")}}

	Reconcile(s, o, desired)
	Reconcile(s, o, desired)

	if c := count(s, "ctxloom hook session-bind"); c != 1 {
		t.Errorf("expected exactly one entry after two runs, got %d", c)
	}
}

// Cross-tool isolation: ctxloom's reconcile leaves ltk's and the user's hooks
// untouched (a tool only owns its own executable namespace).
func TestReconcile_LeavesOtherToolsAndUser(t *testing.T) {
	s := &Settings{Hooks: map[string][]HookGroup{
		"SessionStart": {grp("", "ctxloom session bind")},
		"PreToolUse":   {grp("Bash", "ltk evaluate --config .ctxloom/ltk/config.yaml")},
		"PostToolUse":  {grp("Edit|Write", "my-linter --fix")},
	}}
	Reconcile(s, Owner{Bin: "ctxloom"}, map[string][]HookGroup{
		"SessionStart": {grp("", "ctxloom hook session-bind")},
	})

	for _, cmd := range []string{
		"ctxloom hook session-bind",
		"ltk evaluate --config .ctxloom/ltk/config.yaml",
		"my-linter --fix",
	} {
		if !has(s, cmd) {
			t.Errorf("expected %q to be present", cmd)
		}
	}
	if has(s, "ctxloom session bind") {
		t.Error("ctxloom's own legacy entry should have been replaced")
	}
}

// No false positives: a tool never touches a hook it does not own.
func TestReconcile_DoesNotTouchUnowned(t *testing.T) {
	s := &Settings{Hooks: map[string][]HookGroup{
		"PreToolUse": {grp("", "npx -y some-mcp")},
	}}
	Reconcile(s, Owner{Bin: "ctxloom"}, map[string][]HookGroup{})

	if !has(s, "npx -y some-mcp") {
		t.Error("ctxloom must not remove a hook it has no claim to")
	}
}
