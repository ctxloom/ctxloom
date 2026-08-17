package resources

import (
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestListBuiltinCommands(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}

	// Should have at least the discover command
	found := false
	for _, name := range names {
		if name == "discover" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'discover' command, got: %v", names)
	}
}

func TestGetBuiltinCommand(t *testing.T) {
	content, err := GetBuiltinCommand("discover")
	if err != nil {
		t.Fatalf("GetBuiltinCommand: %v", err)
	}

	// Should contain frontmatter with description
	if !strings.Contains(string(content), "description:") {
		t.Error("Expected description in frontmatter")
	}

	// Should contain the main content
	if !strings.Contains(string(content), "Scan the current project") {
		t.Error("Expected discover prompt content")
	}
}

// recover was migrated from a built-in slash command to an in-engine "resume"
// Agent Skill (WS-5, Decision 10/11): it depended on the runtime resume
// picker at startup (run.go), which no session-selection-free startup no
// longer has a place for. It now ships in the ctxloom-default remote bundle
// as a skill backed by recover_session/load_session/get_previous_session,
// not as a resources/commands/ builtin — see docs/backend-contract.md and the
// ctxloom-default repo's skills/resume/SKILL.md.
func TestGetBuiltinCommand_Unknown(t *testing.T) {
	_, err := GetBuiltinCommand("nope-no-such-command")
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

func TestListBuiltinCommands_OnlyMdFiles(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".md") {
			t.Errorf("ListBuiltinCommands must strip the .md extension; got %q", n)
		}
	}
}

func TestGetConfigSchema(t *testing.T) {
	data, err := GetConfigSchema()
	if err != nil {
		t.Fatalf("GetConfigSchema: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("config schema must be non-empty")
	}
	// Must be valid JSON shape — accept either '{' or whitespace+'{'.
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("config schema should look like JSON; got prefix %q", string(data[:32]))
	}
	// Should reference the standard JSON Schema draft URI.
	if !strings.Contains(string(data), "json-schema.org") {
		t.Error("config schema should reference json-schema.org")
	}
}

func TestGetExampleConfig(t *testing.T) {
	data, err := GetExampleConfig()
	if err != nil {
		t.Fatalf("GetExampleConfig: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("example config must be non-empty")
	}
}

// TestGetExampleConfig_MatchesOnDiskFile pins U155-F03 as REFUTED, not dead:
// the finding is that GetExampleConfig's only callers are tests
// (internal/schema/schema_test.go's "embedded example config is valid" and
// internal/config/arch_test.go's TestArch_ConfigSchema_ShippedConfigsValidate,
// both build-tagged `arch` or plain, in OTHER packages). Those are not
// throwaway reach-only tests -- they are the schema-drift regression gate for
// resources/example-config.yaml, the file cmd/validate also treats as a
// REQUIRED build-gate target (cmd/validate/main.go's defaultTargets). Reading
// it back out of the embedded FS (rather than a repo-relative os.ReadFile)
// is deliberate: a relative path assumes the process CWD is the repo root --
// exactly the fragility this campaign flags elsewhere (U003-F06, U009-F04) --
// while resourcesFS is baked in at compile time and is CWD-independent.
//
// This asserts the embed is faithful to its source file, which is what makes
// those two cross-package drift tests meaningful in the first place.
func TestGetExampleConfig_MatchesOnDiskFile(t *testing.T) {
	embedded, err := GetExampleConfig()
	if err != nil {
		t.Fatalf("GetExampleConfig: %v", err)
	}
	onDisk, err := os.ReadFile("example-config.yaml")
	if err != nil {
		t.Fatalf("read resources/example-config.yaml from disk: %v", err)
	}
	if string(embedded) != string(onDisk) {
		t.Fatal("GetExampleConfig's embedded bytes do not match resources/example-config.yaml on disk")
	}
}

func TestGetDefaultRemotes(t *testing.T) {
	data, err := GetDefaultRemotes()
	if err != nil {
		t.Fatalf("GetDefaultRemotes: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("default remotes must be non-empty")
	}
	// Sanity: should be YAML, should mention at least the ctxloom-default
	// remote name.
	if !strings.Contains(string(data), "ctxloom-default") {
		t.Error("default-remotes.yaml should declare the ctxloom-default remote")
	}
	// A remote carries NO trust flag anymore (signature-envelope spec §11):
	// trust is keyed to the publisher key, not the remote. The deleted
	// `trust_bundles` flag must not reappear — its presence would silently
	// restore the hash-blind source bypass this rework removed.
	if strings.Contains(string(data), "trust_bundles") {
		t.Error("default-remotes.yaml must NOT carry a trust_bundles flag — source trust is deleted")
	}
}

// TestGetBuiltinBundle_TaskloomDeleted proves taskloom is no longer an
// embedded builtin bundle (signature-envelope spec §4.3, S8): its content
// (fragment, hooks, MCP server) now ships from its own binary's loadout
// (`taskloom loadout --format json`, cmd/taskloom/loadout.yaml), discovered
// on PATH — see internal/config's ProbeCompanionLoadouts and
// TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated. This
// replaces the old TestGetBuiltinBundle, which asserted the OPPOSITE
// (taskloom.yaml present and non-empty) — that fixture is gone by design.
func TestGetBuiltinBundle_TaskloomDeleted(t *testing.T) {
	_, err := GetBuiltinBundle("taskloom")
	if err == nil {
		t.Fatal("taskloom.yaml must no longer be an embedded builtin bundle — it ships from its own loadout now")
	}
}

func TestGetBuiltinBundle_Unknown(t *testing.T) {
	_, err := GetBuiltinBundle("no-such-bundle")
	if err == nil {
		t.Fatal("expected error for unknown bundle, got nil")
	}
}

// TestListBuiltinBundles_OnlyIsolation pins the current embedded set. S8
// deleted the last two embedded bundles (ltk, taskloom) — their content now
// ships from the companions' own loadouts — leaving resources/builtin_bundles/
// with only a README (see its doc comment) until "isolation" became the first
// bundle that genuinely needs to ship compiled into the binary itself (no
// companion of its own). ListBuiltinBundles must report exactly that one name,
// and must still strip the .yaml extension for whatever it finds.
func TestListBuiltinBundles_OnlyIsolation(t *testing.T) {
	names, err := ListBuiltinBundles()
	if err != nil {
		t.Fatalf("ListBuiltinBundles: %v", err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".yaml") {
			t.Errorf("ListBuiltinBundles must strip the .yaml extension; got %q", n)
		}
	}
	if want := []string{"isolation"}; !reflect.DeepEqual(names, want) {
		t.Errorf("expected embedded builtin bundles %v, got %v", want, names)
	}
}

// TestGetPromptText verifies every embedded prompt template loads non-empty,
// trims its trailing newline, and that a missing name errors. These prompts
// back package-level vars via MustGetPromptText, so a renamed or unembedded
// file would otherwise only surface as an init-time panic at runtime.
// "profile-discovery" and "agent-setup" moved out of prompts/ entirely (their
// content merged into resources/commands/ctxloom-init.md's six-phase body,
// init-as-skill slice 3) — see TestGetBuiltinCommandBody_CtxloomInit below.
func TestGetPromptText(t *testing.T) {
	for _, name := range []string{
		"tooling",
		"distill-default",
		"mcp-server-instructions",
		"session-distill",
		"session-distill-reduce",
	} {
		got, err := GetPromptText(name)
		if err != nil {
			t.Errorf("GetPromptText(%q): %v", name, err)
			continue
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("GetPromptText(%q) is empty", name)
		}
		if strings.HasSuffix(got, "\n") {
			t.Errorf("GetPromptText(%q) has a trailing newline; want it trimmed", name)
		}
	}

	if _, err := GetPromptText("does-not-exist"); err == nil {
		t.Error("GetPromptText(missing) returned nil error")
	}
}

// TestSetupPrompts_ContentContract pins the tokens the collapsed init
// interview depends on: the tooling prompt must teach the gated Containerfile
// workflow. (The agent-setup/profile-discovery tokens moved into
// TestGetBuiltinCommandBody_CtxloomInit below, alongside the six-phase body.)
func TestSetupPrompts_ContentContract(t *testing.T) {
	tooling, err := GetPromptText("tooling")
	if err != nil {
		t.Fatalf("GetPromptText(tooling): %v", err)
	}
	for _, want := range []string{
		"ctxloom container scaffold",
		"ctxloom container build",
		"explicit approval",
		"Never apply tooling automatically",
	} {
		if !strings.Contains(tooling, want) {
			t.Errorf("tooling prompt lost required token %q", want)
		}
	}
}

// TestGetBuiltinCommandBody_CtxloomInit pins the contents of ctxloom's
// five-phase setup body (init-as-skill plan §4.3/§4.4, "the skill text is
// load-bearing"; CLI-primary reorg plan WS-3: ACP is no longer one of the
// phases — it moved out to the acp-setup Agent Skill, so init's working
// outcome is the CLI/TUI alone, never gated on ACP): every phase must be
// present, the agent-setup tokens the old TestSetupPrompts_ContentContract
// pinned must have survived the merge, and the body must carry NO literal
// "{{" (the mustache trap: fragment assembly silently blanks an unescaped
// "{{...}}", and the command-export path rewrites a bare "{{word}}" to a
// positional shell arg — either way, a stray "{{" here would corrupt every
// door this body reaches).
func TestGetBuiltinCommandBody_CtxloomInit(t *testing.T) {
	body, err := GetBuiltinCommandBody("ctxloom-init")
	if err != nil {
		t.Fatalf("GetBuiltinCommandBody(ctxloom-init): %v", err)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("ctxloom-init body is empty")
	}
	if strings.Contains(body, "{{") {
		t.Error("ctxloom-init body contains a literal \"{{\" — the mustache trap; escape or remove it")
	}

	for _, want := range []string{
		// The five phases, in order.
		"Phase 1", "Phase 2", "Phase 3", "Phase 4", "Phase 5",
		// ACP is optional, pointed at the acp-setup skill — never a gate here.
		"acp-setup",
		// Phase 4 (agents), carried over from the old agent-setup.md prompt.
		"SCAN → DISCUSS → SET",
		"coordinator",
		"developer",
		"finder",
		// The runtime axis is asked per agent and recorded explicitly —
		// `host|container` was the retired two-value spelling, and an
		// interview that stops asking is the regression this token guards.
		"--runtime container-rootless",
		"4b-runtime",
		"ctxloom llm list",
		"--workspace worktree",
		"ctxloom container check",
		"ctxloom agent create",
		"ctxloom agent list",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ctxloom-init body lost required token %q", want)
		}
	}
	for _, unwanted := range []string{
		// ACP is no longer a required exit criterion of init's phases.
		"Phase 2 — ACP client",
		"ACP client(s) — **required outcome**",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("ctxloom-init body still contains retired token %q — ACP moved to the acp-setup skill (WS-3)", unwanted)
		}
	}

	// Frontmatter must be stripped, not leaked into the composed prompt.
	if strings.Contains(body, "description:") {
		t.Error("ctxloom-init body still contains frontmatter; GetBuiltinCommandBody must strip it")
	}
}

// TestListBuiltinCommands_IncludesCtxloomInit pins that the setup body is
// discoverable as an ordinary builtin command (not just readable by name) —
// the same listing internal/lm/backends.builtinCommands walks to build every
// session's slash-command catalog.
func TestListBuiltinCommands_IncludesCtxloomInit(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}
	for _, n := range names {
		if n == "ctxloom-init" {
			return
		}
	}
	t.Errorf("ListBuiltinCommands missing \"ctxloom-init\"; got %v", names)
}

// TestEmbeddedFS_ExcludesGeneratedSchemas pins the half of U003-F05 that the
// deletion register already settled: `just gen-schemas` never prunes, so a
// renamed or deleted result type leaves its old schema in resources/schema/gen
// forever — but that directory is NOT embedded, so a stale file cannot reach a
// shipped binary. It was `all:schema` once, and reverting to that pattern is
// what re-opens the question; this test is red the moment it does.
//
// The schema/input assertion is not decoration: without it this would pass just
// as happily if the whole schema tree stopped being embedded.
func TestEmbeddedFS_ExcludesGeneratedSchemas(t *testing.T) {
	sawInput := false
	err := fs.WalkDir(resourcesFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(path, "schema/gen") {
			return fmt.Errorf("generated schema %q is embedded in the binary: schema/gen is a gitignored build artifact nothing reads back", path)
		}
		if !d.IsDir() && strings.HasPrefix(path, "schema/input/") {
			sawInput = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawInput {
		t.Fatal("no schema/input file is embedded — the hand-authored schemas GetSchema resolves have gone missing")
	}
}
