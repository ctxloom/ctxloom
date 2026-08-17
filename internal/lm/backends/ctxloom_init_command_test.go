// ctxloom-init command tests are the LOAD-BEARING proof for init-as-skill
// slice 3's "seed the setup skill into ALL sessions, invocable-not-always-on"
// requirement: the five-phase setup body
// (resources/commands/ctxloom-init.md) must be present in an ORDINARY
// session's command catalog with no profile wiring at all (it's a builtin,
// like check-triggers/discover/recover — see builtinCommands), yet it must
// NEVER be injected into that same session's always-on assembled context —
// that is fragment territory (resources/builtin_bundles/isolation.yaml),
// and a body this large landing there by mistake would tax every session's
// context forever. This file proves the "invocable" half from the backends
// package that builds the command catalog; the "not always-on" half is
// proven from the operations package (see
// internal/operations/context_test.go's
// TestAssembleContext_ExcludesCtxloomInitCommandBody), since AssembleContext
// lives there and never imports this package's command-export machinery in
// the first place — the strongest form of "these are different doors".
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// findByName returns the entry named name, or nil.
func findByName(prompts []*bundles.LoadedContent, name string) *bundles.LoadedContent {
	for _, p := range prompts {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func promptNames(prompts []*bundles.LoadedContent) []string {
	names := make([]string, len(prompts))
	for i, p := range prompts {
		names[i] = p.Name
	}
	return names
}

// TestLoadCommandExports_CtxloomInitAlwaysPresent proves the ctxloom-init
// command reaches an ORDINARY session's catalog unconditionally: no profile
// references it, no bundle ships it, no companion is installed — the only
// source is ctxloom's own embedded builtinCommands() sweep, exactly like
// check-triggers/discover/recover. A bare, from-scratch project config with
// zero profiles/bundles still gets the reconfigure door.
func TestLoadCommandExports_CtxloomInitAlwaysPresent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	prompts := LoadCommandExports(cfg, nil)
	found := findByName(prompts, "ctxloom-init")
	require.NotNil(t, found, "ctxloom-init missing from LoadCommandExports; got names: %v", promptNames(prompts))
	assert.NotEmpty(t, found.LLM.ClaudeCode.Description, "ctxloom-init must carry its frontmatter description for /help listings")
	assert.Contains(t, found.Content, "Phase 2", "ctxloom-init's exported content must be the five-phase body, not a placeholder")
}
