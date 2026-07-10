package agent

import "testing"

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
		if got := execToken(in); got != want {
			t.Errorf("execToken(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestIsManaged_AbsolutePathCommand pins the invariant the self-exec fix
// (CtxloomCommand) depends on: once materialized commands name an absolute
// path instead of the bare "ctxloom", the removal/reconcile matcher must
// still recognize them as managed — else re-apply orphans every surface a
// staged/installed binary previously wrote. Both an MCP Command field
// (bare absolute path, no args) and a shell hook command (absolute path
// prefix, unquoted) are covered.
func TestIsManaged_AbsolutePathCommand(t *testing.T) {
	for _, c := range []string{
		"/usr/local/bin/ctxloom",
		"/home/me/go/bin/ctxloom hook inject-context --project /work abc123",
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
