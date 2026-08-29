//go:build arch

// THE HARDEST INVARIANT IN THE ENGINE-HOME MODEL, and until now the only one
// with nothing pinning it: **ctxloom never writes the engine's real host home.**
//
// Everything else in the model is a consequence of it. The durable truth of a
// user's engine configuration — codex's per-project trust entries, claude's
// credentials and per-project keys, kiro's global agents and steering — lives
// in ~/.codex, ~/.claude and ~/.kiro, and those are the user's. ctxloom reads
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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	// parked_engines: codex/kiro are out of the default build. codex was the
	// engine with home-keyed surfaces (the fullest home-writing path there
	// is) so TestArch_RealHostHomesAreByteIdenticalWithNoControlledHome,
	// codex-only, is commented out with it; the other two tests below are
	// trimmed to claude-code. grep -rn parked_engines finds every parked
	// site.
	// "github.com/ctxloom/ctxloom/internal/codex"
	// "github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// realHomeFixture writes a believable ~/.claude, ~/.codex and ~/.kiro into a
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

	// kiro: global steering and an agent, the content KIRO_HOME relocation
	// exists to keep an agent run away from.
	write(filepath.Join(".kiro", "steering", "personal.md"), "my own steering\n", 0o644)
	write(filepath.Join(".kiro", "agents", "mine.json"), `{"name":"mine"}`, 0o644)

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

// realHomeSnapshot fingerprints all three engine homes at once.
func realHomeSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, leaf := range []string{".claude", ".claude.json", ".codex", ".kiro"} {
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
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// launchManaged is a realistic managed payload: hooks, an MCP server, a
// command and a skill. Everything an engine's home-keyed surfaces would write.
func launchManaged() *agent.ManagedConfig {
	return &agent.ManagedConfig{
		Commands: []agent.CommandExport{{Name: "review", Content: "review it", Enabled: true}},
		Skills: []agent.SkillExport{{
			Name:    "humanize",
			Enabled: true,
			Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\n---\nBody.\n"), Mode: 0o644}},
		}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
}

// TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch is the gate.
// A `config_home: project` run of every home-controlled engine resolves its
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
	t.Setenv("KIRO_API_KEY", "")
	workDir := t.TempDir()
	const harp = "ugly-icy-squid"

	before := realHomeSnapshot(t, home)

	instances := map[string]string{}
	// parked_engines: kiro/codex dropped — they are unregistered backends
	// now, so InTreeAgentHomeEnv would hand back nil for them and fail the
	// "exactly one config-home var" precondition below for the wrong
	// reason (unregistered, not "instance resolved").
	for _, backend := range []string{"claude-code"} {
		env := operations.InTreeAgentHomeEnv(operations.InTreeAgentHome{
			Backend:    backend,
			WorkDir:    workDir,
			Harp:       harp,
			ConfigHome: agents.ConfigHomeProject,
			Policy:     isolation.None{},
		})
		if len(env) != 1 {
			t.Fatalf("%s: a config_home: project run must be handed exactly one config-home var, got %v", backend, env)
		}
		for _, v := range env {
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
		t.Fatalf("claude's credential was not copied into the instance (%v); the copy-in must have happened for this gate to mean anything", err)
	}

	// ACCESS-TOKEN-ONLY (easiest-stomp): the copy is a STRICT SUBSET of the host
	// credential — the access token authenticates it, the single-use rotating
	// refresh token is stripped so a refresh in this disposable home can never
	// invalidate the human's real ~/.claude login. The byte-identity gate below
	// already proves the real home was untouched; this proves the COPY is the
	// safe subset rather than a verbatim clone.
	if s := string(credential); strings.Contains(s, "refreshToken") || strings.Contains(s, "host-refresh") {
		t.Errorf("claude's instance credential still carries the refresh token; a copied home that can refresh rotates and invalidates the host's single-use token.\ncopy: %s", s)
	}
	if !strings.Contains(string(credential), "host-token") {
		t.Errorf("claude's instance credential lost its access token; the copy must still authenticate.\ncopy: %s", string(credential))
	}

	// parked_engines: codex was the engine whose hooks, MCP servers, prompts
	// and skills are all home-keyed — its Setup was the fullest
	// home-writing path there is, driven here against the instance the
	// contribution above named. internal/codex is out of the default build;
	// this exercise (and TestArch_RealHostHomesAreByteIdenticalWithNoControlledHome,
	// codex's D2 refusal half) comes back with the package.
	//
	// b := codex.NewCodex()
	// if err := b.Setup(context.Background(), &agent.SetupRequest{
	// 	WorkDir:   workDir,
	// 	Env:       map[string]string{codex.CodexHomeEnv: instances["codex"]},
	// 	Fragments: []*agent.Fragment{{Content: "project rules"}},
	// 	CellKind:  agent.CellKindShared,
	// 	Managed:   launchManaged(),
	// }); err != nil {
	// 	t.Fatalf("codex Setup: %v", err)
	// }
	// if _, err := os.Stat(filepath.Join(instances["codex"], codex.ConfigFileName)); err != nil {
	// 	t.Fatalf("codex delivered no config.toml into its instance (%v); the invariant below would be vacuous", err)
	// }

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

// parked_engines: this whole test is codex's D2 "no controlled home"
// refusal half — internal/codex is out of the default build, so there is
// no codex.NewCodex() left to drive. Comes back with the package.
//
// func TestArch_RealHostHomesAreByteIdenticalWithNoControlledHome(t *testing.T) {
// 	resetArchStrictness(t)
// 	home := realHomeFixture(t)
// 	t.Setenv("OPENAI_API_KEY", "")
// 	workDir := t.TempDir()
//
// 	before := realHomeSnapshot(t, home)
//
// 	for _, configHome := range []string{"", agents.ConfigHomeHost} {
// 		env := operations.InTreeAgentHomeEnv(operations.InTreeAgentHome{
// 			Backend:    "codex",
// 			WorkDir:    workDir,
// 			Harp:       "ugly-icy-squid",
// 			ConfigHome: configHome,
// 			Policy:     isolation.None{},
// 		})
// 		if env != nil {
// 			t.Fatalf("config_home=%q: a run without an opt-in must be handed no config home, got %v", configHome, env)
// 		}
// 	}
//
// 	b := codex.NewCodex()
// 	// Setup's own error is not the assertion here (an unauthenticated run fails
// 	// loud from Execute); what matters is what it wrote.
// 	_ = b.Setup(context.Background(), &agent.SetupRequest{
// 		WorkDir:   workDir,
// 		Fragments: []*agent.Fragment{{Content: "project rules"}},
// 		CellKind:  agent.CellKindShared,
// 		Managed:   launchManaged(),
// 	})
//
// 	after := realHomeSnapshot(t, home)
// 	for leaf, want := range before {
// 		if after[leaf] != want {
// 			t.Errorf("a run with NO controlled home modified the user's real %s. "+
// 				"Keeping the real home means READING it, never writing it — codex's home-keyed surfaces must refuse "+
// 				"(loudly) rather than deliver there.", leaf)
// 		}
// 	}
//
// 	// Degraded, not silent: the cwd-keyed context surface still landed, which
// 	// is what distinguishes "refused with a warning" from "did nothing at all".
// 	if _, err := os.Stat(filepath.Join(workDir, codex.AgentsMDFile)); err != nil {
// 		t.Errorf("the cwd-keyed AGENTS.md surface must still deliver when the home-keyed ones refuse: %v", err)
// 	}
// }

// TestArch_InstanceHomesLiveInsideTheProjectStateTier is the other side of the
// same coin: wherever ctxloom DOES write an engine home, it is inside the
// project's gitignored state tier — never anywhere near the user's real home,
// and never in the cache tier a `deps pull` could clobber.
func TestArch_InstanceHomesLiveInsideTheProjectStateTier(t *testing.T) {
	const workDir = "/proj"
	const harp = "ugly-icy-squid"
	stateTier := filepath.Join(workDir, paths.AppDirName, "state") + string(filepath.Separator)

	claudeDir, err := claude.SessionConfigDir(workDir, harp)
	if err != nil {
		t.Fatalf("claude.SessionConfigDir: %v", err)
	}
	// parked_engines: kiro/codex rows are commented out with those
	// packages.
	// kiroDir, err := kiro.SessionHome(workDir, harp)
	// if err != nil {
	// 	t.Fatalf("kiro.SessionHome: %v", err)
	// }
	// codexRoot, err := codex.SessionHome(workDir, harp)
	// if err != nil {
	// 	t.Fatalf("codex.SessionHome: %v", err)
	// }

	for name, dir := range map[string]string{
		"claude-code": claudeDir,
		// "kiro":        kiroDir,
		// "codex":       filepath.Join(codexRoot, codex.ConfigDirName),
	} {
		if !strings.HasPrefix(dir, stateTier) {
			t.Errorf("%s's instance %q is not inside the project state tier %q", name, dir, stateTier)
		}
		if strings.Contains(dir, filepath.Join(".ctxloom", "cache")) {
			t.Errorf("%s's instance %q sits in the cache tier, which a rebuild may wipe and reconstruct", name, dir)
		}
	}
}
