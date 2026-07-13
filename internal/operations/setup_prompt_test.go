package operations

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveSetupPrompt_NoSkillsIsBuiltinAlone proves the built-in alone is
// returned when nothing installed ships an `agent-setup` skill, and that a nil
// config is safe (never blocks setup).
func TestResolveSetupPrompt_NoSkillsIsBuiltinAlone(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "misc", `version: "1.0"
skills:
  something-else:
    content: "UNRELATED"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(cfg, "BUILTIN"),
		"no agent-setup skill installed → the built-in prompt alone")
	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(nil, "BUILTIN"),
		"a nil config is safe and falls back to the built-in")
}

// TestResolveSetupPrompt_OneSkillAugmentsBuiltin proves a bundle shipping a
// single `agent-setup` skill ADDS to the built-in rather than replacing it —
// "bundles can ship setup guidance" now means augment, not override.
func TestResolveSetupPrompt_OneSkillAugmentsBuiltin(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "onboarding", `version: "1.0"
skills:
  agent-setup:
    content: "BUNDLE-SHIPPED-SETUP-PROMPT"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	got := ResolveSetupPrompt(cfg, "BUILTIN-DEFAULT")
	assert.Contains(t, got, "BUILTIN-DEFAULT", "the built-in guidance must still be present")
	assert.Contains(t, got, "BUNDLE-SHIPPED-SETUP-PROMPT", "the bundle's agent-setup content must be added")
}

// TestResolveSetupPrompt_TwoSkillsComposeInStableOrder proves TWO installed
// agent-setup skills (e.g. a personal repo's and a company repo's) both land
// in the composed prompt, alongside the built-in, in a deterministic
// (sorted-by-skill-name) order regardless of which bundle happened to load
// first.
func TestResolveSetupPrompt_TwoSkillsComposeInStableOrder(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	// "alpha-onboarding" sorts before "zebra-onboarding" by bundle-qualified
	// skill name, so the composed order is deterministic across repeated runs
	// regardless of directory listing order.
	writeRegenBundle(t, appDir, "zebra-onboarding", `version: "1.0"
skills:
  agent-setup:
    content: "ZEBRA-SETUP-CONTENT"
`)
	writeRegenBundle(t, appDir, "alpha-onboarding", `version: "1.0"
skills:
  agent-setup:
    content: "ALPHA-SETUP-CONTENT"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	got := ResolveSetupPrompt(cfg, "BUILTIN")
	require.Contains(t, got, "BUILTIN")
	require.Contains(t, got, "ALPHA-SETUP-CONTENT")
	require.Contains(t, got, "ZEBRA-SETUP-CONTENT")
	assert.Less(t, strings.Index(got,"BUILTIN"), strings.Index(got,"ALPHA-SETUP-CONTENT"),
		"the built-in leads every contribution")
	assert.Less(t, strings.Index(got,"ALPHA-SETUP-CONTENT"), strings.Index(got,"ZEBRA-SETUP-CONTENT"),
		"alpha-onboarding#agent-setup sorts before zebra-onboarding#agent-setup")

	// Re-resolving must reproduce the exact same order — this is a
	// deterministic composition, not an accident of one run's map iteration.
	again := ResolveSetupPrompt(cfg, "BUILTIN")
	assert.Equal(t, got, again, "composition order must be stable across repeated resolutions")
}

// TestResolveSetupPrompt_CompanionLoadoutSkillAugmentsBuiltin proves the
// paced-trump claim that a companion loadout's `agent-setup` skill lands in
// the SAME seeded set a repo bundle's does (config.go's SeededBundleLoader
// merges loadRemoteBundleSeed and companionBundleSeed into one map), so
// composing over ListAllSkills picks up an installed companion's setup
// guidance with no separate companion-specific lookup in this package.
func TestResolveSetupPrompt_CompanionLoadoutSkillAugmentsBuiltin(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("HOME", t.TempDir())

	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/fake/ltk", nil
		}
		return "", exec.ErrNotFound
	})
	defer restoreLook()

	bundleYAML := []byte("version: \"1.0.0\"\nskills:\n  agent-setup:\n    content: COMPANION-SHIPPED-SETUP-PROMPT\n")
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, nil, "")
	require.NoError(t, err)
	restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	appDir, _ := regenTestApp(t)
	cfg := &config.Config{AppPaths: []string{appDir}}

	got := ResolveSetupPrompt(cfg, "BUILTIN")
	assert.Contains(t, got, "BUILTIN", "the built-in guidance must still be present")
	assert.Contains(t, got, "COMPANION-SHIPPED-SETUP-PROMPT",
		"an installed companion's agent-setup skill must be composed in exactly like a repo bundle's")
}
