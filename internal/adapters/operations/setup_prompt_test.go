package operations

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// fakeCompanions installs one fake companion per entry — bin name → the
// loadout DOCUMENT its `loadout --format json` probe emits (unsigned) — and
// admits every one of them for execution. The restore is wired to t.Cleanup.
func fakeCompanions(t *testing.T, docs map[string]string) {
	t.Helper()
	t.Cleanup(companions.AdmitEveryDiscoveredCompanionForTesting())
	t.Setenv("HOME", t.TempDir())

	paths := make(map[string]string, len(docs))
	envelopes := make(map[string][]byte, len(docs))
	for bin, doc := range docs {
		path := "/fake/" + bin
		paths[bin] = path
		env, err := signing.EncodeLoadoutEnvelope([]byte(doc), nil, "")
		require.NoError(t, err)
		envelopes[path] = env
	}
	t.Cleanup(companions.SetLookPathForTesting(func(bin string) (string, error) {
		if p, ok := paths[bin]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}))
	t.Cleanup(companions.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		env, ok := envelopes[path]
		if !ok {
			return nil, exec.ErrNotFound
		}
		return env, nil
	}))
}

// TestResolveSetupPrompt_NoGuidanceIsBuiltinAlone proves the built-in alone
// is returned when no installed companion declares setup guidance, and that a
// nil config is safe (never blocks setup).
func TestResolveSetupPrompt_NoGuidanceIsBuiltinAlone(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "run:\n  version: 1.0.0\n  fragments:\n    ltk:\n      content: RUN-ONLY\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})

	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(published(t, cfg), "BUILTIN"),
		"no companion declares setup_guidance → the built-in prompt alone")
	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(nil, "BUILTIN"),
		"a nil config is safe and falls back to the built-in")
}

// TestResolveSetupPrompt_CompanionSetupGuidanceAugmentsBuiltin proves a
// companion's TYPED `init.setup_guidance` ADDS to the built-in rather than
// replacing it: setup guidance is a field of the INIT loadout, read by name
// from the parsed document — not a command that happens to be called
// `agent-setup`.
func TestResolveSetupPrompt_CompanionSetupGuidanceAugmentsBuiltin(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "run:\n  version: 1.0.0\ninit:\n  setup_guidance: COMPANION-SHIPPED-SETUP-GUIDANCE\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})

	got := ResolveSetupPrompt(published(t, cfg), "BUILTIN-DEFAULT")
	assert.Contains(t, got, "BUILTIN-DEFAULT", "the built-in guidance must still be present")
	assert.Contains(t, got, "COMPANION-SHIPPED-SETUP-GUIDANCE", "the companion's typed setup_guidance must be added")
}

// TestResolveSetupPrompt_TwoCompanionsComposeInStableOrder proves TWO
// companions' guidance both land in the composed prompt, alongside the
// built-in, in a deterministic (sorted-by-companion-ref) order regardless of
// which probe returned first.
func TestResolveSetupPrompt_TwoCompanionsComposeInStableOrder(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ctxloom-companion-zebra": "init:\n  setup_guidance: ZEBRA-SETUP-CONTENT\n",
		"ctxloom-companion-alpha": "init:\n  setup_guidance: ALPHA-SETUP-CONTENT\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))

	got := ResolveSetupPrompt(cfg, "BUILTIN")
	require.Contains(t, got, "BUILTIN")
	require.Contains(t, got, "ALPHA-SETUP-CONTENT")
	require.Contains(t, got, "ZEBRA-SETUP-CONTENT")
	assert.Less(t, strings.Index(got, "BUILTIN"), strings.Index(got, "ALPHA-SETUP-CONTENT"),
		"the built-in leads every contribution")
	assert.Less(t, strings.Index(got, "ALPHA-SETUP-CONTENT"), strings.Index(got, "ZEBRA-SETUP-CONTENT"),
		"ctxloom:companion@…alpha sorts before …zebra")

	again := ResolveSetupPrompt(cfg, "BUILTIN")
	assert.Equal(t, got, again, "composition order must be stable across repeated resolutions")
}

// TestResolveSetupPrompt_MagicCommandNameNoLongerContributes pins the clean
// break: a command that happens to be named `agent-setup` — in a project
// bundle or in a companion's RUN loadout — is an ordinary command and reaches
// the init prompt exactly as much as any other command does: not at all. The
// convention it replaced silently no-op'd on a typo and was never actually
// used by any companion; nothing may keep reading it.
func TestResolveSetupPrompt_MagicCommandNameNoLongerContributes(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "run:\n  version: 1.0.0\n  commands:\n    agent-setup:\n      content: COMPANION-MAGIC-COMMAND\n",
	})
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "onboarding", `version: "1.0"
commands:
  agent-setup:
    content: "PROJECT-MAGIC-COMMAND"
`)
	cfg := published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))

	got := ResolveSetupPrompt(cfg, "BUILTIN")
	assert.Equal(t, "BUILTIN", got, "a command named agent-setup is not setup guidance")
}

// TestResolveSetupPrompt_HealthyPathNeverWarns is a regression net: the
// clidiag.Warn calls fire only on their error paths, never on an ordinary
// successful composition.
func TestResolveSetupPrompt_HealthyPathNeverWarns(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "init:\n  setup_guidance: COMPANION-SHIPPED-SETUP-GUIDANCE\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	got := ResolveSetupPrompt(cfg, "BUILTIN")
	assert.Contains(t, got, "COMPANION-SHIPPED-SETUP-GUIDANCE")
	assert.Empty(t, buf.String(), "a healthy composition must never warn")
}
