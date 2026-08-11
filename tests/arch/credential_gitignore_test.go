//go:build arch

package arch

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ctxloom seeds real engine credentials into the working tree so a vendor CLI
// can authenticate: codex reads auth.json from $CODEX_HOME, and ctxloom points
// CODEX_HOME at a project-scoped home. There is no vendor surface that
// separates the credential from the rest of CODEX_HOME (checked against
// codex-cli 0.144.4: no auth-path flag, no auth-path env var), so the file
// genuinely lands in the tree and .gitignore is the ONLY thing keeping it out
// of a commit.
//
// A lone .gitignore line is one careless edit away from gone, and the failure is
// silent and unrecoverable — a leaked credential cannot be un-pushed. This gate
// makes the ignore rule load-bearing in the test suite instead of by convention.
//
// BOTH the current and the legacy location are listed. The engine-home policy
// moved the home to .ctxloom/state/engines/codex (paths.EngineStateHome), where
// the single ".ctxloom/state/" rule covers it — but ctxloom's one-time
// migration only runs in checkouts somebody actually opens, so a pre-migration
// tree still holds the old file and still needs its rule.
//
// Add a row here whenever a new engine seeds a credential in-tree.
var credentialPaths = []struct {
	path string
	why  string
}{
	{".ctxloom/state/engines/codex/.codex/auth.json", "codex OAuth/API credential at the CURRENT project-scoped home (paths.EngineStateHome), seeded from the host's ~/.codex/auth.json by isolation.SeedCodexHome"},
	{".codex/auth.json", "codex OAuth/API credential at the LEGACY pre-migration home; a checkout that never runs ctxloom again keeps this file forever"},
}

func TestArch_SeededCredentialsAreGitignored(t *testing.T) {
	root := moduleRoot(t)

	for _, c := range credentialPaths {
		t.Run(c.path, func(t *testing.T) {
			// git check-ignore exits 0 when the path IS ignored, 1 when it is not.
			// Ask git rather than parsing .gitignore ourselves: negation patterns
			// (this repo has `!.codex/prompts/`) make hand-parsing wrong in exactly
			// the cases that matter.
			cmd := exec.Command("git", "check-ignore", "-q", "--no-index", c.path)
			cmd.Dir = root
			err := cmd.Run()
			if err != nil {
				t.Errorf("%s is NOT gitignored — %s.\n"+
					"This file is a real credential written into the working tree. "+
					"Without an ignore rule it can be committed and cannot be un-leaked. "+
					"Restore the rule in .gitignore rather than deleting this test.",
					c.path, c.why)
			}
		})
	}
}

// TestArch_CredentialIgnoreSurvivesNegation guards the subtler failure: this
// repo re-includes .codex/prompts/ with a negation, so `.codex/*` is not a
// blanket ignore. If a credential ever landed under a re-included directory it
// would be tracked despite the rule above passing.
func TestArch_CredentialIgnoreSurvivesNegation(t *testing.T) {
	root := moduleRoot(t)

	out, err := exec.Command("git", "-C", root, "ls-files", ".codex/").Output()
	if err != nil {
		t.Fatalf("git ls-files .codex/: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tracked := strings.TrimSpace(line)
		if tracked == "" {
			continue
		}
		// Everything legitimately tracked under .codex/ is a ctxloom-generated
		// prompt. Anything else is either a credential or unreviewed engine state.
		if !strings.HasPrefix(tracked, ".codex/prompts/") {
			t.Errorf("unexpected tracked file under .codex/: %s\n"+
				"Only ctxloom-generated prompts belong here. CODEX_HOME also holds "+
				"auth.json, config.toml and skills, which codex mutates every run.",
				tracked)
		}
		if filepath.Base(tracked) == "auth.json" {
			t.Errorf("A CREDENTIAL IS TRACKED: %s", tracked)
		}
	}
}
