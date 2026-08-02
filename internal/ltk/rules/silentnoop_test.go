package rules

import (
	"strings"
	"testing"
)

// TestDenyRuleNeedsMessageOrSuggest pins that a deny rule with neither
// `message` nor `suggest` validates, fires, and hands the model a bare "deny"
// with no explanation — `permissionDecisionReason` is absent from the hook
// response entirely, so the agent cannot tell the user why or what to do
// instead. A denial that cannot say why is a guard that silently does nothing
// useful, so the config must be rejected at parse time.
func TestDenyRuleNeedsMessageOrSuggest(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name:    "deny with no message and no suggest",
			yaml:    "version: 1\nrules:\n  - id: bare\n    match: { command: [rm] }\n",
			wantErr: true,
		},
		{
			name:    "explicit deny with no message and no suggest",
			yaml:    "version: 1\nrules:\n  - id: bare\n    action: deny\n    match: { command: [rm] }\n",
			wantErr: true,
		},
		{
			name:    "whitespace-only message is no message",
			yaml:    "version: 1\nrules:\n  - id: bare\n    match: { command: [rm] }\n    message: \"   \"\n",
			wantErr: true,
		},
		{
			name: "suggest alone is enough — it renders as \"Use instead: …\"",
			yaml: "version: 1\nrules:\n  - id: ok\n    match: { command: [rm] }\n    suggest: \"trash\"\n",
		},
		{
			name: "message alone is enough",
			yaml: "version: 1\nrules:\n  - id: ok\n    match: { command: [rm] }\n    message: \"no\"\n",
		},
		{
			name: "an allow rule explains nothing by design",
			yaml: "version: 1\nrules:\n  - id: ok\n    action: allow\n    match: { command: [ls] }\n",
		},
		{
			name: "a disabled rule never fires, so it owes no explanation",
			yaml: "version: 1\nrules:\n  - id: ok\n    mode: disable\n    match: { command: [rm] }\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			switch {
			case tc.wantErr && err == nil:
				t.Error("a deny rule with no message and no suggest must be a config error")
			case tc.wantErr && !strings.Contains(err.Error(), "message"):
				t.Errorf("the error must name what is missing, got %v", err)
			case !tc.wantErr && err != nil:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestExpandSubmodules_RejectsInvalidInjectedPattern pins that
// ExpandSubmodules injects raw .gitmodules strings into Match.Path AFTER Parse
// validated that list, and globMatch swallows the resulting error — so a
// submodule whose path carries a glob metacharacter (`a[`) expands into a
// pattern that can never match and the submodule is silently unprotected by a
// rule written precisely to protect it.
func TestExpandSubmodules_RejectsInvalidInjectedPattern(t *testing.T) {
	cfg := cfgWith(Rule{
		ID:      "no-submodule-edits",
		Match:   Match{Path: []string{"@submodules"}},
		Message: "submodules are pinned",
	})
	err := cfg.ExpandSubmodules([]string{"libs/foo", "weird[/dir"})
	if err == nil {
		t.Fatal("a submodule path that is not a valid glob must be a loud error, not a dead pattern")
	}
	if !strings.Contains(err.Error(), "weird[/dir") {
		t.Errorf("the error must name the offending submodule path, got %v", err)
	}
}

// The ordinary expansion still succeeds and still returns no error.
func TestExpandSubmodules_ValidPathsSucceed(t *testing.T) {
	cfg := cfgWith(Rule{
		ID:      "no-submodule-edits",
		Match:   Match{Path: []string{".gitmodules", "@submodules"}},
		Message: "submodules are pinned",
	})
	if err := cfg.ExpandSubmodules([]string{"libs/foo", "third_party/bar/"}); err != nil {
		t.Fatalf("valid submodule paths must expand cleanly: %v", err)
	}
	if EvaluatePath(cfg, "libs/foo/x.c").Allowed {
		t.Error("editing inside an expanded submodule should be denied")
	}
}
