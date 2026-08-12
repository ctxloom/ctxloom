//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// This file covers agent.ApproachHook — the delivery approach whose destination
// is NOT a file the engine reads but the JSON a SessionStart hook PRINTS.
//
// The matrix file (delivery_approach_matrix_test.go) can only assert the hook
// approach's file-side halves, because at the surface seam claude's hook arm is
// a documented no-op. The approach is only real once the SessionStart hook is
// registered in the engine's settings AND the hook, when run, emits the
// context. Both are asserted here, ending at the hook's own stdout — the last
// place the payload can silently disappear.

// hookSentinel is the payload traced from a profile fragment all the way to the
// JSON the SessionStart hook prints.
const hookSentinel = "CTXSENTINEL-hookdelivered-marker"

// hookBundleYAML ships one fragment whose content IS the sentinel.
const hookBundleYAML = "version: \"1.0.0\"\nfragments:\n  sentinel:\n    content: \"" + hookSentinel + "\"\n"

// applyWithContextRegen lays out an app dir holding the sentinel bundle and a
// default profile that pulls its fragment, then runs operations.ApplyHooks with
// context regeneration ON — the apply path that INSTALLS the SessionStart
// inject-context hook for the hook-approach backends (claude/codex). It returns
// the project dir and the regenerated context hash.
func applyWithContextRegen(t *testing.T) (projectDir, contextHash string) {
	t.Helper()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "content", "bundles") // paths.LocalBundlesPath layout
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hookdemo.yaml"), []byte(hookBundleYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "base.yaml"),
		[]byte("name: base\nbundles:\n  - hookdemo#fragments/sentinel\n"), 0o644))

	projectDir = t.TempDir()
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"base"}}},
		AppPaths:     []string{appDir},
	})

	res, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
		WorkDir:           projectDir,
		FS:                afero.NewOsFs(),
		ConfigLoader:      func() (*config.Config, error) { return cfg, nil },
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// The hash is the hook's ONLY argument, so an empty one means the hook that
	// gets registered can never find anything to inject. Asserted before any
	// file check: a "applied, 0 errors" result with an empty hash is precisely
	// the exit-0-and-zero-bytes shape.
	require.NotEmpty(t, res.ContextHash,
		"apply reported %q with no errors but regenerated NO context — the hook it installs would inject nothing",
		res.Status)
	return projectDir, res.ContextHash
}

// TestHookApproach_PayloadReachesTheInjectedContext is the end-to-end assertion
// for agent.ApproachHook: a fragment's bytes must reach the JSON the SessionStart
// hook prints. It checks each link of that chain by PAYLOAD.
func TestHookApproach_PayloadReachesTheInjectedContext(t *testing.T) {
	projectDir, hash := applyWithContextRegen(t)

	// Link 1 — the content-addressed cache file the hook reads.
	cachePath := filepath.Join(projectDir, ".ctxloom", "cache", "context", hash+".md")
	cached, err := os.ReadFile(cachePath)
	require.NoError(t, err, "the hook approach must leave a cache file at the regenerated hash")
	assert.Contains(t, string(cached), hookSentinel,
		"the cache file exists but does not carry the fragment payload")

	// Link 2 — the hook itself, registered in each hook-approach backend's
	// settings surface AND keyed to that same hash. A hook registered with a
	// stale or empty hash reads a file that is not there and injects nothing.
	for _, tc := range []struct{ name, path string }{
		{"claude-code", filepath.Join(projectDir, ".claude", "settings.json")},
		// codex's config home is the one ctxloom relocates, so its path is
		// asked of codex's own writer (internal/codex.ProjectHome) rather than
		// spelled out here.
		{"codex", (&codex.CodexHookWriter{}).SettingsPath(projectDir)},
	} {
		t.Run("hook registered/"+tc.name, func(t *testing.T) {
			b, err := os.ReadFile(tc.path)
			require.NoError(t, err, "%s: the settings surface carrying the hook must exist", tc.name)
			settings := string(b)
			assert.Contains(t, settings, "hook inject-context",
				"%s: the SessionStart inject-context hook must be registered", tc.name)
			assert.Contains(t, settings, hash,
				"%s: the registered hook must name the hash of the context just regenerated", tc.name)
		})
	}

	// Link 2b — claude's hook approach must NOT also write a native CLAUDE.md.
	// A static file alongside the hook would DOUBLE the context, which is the
	// documented reason claude's hook arm resolves to a no-op.
	_, err = os.Stat(filepath.Join(projectDir, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(err),
		"claude delivers context via the hook here, so no native CLAUDE.md may be written alongside it")

	// Link 3 — the hook's OWN OUTPUT. This is the destination the approach
	// promises, and the only link an exit code cannot vouch for: the hook is
	// written to always emit valid JSON, so a hook that found nothing still
	// exits 0 and prints "{}".
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })

	cmd := exec.Command(env.AppBinary, "hook", "inject-context", "--project", projectDir, hash)
	cmd.Stdin = strings.NewReader(`{"session_id":"delivery-matrix","source":"startup"}`)
	out, err := cmd.Output()
	require.NoError(t, err, "the inject-context hook must run cleanly; stdout was %q", string(out))
	require.NotEmpty(t, out, "the hook printed NOTHING — a SessionStart hook that emits no JSON injects no context")

	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(out, &payload),
		"the hook must emit parseable SessionStart JSON, got %q", string(out))

	require.NotEmpty(t, payload.HookSpecificOutput.AdditionalContext,
		"the hook emitted valid JSON with an EMPTY additionalContext — exit 0, success shape, zero context")
	assert.Contains(t, payload.HookSpecificOutput.AdditionalContext, hookSentinel,
		"the fragment payload must reach the injected context, which is what agent.ApproachHook promises")
}

// TestHookApproach_MissingCacheFileInjectsNothingAndSaysSo is the negative
// direction for the hook approach's terminal link: a hook whose cache file is
// absent (reaped cache, stale hash) must NOT report an injected context.
//
// It pins the current contract exactly: the hook still exits 0 and still emits
// valid JSON — deliberate, so the engine's session start never hangs waiting for
// output — but the additionalContext must be EMPTY rather than carrying a
// plausible-looking envelope with no content in it, and the reason must reach
// stderr rather than being swallowed.
func TestHookApproach_MissingCacheFileInjectsNothingAndSaysSo(t *testing.T) {
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })

	projectDir := t.TempDir()

	cmd := exec.Command(env.AppBinary, "hook", "inject-context", "--project", projectDir, "deadbeefdeadbeef")
	cmd.Stdin = strings.NewReader(`{"session_id":"delivery-matrix","source":"startup"}`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "the hook must not fail the engine's session start over a missing cache file")

	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(out, &payload),
		"even with nothing to inject the hook must emit parseable JSON, got %q", string(out))
	assert.Empty(t, payload.HookSpecificOutput.AdditionalContext,
		"with no cache file there is nothing to inject, so the payload must be empty rather than a content-free envelope")
	assert.Contains(t, stderr.String(), "inject-context",
		"the reason the hook injected nothing must reach stderr, not be swallowed")
}
