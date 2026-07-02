package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveSetupPrompt_BundleOverridesBuiltin proves a bundle shipping a
// `agent-setup` skill supplies the setup prompt instead of the built-in —
// the "bundles can ship the init prompt" capability.
func TestResolveSetupPrompt_BundleOverridesBuiltin(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "onboarding", `version: "1.0"
skills:
  agent-setup:
    content: "BUNDLE-SHIPPED-SETUP-PROMPT"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	got := ResolveSetupPrompt(cfg, "BUILTIN-DEFAULT")
	assert.Equal(t, "BUNDLE-SHIPPED-SETUP-PROMPT", got,
		"a bundle-shipped agent-setup skill overrides the built-in prompt")
}

// TestResolveSetupPrompt_FallsBackToBuiltin proves the built-in is used when no
// bundle ships agent-setup, and that a nil config is safe.
func TestResolveSetupPrompt_FallsBackToBuiltin(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "misc", `version: "1.0"
skills:
  something-else:
    content: "UNRELATED"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(cfg, "BUILTIN"),
		"no agent-setup skill installed → the built-in prompt")
	assert.Equal(t, "BUILTIN", ResolveSetupPrompt(nil, "BUILTIN"),
		"a nil config is safe and falls back to the built-in")
}
