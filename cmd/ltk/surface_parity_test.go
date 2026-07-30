package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// `evaluate` (the hook) and `check` (the query surface) run the SAME four-step
// wiring over the same rules: load the config, expand the @submodules
// sentinel, build the rules app, decide. U005-F10 called that duplication and
// noted it "has already drifted". The wiring is not verbatim — the two
// surfaces deliberately differ in ERROR POLICY, one failing closed and the
// other failing loud — so a parity test over the failure arms would be pinning
// a difference that is the point.
//
// What must NOT differ is the answer either surface gives about the same
// command under the same rules, and what either surface DISCLOSES about how
// much checking actually happened. That is where the drift was: evaluate says
// "no rules config found ... nothing is being gated" when the search came up
// empty, and check said nothing at all, so a tool asking "would this be
// blocked?" could not tell "checked your rules, nothing matched" from
// "there are no rules anywhere".
func TestSurfaceParity_CheckAndEvaluateAgree(t *testing.T) {
	payload := func(command string) string {
		return `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	}

	withRules := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(withRules, []byte(`version: 1
rules:
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "no force pushes"
    suggest: "git push --force-with-lease"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		cfgPath string
		command string
		wantDe  string
	}{
		{"denied command", withRules, "git push --force", "deny"},
		{"allowed command", withRules, "git status", "allow"},
		{"wrapped denied command", withRules, "bash -ec 'git push --force'", "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hookOut, err := evaluate("claude-code", tc.cfgPath, "", strings.NewReader(payload(tc.command)))
			if err != nil {
				t.Fatal(err)
			}
			hookDecision := "allow"
			if strings.Contains(string(hookOut.Stdout), `"deny"`) {
				hookDecision = "deny"
			}

			var buf, diag strings.Builder
			if err := runCheck(&buf, &diag, tc.command, tc.cfgPath, "", "json"); err != nil {
				t.Fatal(err)
			}
			var got checkResult
			if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
				t.Fatalf("check output is not JSON: %s (%v)", buf.String(), err)
			}

			if hookDecision != tc.wantDe {
				t.Errorf("evaluate decided %q, want %q", hookDecision, tc.wantDe)
			}
			if got.Decision != tc.wantDe {
				t.Errorf("check decided %q, want %q", got.Decision, tc.wantDe)
			}
			if got.Decision != hookDecision {
				t.Errorf("the two surfaces disagree about %q: hook %q, check %q", tc.command, hookDecision, got.Decision)
			}
		})
	}

	// The drift arm: no rules config anywhere. Both surfaces allow — that is
	// by design, an unconfigured project is not gated — but both must SAY that
	// nothing was gated, or "allow" means two different things depending on
	// which surface you asked.
	t.Run("no rules config anywhere: both surfaces disclose it", func(t *testing.T) {
		t.Chdir(t.TempDir())

		hookOut, err := evaluate("claude-code", "", "", strings.NewReader(payload("git push --force")))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(hookOut.Stderr), "nothing is being gated") {
			t.Errorf("evaluate must disclose that no rules config was found, got %q", hookOut.Stderr)
		}

		var buf, diag strings.Builder
		if err := runCheck(&buf, &diag, "git push --force", "", "", "json"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diag.String(), "nothing is being gated") {
			t.Errorf("check reported %s with no hint that there are no rules at all; a caller cannot tell it from a clean allow. diagnostics: %q",
				buf.String(), diag.String())
		}
	})
}
