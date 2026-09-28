//go:build arch

// THE HARDEST INVARIANT IN THE ENGINE-HOME MODEL, and until now the only one
// with nothing pinning it: **ctxloom never writes the engine's real host home.**
//
// Everything else in the model is a consequence of it. The durable truth of a
// user's engine configuration — claude's
// credentials and per-project keys — lives in the engine's own dotdir under
// the user's home, and those are the user's. ctxloom reads only what an
// engine's instance config carries across and points engines at throwaway
// per-session instances instead. A single write-back would make an instance's
// disposability a lie and could destroy configuration no clone and no rebuild
// can restore.
//
// A path assertion cannot prove this: it can only say where ctxloom MEANT to
// write. So this gate drives the real launch machinery against a SCRATCH $HOME
// carrying realistic engine homes, hashes those homes before and after, and
// requires byte identity. Any write — a trust pre-seed, a managed [hooks]
// table, a copied credential going the wrong way, even an empty directory
// created as a side effect — moves the hash.
package arch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// realHomeFixture writes a believable host home for each engine into a
// scratch HOME and returns it. Every file carries CONTENT: a hash comparison
// between two empty trees is vacuous, which is this project's characteristic
// false green.
func realHomeFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("fixture mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("fixture write %s: %v", path, err)
		}
	}

	// claude: the user's native login, which must never be copied, plus the
	// personal top-level config that must never be copied OR modified.
	write(filepath.Join(".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"host-token","refreshToken":"host-refresh","expiresAt":1,"refreshTokenExpiresAt":2,"subscriptionType":"max"}}`, 0o600)
	write(".claude.json", `{"mcpServers":{"personal":{"command":"secret"}}}`, 0o600)

	// codex: the credential, the user's own config.toml with their model
	// preference and their own accumulated project-trust answer.
	write(filepath.Join(".codex", "auth.json"), `{"tokens":{"access_token":"host-codex"}}`, 0o600)
	write(filepath.Join(".codex", "config.toml"),
		"model = 'o3'\napproval_policy = 'on-request'\n\n[projects.\"/somewhere/else\"]\ntrust_level = 'trusted'\n", 0o644)
	write(filepath.Join(".codex", "prompts", "personal.md"), "# my own prompt\n", 0o644)

	return home
}

// hashTree is the whole-directory fingerprint: every relative path, its mode
// bits and its bytes, folded in sorted order so the result is stable. Absent
// directories hash to a distinct sentinel rather than to the empty string, so
// "the home was deleted" cannot read as "the home was unchanged".
func hashTree(t *testing.T, root string) string {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return "<absent>"
	}
	type entry struct {
		rel  string
		mode fs.FileMode
		sum  string
	}
	var entries []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		e := entry{rel: rel, mode: info.Mode()}
		if !d.IsDir() {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			raw := sha256.Sum256(data)
			e.sum = hex.EncodeToString(raw[:])
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		t.Fatalf("hashTree(%s): %v", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%o\x00%s\x00", e.rel, e.mode, e.sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// realHomeSnapshot fingerprints every engine home at once.
func realHomeSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, leaf := range []string{".claude", ".claude.json", ".codex"} {
		snap[leaf] = hashTree(t, filepath.Join(home, leaf))
	}
	for leaf, sum := range snap {
		if sum == "<absent>" {
			t.Fatalf("fixture is missing %s; a byte-identity comparison against an absent tree proves nothing", leaf)
		}
	}
	return snap
}

func resetArchStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

// launchManaged is a realistic managed payload: hooks, an MCP server, a
// command and a skill. Everything an engine's home-keyed surfaces would write.
func launchManaged() *agent.ManagedConfig {
	return &agent.ManagedConfig{
		Commands: []agent.CommandExport{{Name: "review", Content: "review it", Enabled: true}},
		Skills: []agent.SkillExport{{
			Name:        "humanize",
			Description: "removes AI writing tells",
			Enabled:     true,
			Files:       []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: removes AI writing tells\n---\nBody.\n"), Mode: 0o644}},
		}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
}

// TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch is the gate.
// A `engine_home: session` run of every home-controlled engine resolves its
// per-session instance and performs the launch delivery into it. Afterwards
// every real host home must hash exactly as it did before.
//
// MUTATION TARGET: make any prepare/delivery step write into the real home —
// copy a credential back, pre-seed trust there, write a managed [hooks] table
// there — and this goes red naming the tree that changed.
func TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch(t *testing.T) {
	resetArchStrictness(t)
	home := realHomeFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-arch-fixture")
	workDir := t.TempDir()
	const harp = "ugly-icy-squid"

	before := realHomeSnapshot(t, home)
	instances := sessionInstances(t, workDir, harp)
	requirePopulatedInstance(t, instances["claude-code"])

	// Drive the real delivery against the instance the contribution just
	// named — the engine Definition's approaches, under the roots the runner
	// advises (the project as the workspace, the instance as the engine
	// home) — so the invariant below is asserted over a launch that actually
	// delivered rather than one that did nothing.
	//
	// NOTE ON REACH: no currently-registered engine keys its hooks, MCP
	// servers, prompts and skills to its HOME — the engine that did is gone —
	// so this drives the cwd-keyed path only. A home-keyed engine's delivery
	// is the fullest home-writing path there is, and until one exists again
	// this gate does not cover it.
	deliverIntoInstance(t, workDir, instances["claude-code"])

	assertHomeUnchanged(t, before, realHomeSnapshot(t, home))
}

// sessionInstances prepares each home-controlled engine's per-session home
// through the environment a engine_home: session host run gets.
func sessionInstances(t *testing.T, workDir, harp string) map[string]string {
	t.Helper()
	instances := map[string]string{}
	for _, backend := range []engine.Name{"claude-code"} {
		eng, ok := engines.Registry().Lookup(backend)
		if !ok {
			t.Fatalf("%s is not registered", backend)
		}
		sessionDir, err := paths.HarpDir(harp)
		if err != nil {
			t.Fatalf("session dir: %v", err)
		}
		spec, err := isolation.NewSpec(launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}, eng).
			Project(workDir).Session(harp, sessionDir, isolation.SessionState{Harp: harp}).Home(agents.HomeModeSession).Build()
		if err != nil {
			t.Fatalf("%s: spec: %v", backend, err)
		}
		env, err := isolation.Prepare(context.Background(), spec)
		if err != nil {
			t.Fatalf("%s: prepare: %v", backend, err)
		}
		t.Cleanup(func() { _ = env.Cleanup() })
		pl := env.Placement()
		if len(pl.Home) != 1 {
			t.Fatalf("%s: a engine_home: session run must be handed exactly one config-home var, got %v", backend, pl.Home)
		}
		instances[string(backend)] = pl.Paths.Paths().SessionHome.Host
	}
	return instances
}

// requirePopulatedInstance requires claude's instance to be REAL and
// populated — otherwise "the host home did not change" would be trivially
// true because nothing happened at all — and to carry no credential: the run
// authenticates from its env, so the real home's credential must not appear
// in the instance.
func requirePopulatedInstance(t *testing.T, instance string) {
	t.Helper()
	if got := hashTree(t, instance); got == "<absent>" {
		t.Fatal("claude's instance was never created; the invariant below would be vacuous")
	}
	if _, err := os.Stat(filepath.Join(instance, ".claude.json")); err != nil {
		t.Fatalf("claude's instance config was never generated (%v); the preparation must have happened for this gate to mean anything", err)
	}
	if _, err := os.Lstat(filepath.Join(instance, ".credentials.json")); !os.IsNotExist(err) {
		t.Errorf("a credential file appeared in claude's session home (%v); no credential is ever copied out of the real home", err)
	}
}

// deliverIntoInstance drives claude's context, MCP and hooks delivery with
// the project as the workspace and instance as the engine home, and requires
// it to have landed something.
func deliverIntoInstance(t *testing.T, workDir, instance string) {
	t.Helper()
	eng, err := claude.Build()
	if err != nil {
		t.Fatalf("claude engine: %v", err)
	}
	def := eng.Root().Definition
	start := present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: workDir, Engine: workDir},
		SessionHome: present.Root{Host: instance, Engine: instance},
	}))
	managed := launchManaged()
	if _, err := def.Context.DeliverContext(start, present.RootProjectRoot, engine.ContextInputs{Text: []byte("project rules")}, nil); err != nil {
		t.Fatalf("claude context delivery: %v", err)
	}
	if _, err := def.MCP.DeliverMCP(start, present.RootProjectRoot, engine.MCPInputs{Servers: managed.BundleMCP}, nil); err != nil {
		t.Fatalf("claude MCP delivery: %v", err)
	}
	if _, err := def.Hooks.DeliverHooks(start, present.RootProjectRoot, engine.HooksInputs{Hooks: managed.Hooks.Unified}, nil); err != nil {
		t.Fatalf("claude hooks delivery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, claude.ConfigDirName)); err != nil {
		t.Fatalf("claude's delivery landed nothing in the project (%v); the invariant below would be vacuous", err)
	}
}

// assertHomeUnchanged fails for every real-home tree whose hash changed.
func assertHomeUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for leaf, want := range before {
		if after[leaf] != want {
			t.Errorf("ctxloom modified the user's real %s during an in-tree agent launch.\n"+
				"The real host home is the DURABLE truth of the user's engine configuration and ctxloom never writes it: "+
				"per-session instances are copied-into one way and thrown away. A write here can destroy configuration "+
				"nothing rebuilds. Find what wrote it rather than relaxing this gate.", leaf)
		}
	}
}

// TestArch_InstanceHomesLiveInsideTheSessionsStore is the other side of the
// same coin: wherever ctxloom DOES write an engine home, it is the session's
// own member under the home-rooted sessions store (paths.HarpSessionEngineHomes) —
// never the user's real engine home, never the project tree, and never the
// cache tier a `deps pull` could clobber.
func TestArch_InstanceHomesLiveInsideTheSessionsStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const harp = "ugly-icy-squid"
	store := filepath.Join(home, paths.AppDirName, paths.SessionsDir, harp) + string(filepath.Separator)

	root, err := paths.HarpSessionEngineHomes(harp)
	if err != nil {
		t.Fatalf("paths.HarpSessionEngineHomes: %v", err)
	}
	for name, dir := range map[string]string{
		"claude-code": filepath.Join(root, claude.HomeLeaf),
	} {
		if !strings.HasPrefix(dir, store) {
			t.Errorf("%s's instance %q is not inside the session's own dir %q", name, dir, store)
		}
		if strings.HasPrefix(dir, filepath.Join(home, ".claude")) {
			t.Errorf("%s's instance %q sits inside the user's real engine home", name, dir)
		}
		if strings.Contains(dir, filepath.Join(".ctxloom", "cache")) {
			t.Errorf("%s's instance %q sits in the cache tier, which a rebuild may wipe and reconstruct", name, dir)
		}
	}
}
