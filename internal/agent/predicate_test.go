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

func TestOwner_Owns(t *testing.T) {
	o := Owner{Bin: "ctxloom"}
	for _, c := range []string{"ctxloom hook hud", `"/usr/bin/ctxloom" mcp`, "ctxloom session bind"} {
		if !o.Owns(c) {
			t.Errorf("ctxloom should own %q", c)
		}
	}
	for _, c := range []string{"ltk evaluate", "npx -y some-mcp", "/usr/local/bin/ctxloomctl x", ""} {
		if o.Owns(c) {
			t.Errorf("ctxloom must not own %q", c)
		}
	}
	if (Owner{}).Owns("ctxloom x") {
		t.Error("an empty Bin owns nothing")
	}
}
