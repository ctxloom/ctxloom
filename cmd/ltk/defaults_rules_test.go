package main

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// The shipped defaults as a decision table: each line names the rule that must
// deny it, or "" for a command that must pass. Both directions are listed for
// every guard, because an exemption tested alone is satisfied just as well by a
// rule that stopped matching anything.
func TestDefaultRules_DecisionTable(t *testing.T) {
	cfg, err := rules.Parse([]byte(defaultRules))
	if err != nil {
		t.Fatalf("default rules do not parse: %v", err)
	}
	for _, tc := range []struct {
		shell ir.Shell
		line  string
		rule  string
	}{
		// Identity: writes denied, reads allowed.
		{ir.ShellBash, "git config user.name", ""},
		{ir.ShellBash, "git config --get user.email", ""},
		{ir.ShellBash, "git config --get user.name Bo.*", ""},
		{ir.ShellBash, "git config get user.name", ""},
		{ir.ShellBash, "git -c k=v config user.name", ""},
		{ir.ShellBash, "git config user.name Bob", "no-rewrite-git-identity"},
		{ir.ShellBash, "git config User.Name Bob", "no-rewrite-git-identity"},
		{ir.ShellBash, "git config --local user.email b@x", "no-rewrite-git-identity"},
		{ir.ShellBash, "git config set user.name Bob", "no-rewrite-git-identity"},
		{ir.ShellBash, "git config --unset user.name", "no-unset-git-identity"},
		{ir.ShellBash, "git config --unset-all user.email", "no-unset-git-identity"},
		{ir.ShellBash, "git config unset user.name", "no-unset-git-identity"},
		// Hooks: -n is commit's --no-verify, but push's --dry-run.
		{ir.ShellBash, "git commit -n -m x", "no-skip-commit-hooks"},
		{ir.ShellBash, "git commit --no-verify -m x", "no-skip-commit-hooks"},
		{ir.ShellBash, "git commit -m x", ""},
		{ir.ShellBash, "git push -n origin main", ""},
		{ir.ShellBash, "git push --no-verify", "no-skip-push-hooks"},
		// Force push, by flag and by refspec.
		{ir.ShellBash, "git push -f", "no-force-push"},
		{ir.ShellBash, "git push origin +main", "no-force-push-refspec"},
		{ir.ShellBash, "git push --force-with-lease", ""},
		{ir.ShellBash, "git push origin main", ""},
		// Staging: `.` is the literal dot, not any one character.
		{ir.ShellBash, "git add .", "stage-explicitly"},
		{ir.ShellBash, "git add a", ""},
		// Recursive force-delete, every spelling.
		{ir.ShellBash, "rm -rf x", "rm-rf-careful"},
		{ir.ShellBash, "rm -R -f x", "rm-rf-careful"},
		{ir.ShellBash, "rm --recursive --force x", "rm-rf-careful"},
		{ir.ShellBash, "rm -r x", ""},
		// git clean: dry runs exempt.
		{ir.ShellBash, "git clean -fdx", "no-git-clean"},
		{ir.ShellBash, "git clean -n", ""},
		// Encoded PowerShell: any case, any unambiguous prefix, .exe or not.
		{ir.ShellBash, "pwsh -EncodedCommand ZQBjAGgAbwA=", "no-encoded-command"},
		{ir.ShellBash, "pwsh -enc ZQBjAGgAbwA=", "no-encoded-command"},
		{ir.ShellPwsh, "PowerShell.exe -ENCO ZQBjAGgAbwA=", "no-encoded-command"},
		{ir.ShellBash, "pwsh -Command Get-Date", ""},
		{ir.ShellBash, "pwsh -ExecutionPolicy Bypass -File x.ps1", ""},
	} {
		d := rules.Evaluate(cfg, &ir.Script{
			Shell:     tc.shell,
			Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: strings.Fields(tc.line)}}}},
		})
		if tc.rule == "" && !d.Allowed {
			t.Errorf("%q: denied by %q, want allowed", tc.line, d.RuleID)
		}
		if tc.rule != "" && (d.Allowed || d.RuleID != tc.rule) {
			t.Errorf("%q: allowed=%v rule=%q, want denied by %q", tc.line, d.Allowed, d.RuleID, tc.rule)
		}
	}
}
