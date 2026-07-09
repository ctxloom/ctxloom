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
