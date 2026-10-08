package exectoken

import (
	"testing"
)

func TestExecToken(t *testing.T) {
	cases := map[string]string{
		"ctxloom hook hud":                       "ctxloom",
		`"/usr/bin/ctxloom" hook hud`:            "ctxloom",
		"/home/me/go/bin/ctxloom session bind":   "ctxloom",
		`"C:\Tools\ctxloom.exe" hook stamp-plan`: "ctxloom",
		`'/Apps/My Tools/ctxloom' mcp`:           "ctxloom",
		"ltk evaluate --config x":                "ltk",
		"/usr/local/bin/ctxloomctl whatever":     "ctxloomctl",
		"":                                       "",
	}
	for in, want := range cases {
		if got := Token(in); got != want {
			t.Errorf("Token(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestIsManaged_AbsolutePathCommand pins the matcher against a command shape
// CtxloomCommand no longer emits but that still exists on disk: an
// absolute-path ctxloom invocation, written either by an older ctxloom or by
// hand. The removal/reconcile matcher must recognize those as managed — else
// uninstall leaves them behind and a re-apply adds a bare-name duplicate
// beside each one. Both an MCP Command field (bare absolute path, no args)
// and a shell hook command (absolute path prefix, unquoted) are covered.
func TestIsManaged_AbsolutePathCommand(t *testing.T) {
	for _, c := range []string{
		"/usr/local/bin/ctxloom",
		"/home/me/go/bin/ctxloom hook session-start",
		`"/Apps/My Tools/ctxloom" hook hud`,
	} {
		if !IsManaged(c, "ctxloom") {
			t.Errorf("an absolute-path command must still be recognized as managed: %q", c)
		}
	}
}

func TestIsManaged(t *testing.T) {
	for _, c := range []string{"ctxloom hook hud", `"/usr/bin/ctxloom" mcp`, "ctxloom session bind"} {
		if !IsManaged(c, "ctxloom") {
			t.Errorf("ctxloom should manage %q", c)
		}
	}
	for _, c := range []string{"ltk evaluate", "npx -y some-mcp", "/usr/local/bin/ctxloomctl x", ""} {
		if IsManaged(c, "ctxloom") {
			t.Errorf("ctxloom must not manage %q", c)
		}
	}
	if IsManaged("ctxloom x", "") {
		t.Error("an empty bin manages nothing")
	}
	// Cross-tool: each bin manages only its own namespace.
	if !IsManaged("ltk evaluate --config x", "ltk") {
		t.Error("ltk should manage its own command")
	}
	if IsManaged("ctxloom hook hud", "ltk") {
		t.Error("ltk must not manage ctxloom's command")
	}
}

func TestArgs(t *testing.T) {
	cases := map[string][]string{
		"ctxloom hook inject-context abc":            {"hook", "inject-context", "abc"},
		`"/opt/my tools/ctxloom" hook session-start`: {"hook", "session-start"},
		`'/a b/ctxloom'   hook  hud`:                 {"hook", "hud"},
		"/usr/bin/ctxloom":                           nil,
		`"/unterminated ctxloom hook`:                nil,
		"":                                           nil,
	}
	for in, want := range cases {
		got := Args(in)
		if len(got) != len(want) {
			t.Errorf("Args(%q) = %q; want %q", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Args(%q) = %q; want %q", in, got, want)
			}
		}
	}
}
