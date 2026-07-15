package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// materializeFixture builds an isolated project whose `reviewer` profile
// tag-selects a fragment carrying MARK, plus a fresh target dir. Reuses the
// regen helpers (same package): a real appDir the exposure loader reads from.
func materializeFixture(t *testing.T, mark string) (cfg *config.Config, target string) {
	t.Helper()
	testsupport.Isolate(t) // junk HOME + temp cwd: no host-config leak, no source-tree writes
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "`+mark+`"
`)
	cfg = &config.Config{
		AppPaths: []string{appDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"reviewer": {SelectTags: []string{"security"}},
		}},
	}
	return cfg, t.TempDir()
}

// TestMaterializeProfile_WritesClaudeMd proves the core surface: the assembled
// profile context lands in <target>/CLAUDE.md, the backend defaults to
// claude-code, and settings/mcp are written.
func TestMaterializeProfile_WritesClaudeMd(t *testing.T) {
	cfg, target := materializeFixture(t, "MATERIALIZED-CONTENT")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", res.Backend, "backend defaults to claude-code")
	assert.Contains(t, res.Wrote, "context", "the assembled context surface is reported")

	data, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err, "CLAUDE.md must be written to the target dir")
	assert.Contains(t, string(data), "MATERIALIZED-CONTENT",
		"the assembled fragment block is the CLAUDE.md payload")
}

// TestMaterializeProfile_KeepsHomeShadowedCommand is the end-to-end regression
// for sour-feed: `profile materialize --target` must produce a PORTABLE,
// self-contained tree, so a builtin command (e.g. "recover") that happens to
// be byte-identical to a file already sitting in the MATERIALIZING machine's
// own ~/.claude/commands must still land in --target. Pre-fix, claude's
// DeliverCommands unconditionally deduped against GlobalCommandsDir(), silently
// dropping it — exactly the observed cr-correctness bug (3 built-ins missing
// for claude-code only, present for antigravity/kiro/codex, because this host
// happened to already have them installed under ~/.claude/commands).
func TestMaterializeProfile_KeepsHomeShadowedCommand(t *testing.T) {
	cfg, target := materializeFixture(t, "X")

	// Render the "recover" builtin command exactly as materialize itself will (same
	// LoadCommandExports/CommandExportsFor pipeline profile_materialize.go drives),
	// and pre-seed a byte-identical copy into $HOME/.claude/commands — simulating
	// a materializing host that has already installed its own commands (e.g. via
	// `manage hooks install`), which the --target launch environment does NOT
	// share.
	exports := backends.CommandExportsFor("claude-code", backends.LoadCommandExports(cfg, []string{"reviewer"}))
	var seeded bool
	for _, e := range exports {
		if e.Name != "recover" {
			continue
		}
		home := filepath.Join(os.Getenv("HOME"), ".claude", "commands")
		require.NoError(t, os.MkdirAll(home, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(home, "recover.md"), []byte(claude.TransformToClaudeCommand(e)), 0o644))
		seeded = true
		break
	}
	require.True(t, seeded, "precondition: the recover builtin command must be among the exports")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "commands")

	assert.FileExists(t, filepath.Join(target, ".claude", "commands", "recover.md"),
		"a command byte-identical to one in the materializing host's ~/.claude/commands must still land in the portable --target tree")
}

// TestMaterializeProfile_BackendAlias proves `claude` maps to claude-code.
func TestMaterializeProfile_BackendAlias(t *testing.T) {
	cfg, target := materializeFixture(t, "X")
	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "claude",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", res.Backend)
}

// TestMaterializeProfile_OverwritesEachRun proves the export is the source of
// truth: a second run produces a byte-identical CLAUDE.md (deterministic, full
// overwrite — no append/duplication).
func TestMaterializeProfile_OverwritesEachRun(t *testing.T) {
	cfg, target := materializeFixture(t, "ONCE")

	req := MaterializeProfileRequest{Profiles: []string{"reviewer"}, Target: target}
	_, err := MaterializeProfile(context.Background(), cfg, req)
	require.NoError(t, err)
	first, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err)

	_, err = MaterializeProfile(context.Background(), cfg, req)
	require.NoError(t, err)
	second, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second), "re-materialize is a clean overwrite")
}

// TestMaterializeProfile_FoldsProfileInlineMCP proves a profile's OWN inline mcp:
// block reaches the exported .mcp.json. The pre-fix path passed only &cfg.MCP
// (config-level) + bundle MCP to WriteSettings, silently dropping the profile's
// inline servers; AssembleManagedMCP folds them in.
func TestMaterializeProfile_FoldsProfileInlineMCP(t *testing.T) {
	cfg, target := materializeFixture(t, "X")
	// Give the inline reviewer profile its own MCP server (trusted-local, ungated).
	p := cfg.Profiles.Definitions["reviewer"]
	p.MCP = wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"prof-srv": {Command: "prof-cmd"},
	}}
	cfg.Profiles.Definitions["reviewer"] = p

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "mcp")

	data, err := os.ReadFile(filepath.Join(target, ".mcp.json"))
	require.NoError(t, err, "the backend MCP config must be written")
	assert.Contains(t, string(data), "prof-srv",
		"the profile's inline mcp: server must be folded into the exported .mcp.json")
}

// TestMaterializeProfile_Validation covers the guard rails.
func TestMaterializeProfile_Validation(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{AppPaths: []string{t.TempDir()}}

	_, err := MaterializeProfile(ctx, cfg, MaterializeProfileRequest{Profiles: []string{"p"}})
	assert.Error(t, err, "missing target is rejected")

	_, err = MaterializeProfile(ctx, cfg, MaterializeProfileRequest{Target: t.TempDir()})
	assert.Error(t, err, "missing profiles is rejected")

	_, err = MaterializeProfile(ctx, cfg, MaterializeProfileRequest{
		Profiles: []string{"p"}, Target: t.TempDir(), Backend: "bogus",
	})
	assert.Error(t, err, "unknown backend is rejected")

	_, err = MaterializeProfile(ctx, nil, MaterializeProfileRequest{
		Profiles: []string{"p"}, Target: t.TempDir(),
	})
	assert.Error(t, err, "nil config is rejected")
}
