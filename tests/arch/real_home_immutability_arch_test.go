//go:build arch

// THE HARDEST INVARIANT IN THE ENGINE-HOME MODEL, and until now the only one
// with nothing pinning it: **ctxloom never writes the engine's real host home.**
//
// Everything else in the model is a consequence of it. The durable truth of a
// user's engine configuration — claude's
// credentials and per-project keys — lives in the engine's own dotdir under
// the user's home, and those are the user's. ctxloom reads
// them (one-way copy-in at instance time) and points engines at throwaway
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

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
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

	// claude: the credential the copy-in reads, plus the personal top-level
	// config that must never be copied OR modified. The credential carries the
	// full live shape (accessToken + the single-use rotating refreshToken half)
	// so the copy-in's refresh-token STRIP is exercised and the real home's
	// refresh token can be shown untouched below.
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
// per-session instance and, for codex (the only engine with home-keyed
// surfaces), performs the full Setup delivery into it. Afterwards every real
// host home must hash exactly as it did before.
//
// MUTATION TARGET: make any prepare/delivery step write into the real home —
// copy a credential back, pre-seed trust there, write a managed [hooks] table
// there — and this goes red naming the tree that changed.
func TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch(t *testing.T) {
	resetArchStrictness(t)
	home := realHomeFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	workDir := t.TempDir()
	const harp = "ugly-icy-squid"

	before := realHomeSnapshot(t, home)

	instances := map[string]string{}
	for _, backend := range []string{"claude-code"} {
		res := operations.ResolveInTreeAgentHome(operations.InTreeAgentHome{
			Backend:  backend,
			Cwd:      workDir,
			Harp:     harp,
			HomeMode: agents.HomeModeSession,
		})
		if res.Absent != "" {
			t.Fatalf("%s: a engine_home: session run must be handed a home, got absent: %s", backend, res.Absent)
		}
		if len(res.Env) != 1 {
			t.Fatalf("%s: a engine_home: session run must be handed exactly one config-home var, got %v", backend, res.Env)
		}
		for _, v := range res.Env {
			instances[backend] = v
		}
	}

	// The instances are REAL and populated — otherwise "the host home did not
	// change" would be trivially true because nothing happened at all.
	if got := hashTree(t, instances["claude-code"]); got == "<absent>" {
		t.Fatal("claude's instance was never created; the invariant below would be vacuous")
	}
	credential, err := os.ReadFile(filepath.Join(instances["claude-code"], ".credentials.json"))
	if err != nil || len(credential) == 0 {
		t.Fatalf("claude's credential never reached the instance (%v); the provisioning must have happened for this gate to mean anything", err)
	}

	// THE ROOT'S COPY IS WHOLE (ruled 2026-09-21): this resolution names no
	// orchestrator, so it is the orchestrator's own — the single ctxloom-side
	// refresher, two-way with the host file through the replicator. The
	// refresh half must be PRESENT here.
	//
	// The byte-identity gate below is what holds the other half honest:
	// ctxloom itself writes nothing into the real home.
	if s := string(credential); !strings.Contains(s, "host-refresh") {
		t.Errorf("the orchestrator's instance credential lost its refresh token; the root is the one refresher and must hold the whole credential.\nplaced: %s", s)
	}
	if !strings.Contains(string(credential), "host-token") {
		t.Errorf("claude's instance credential lost its access token; it must still authenticate.\nplaced: %s", string(credential))
	}

	// AN AGENT'S COPY IS A PROJECTION of the orchestrator's: read from the
	// root's session home just resolved, never from the real home, with the
	// single-use refresh token withheld — so no agent can consume the host's
	// grant and revoke the human's login.
	child := operations.ResolveInTreeAgentHome(operations.InTreeAgentHome{
		Backend: "claude-code", Cwd: workDir, Harp: "brave-warm-otter", HomeMode: agents.HomeModeSession, Orchestrator: harp,
	})
	if child.Absent != "" {
		t.Fatalf("an agent under the root must be handed a home, got absent: %s", child.Absent)
	}
	t.Cleanup(func() { _ = child.Release() })
	projected, err := os.ReadFile(filepath.Join(child.Root.Host, ".credentials.json"))
	if err != nil {
		t.Fatalf("the agent's credential never reached its instance: %v", err)
	}
	if s := string(projected); strings.Contains(s, "host-refresh") || !strings.Contains(s, "host-token") {
		t.Errorf("the agent's credential must be the orchestrator's access half only.\nplaced: %s", s)
	}

	// Drive a real Setup against the instance the contribution just named, so
	// the invariant below is asserted over a launch that actually delivered
	// rather than one that did nothing.
	//
	// NOTE ON REACH: no currently-registered engine keys its hooks, MCP
	// servers, prompts and skills to its HOME — the engine that did is gone —
	// so this drives the cwd-keyed path only. A home-keyed engine's Setup is
	// the fullest home-writing path there is, and until one exists again this
	// gate does not cover it.
	b := claude.NewClaudeCode()
	if err := b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir: workDir,
		Env: map[string]string{
			claude.ConfigDirEnv: instances["claude-code"],
			// CellKindShared's Setup now refuses loudly without a resolvable
			// harp (ErrSharedScratchNoHarp) instead of silently falling back
			// to the OS temp dir — see taskloom urgent-staunch.
			agent.SessionHarpEnv: "perky-same-chevy",
		},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		CellKind:  agent.CellKindShared,
		Managed:   launchManaged(),
	}); err != nil {
		t.Fatalf("claude Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, claude.ConfigDirName)); err != nil {
		t.Fatalf("claude's Setup delivered nothing into the project (%v); the invariant below would be vacuous", err)
	}

	after := realHomeSnapshot(t, home)
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
// own member under the home-rooted sessions store (paths.HarpSessionHome) —
// never the user's real engine home, never the project tree, and never the
// cache tier a `deps pull` could clobber.
func TestArch_InstanceHomesLiveInsideTheSessionsStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const harp = "ugly-icy-squid"
	store := filepath.Join(home, paths.AppDirName, paths.SessionsDir, harp) + string(filepath.Separator)

	root, err := paths.HarpSessionHome(harp)
	if err != nil {
		t.Fatalf("paths.HarpSessionHome: %v", err)
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
