//go:build arch

// THE DECIDED ARCHITECTURE'S SYMBOL RULES (docs/architecture/audit-2026-09-18/
// 30-decided-architecture.md, Part 1.1): the invariants that are about WHO
// MAY NAME A SYMBOL rather than who may import a package, and so cannot be a
// layeringRule row. Each is an AST walk over the module's production files
// with a shrinking, reasoned allowlist in the same shape as the other symbol
// gates here (pathAuthorityAllowed, writeDisciplineAllowed): the day-one
// allowlist is the MEASURED set of sites, each naming the slice in which it
// leaves, and a twin *_AllowlistIsLive test deletes an exhausted entry.
//
//   - one-mint-one-owner: the session identity is minted in one place and the
//     process-wide owners (the coordinator, the config) are constructed only
//     by the composition root.
//   - no-engine-name-in-core: an engine's registered name is a literal only
//     where the engine lives, in config data, and in the init prompts that
//     write config data; everywhere else the core reads the engine's
//     declarations and never branches on its name.
//   - env-literals-once: the CTXLOOM_* environment keys are spelled once, in
//     the package that declares them, and the process environment (home,
//     cwd, temp, the current user) is read only by the composition root, the
//     project-root finder, the filesystem adapters and the leaf env
//     libraries — never by core, which is handed those facts as values.
//
// The family binaries (ltk, taskloom, their packages and internal/shared/
// tasks) are separate products that share the toolbox and are outside the
// rings (Part 0); so is internal/testsupport. The walk skips them.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// outsideRings are the module-relative prefixes the ring rules do not apply
// to: the family products and the test-only trees (testsupport and the
// tagged suites' harness code, which is ordinary .go only so several test
// packages can share it).
var outsideRings = []string{
	"cmd/ltk",
	"cmd/taskloom",
	"internal/ltk",
	"internal/taskloom",
	"internal/shared/tasks",
	"internal/testsupport",
	"tests",
}

// ringFile is one production file the ring walk parsed, with the
// module-relative path and directory the rules key on.
type ringFile struct {
	rel  string
	dir  string
	fset *token.FileSet
	f    *ast.File
}

// walkRingFiles parses every non-test .go file in the module outside
// outsideRings and hands each to fn. It fails rather than returning an
// error, and refuses a walk that saw too few files: a scan that quietly
// found nothing would make every rule built on it vacuous.
func walkRingFiles(t *testing.T, fn func(ringFile)) {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var filesScanned int

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && skippedDir(d.Name()):
			return filepath.SkipDir
		case d.IsDir():
			return nil
		case !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go"):
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if underAny(dir, outsideRings) {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		filesScanned++
		fn(ringFile{rel: rel, dir: dir, fset: fset, f: f})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if filesScanned < 200 {
		t.Fatalf("scanned only %d non-test files — the walk is broken, not the module", filesScanned)
	}
}

// ringSite is one place a ring rule found: the file and enclosing function
// (the durable key — the file alone for a rule that keys by file), what
// was found there, and the line for the message.
type ringSite struct {
	file   string
	symbol string
	what   string
	line   int
}

func (s ringSite) key() string {
	if s.symbol == "" {
		return s.file
	}
	return s.file + "#" + s.symbol
}

// sortSites orders sites by file then line so failures read top to bottom.
func sortSites(sites []ringSite) {
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
}

// checkRingAllowlist is the forward gate shared by the three rules: every
// site is either allowed (with its reason logged) or an error naming the
// key to add.
func checkRingAllowlist(t *testing.T, rule string, sites []ringSite, allowed map[string]string, remedy string) {
	t.Helper()
	sortSites(sites)
	for _, s := range sites {
		if why, ok := allowed[s.key()]; ok {
			t.Logf("allowed: %s:%d %s (%s)", s.file, s.line, s.what, why)
			continue
		}
		t.Errorf("%s:%d %s — %s. If this is a deliberate, reviewed exception, add %q to the %s "+
			"allowlist in tests/arch/ring_symbols_test.go naming the slice in which it leaves.",
			s.file, s.line, s.what, remedy, s.key(), rule)
	}
}

// checkRingAllowlistIsLive is the staleness twin: every allowlist key must
// still be a site the walk finds, or the entry would silently exempt
// whatever lands at that key next.
func checkRingAllowlistIsLive(t *testing.T, rule string, sites []ringSite, allowed map[string]string) {
	t.Helper()
	live := make(map[string]bool, len(sites))
	for _, s := range sites {
		live[s.key()] = true
	}
	if len(allowed) == 0 {
		t.Logf("the %s allowlist is empty: only the forward gate is doing live work", rule)
		return
	}
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !live[k] {
			t.Errorf("the %s allowlist allows %q (%s) but the walk finds nothing there anymore — delete the "+
				"entry, or it will silently exempt whatever lands at that key next", rule, k, allowed[k])
		}
	}
}

// selectorCall reports whether call is `pkg.Name(...)` for the given
// package identifier and selector name.
func selectorCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg
}

// ---------------------------------------------------------------------------
// one-mint-one-owner
// ---------------------------------------------------------------------------

// pinnedCall is one symbol the one-mint-one-owner rule pins: how to
// recognise a call to it, and the directories in which the call is the
// rule's own sanctioned site.
type pinnedCall struct {
	what      string
	match     func(*ast.CallExpr) bool
	permitted []string
}

// pinnedCalls are today's spellings of the three symbols Part 1.1 pins.
// sessions.Mint does not exist yet (slice 2 introduces it): today the
// session identity is minted by the sessions.Store's AssignHarp through the
// harp allocator, so both are pinned; their sanctioned callers are the
// store itself, operations (StartRun's home) and the harp CLI, which mints
// names, not sessions. config.Open is today's config.Load. coord.New is
// already the one constructor.
var pinnedCalls = []pinnedCall{
	{
		what: "mints a session identity (AssignHarp / harp.Generate*)",
		match: func(c *ast.CallExpr) bool {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AssignHarp" {
				return true
			}
			return selectorCall(c, "harp", "GenerateName") || selectorCall(c, "harp", "GenerateNameWithOptions") ||
				selectorCall(c, "harp", "GenerateShortName") || selectorCall(c, "harp", "UniqueFrom")
		},
		permitted: []string{"internal/sessions", "internal/operations", "internal/shared/harp", "cmd/harp"},
	},
	{
		what:      "constructs the coordinator (coord.New)",
		match:     func(c *ast.CallExpr) bool { return selectorCall(c, "coord", "New") },
		permitted: []string{"cmd"},
	},
	{
		what:      "opens the config (config.Load, today's config.Open)",
		match:     func(c *ast.CallExpr) bool { return selectorCall(c, "config", "Load") },
		permitted: []string{"cmd"},
	},
}

// oneMintOneOwnerAllowed is the rule's shrinking allowlist: "file.go#Func"
// mapped to the slice in which the site leaves.
var oneMintOneOwnerAllowed = map[string]string{
	// the second mint: agent_run makes the child's harp itself instead of
	// asking the store
	"internal/mcp/mcp_tools_agents.go#selfIdentityFromEnv": "slice 2 introduces sessions.Mint; coord.Coordinator.AgentRun calls it and the MCP server stops minting",

	// the coordinator is constructed by the MCP server, not the composition root
	"internal/mcp/coord_host.go#NewHostedCoordinator": "Part 1.1 one-mint-one-owner: coord.New moves under cmd/*; Part 4.1 names no slice for the move (measured)",

	// config.Load in the CLI: slice 4 gives operations.App the one
	// config.Owner and the CLI stops opening the config itself
	"internal/cli/agent.go#completeAgentNames":                  "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/clean_cmd.go#sessionReapCutoff":               "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/completion.go#completeFragmentNames":          "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/completion.go#completeLLMNames":               "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/completion.go#completeProfileNames":           "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/completion.go#completePromptNames":            "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/completion.go#completeTagNames":               "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/hook_hud.go#gatherCtxloomInfo":                "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/hook_inject_context.go#agentSetupNudge":       "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/hook_skill_mates.go#skillMatesOutput":         "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/init.go#addPersonalRemotes":                   "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/init.go#applyInitHooks":                       "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/init.go#cloneConfiguredRemotes":               "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/init.go#engineForExistingDir":                 "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/init.go#pullSeededDependencies":               "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/init.go#setupNewCtxloomDir":                   "slice 4: init writes config data through a Draft; the Owner is operations.App's",
	"internal/cli/llm_runner_common.go#loadAndConfigureBackend": "slice 4: one Reload per spawn, owned by operations.App",
	"internal/cli/run.go#runState.loadConfig":                   "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/session_cmd.go#runSessionDistill":             "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/session_cmd.go#sessionAppDir":                 "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/session_distill.go#distillMissingOrStale":     "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",
	"internal/cli/session_query.go#runSessionQuery":             "slice 4: operations.App holds the config.Owner; the CLI reads a Snapshot",

	// config.Load inside operations: the memoized loader each service opens
	// for itself becomes the one Owner the App is constructed with
	"internal/operations/hooks.go#resolveHookConfig":       "slice 4: operations.App is constructed with the one config.Owner; services read its Snapshot",
	"internal/operations/llm.go#SetLLM":                    "slice 4: operations.App is constructed with the one config.Owner; SetLLM writes a Draft",
	"internal/operations/mcp_servers.go#resolveListConfig": "slice 4: operations.App is constructed with the one config.Owner; services read its Snapshot",
	"internal/operations/sessionfeed.go#WatchSessionFeed":  "slice 4: operations.App is constructed with the one config.Owner; services read its Snapshot",
}

func scanOneMintOneOwner(t *testing.T) []ringSite {
	t.Helper()
	var out []ringSite
	var pinnedSeen int
	walkRingFiles(t, func(rf ringFile) {
		for _, decl := range rf.f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for _, p := range pinnedCalls {
					if !p.match(call) {
						continue
					}
					pinnedSeen++
					if underAny(rf.dir, p.permitted) {
						continue
					}
					out = append(out, ringSite{file: rf.rel, symbol: funcSymbol(fd), what: "(" + funcSymbol(fd) + ") " + p.what, line: rf.fset.Position(call.Pos()).Line})
				}
				return true
			})
		}
	})
	if pinnedSeen == 0 {
		t.Fatal("the walk found no call to any pinned symbol anywhere — the spellings in pinnedCalls are stale, not the module clean")
	}
	return out
}

// TestArch_OneMintOneOwner is the gate: outside its sanctioned directories,
// no production function mints a session identity, constructs the
// coordinator or opens the config unless named in oneMintOneOwnerAllowed.
func TestArch_OneMintOneOwner(t *testing.T) {
	checkRingAllowlist(t, "one-mint-one-owner", scanOneMintOneOwner(t), oneMintOneOwnerAllowed,
		"Part 1.1: the identity is minted once (sessions.Mint) and the process-wide owners are built by the composition root")
}

func TestArch_OneMintOneOwner_AllowlistIsLive(t *testing.T) {
	checkRingAllowlistIsLive(t, "one-mint-one-owner", scanOneMintOneOwner(t), oneMintOneOwnerAllowed)
}

// ---------------------------------------------------------------------------
// no-engine-name-in-core
// ---------------------------------------------------------------------------

// engineNameHomes are the directories where an engine's registered name may
// be a string literal: the engine packages themselves (today's spellings;
// internal/engines/* after the rename) and the mock engine's binary.
var engineNameHomes = []string{
	"internal/claude",
	"internal/mockengine",
	"internal/lm/engines",
	"internal/lm/backends",
	"cmd/mockengine",
}

// engineNameInitPrompt reports whether rel is one of the init prompts that
// WRITE config data (Part 1.1 permits the literal there, until init chooses
// its default from engine.Registry.Names instead).
func engineNameInitPrompt(rel string) bool {
	return filepath.Dir(rel) == "internal/cli" && strings.HasPrefix(filepath.Base(rel), "init")
}

// noEngineNameInCoreAllowed is the rule's shrinking allowlist, keyed by
// FILE (the brief's granularity for a literal rule: a file either spells
// the name or it does not), mapped to the slice in which the spelling
// leaves.
var noEngineNameInCoreAllowed = map[string]string{
	// core packages that name the default engine
	"internal/config/config_types.go":            "slice 6b: Config.Validate(engine.Registry) checks a configured name against the registry; no default is a literal in core",
	"internal/bundles/tree_read.go":              "slice 6: bundles.LLMExports become opaque map[string]json.RawMessage keyed by whatever the registry names; no engine key is spelled here",
	"internal/memory/compactor.go":               "slice 14a: memory.NewCompactor(entry, source, llm) is handed its engine; the compactor does not default one",
	"internal/memory/distill.go":                 "slice 14a: memory.NewCompactor(entry, source, llm) is handed its engine; the compactor does not default one",
	"internal/operations/profile_materialize.go": "slice 12: materialize takes the engine from the Target; no default is a literal in the application services",

	// adapters and the CLI choosing a default by name
	"internal/cli/config.go":              "slice 6b: the CLI's default is engine.Registry.Names(default-distribution), not a literal",
	"internal/cli/manage.go":              "slice 6b: the CLI's default is engine.Registry.Names(default-distribution), not a literal",
	"internal/content/convert/convert.go": "slice 6: the per-engine export fields become opaque; the converter keys on the registry's names",
	"internal/tmuxhost/paneinject.go":     "slice 13: hostpty spawns the runner; the pane-injection table keyed by engine name goes with tmuxhost",

	// the retiring plugin wire and the vendor readers
	"internal/lm/grpc/mock_client.go":                   "slice 13: the go-plugin protocol is deleted whole",
	"internal/transcript/vendorreader/claude/locate.go": "slice 6b: the reader becomes an engine.TranscriptReader the engine package supplies, which knows its own name",
	"internal/transcript/vendorreader/mock/mock.go":     "slice 6b: the reader becomes an engine.TranscriptReader the engine package supplies, which knows its own name",
}

// scanEngineNameLiterals finds every string literal equal to a registered
// engine name outside the engine homes and the init prompts. The names are
// read from the live registry (composed by TestMain), never listed here.
func scanEngineNameLiterals(t *testing.T) []ringSite {
	t.Helper()
	names := backends.List()
	if len(names) == 0 {
		t.Fatal("backends.List() returned nothing — the registry did not populate; the rule has nothing to look for")
	}
	isName := make(map[string]bool, len(names))
	for _, n := range names {
		isName[n] = true
	}

	var out []ringSite
	seen := map[string]bool{}
	walkRingFiles(t, func(rf ringFile) {
		if underAny(rf.dir, engineNameHomes) || engineNameInitPrompt(rf.rel) {
			return
		}
		ast.Inspect(rf.f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			v, ok := vocabStringLit(e)
			if !ok || !isName[v] {
				return true
			}
			// One site per file: the key is the file, so a second literal in
			// the same file is the same finding.
			if seen[rf.rel] {
				return true
			}
			seen[rf.rel] = true
			out = append(out, ringSite{file: rf.rel, what: "spells the engine name " + strconv.Quote(v), line: rf.fset.Position(n.Pos()).Line})
			return true
		})
	})
	return out
}

// TestArch_NoEngineNameInCore is the gate: a registered engine name is a
// string literal only where the engine lives, in config data and in the
// init prompts that write config data.
func TestArch_NoEngineNameInCore(t *testing.T) {
	checkRingAllowlist(t, "no-engine-name-in-core", scanEngineNameLiterals(t), noEngineNameInCoreAllowed,
		"Part 1.1: the core reads the engine's declarations and never branches on its name")
}

func TestArch_NoEngineNameInCore_AllowlistIsLive(t *testing.T) {
	checkRingAllowlistIsLive(t, "no-engine-name-in-core", scanEngineNameLiterals(t), noEngineNameInCoreAllowed)
}

// ---------------------------------------------------------------------------
// env-literals-once
// ---------------------------------------------------------------------------

// envKeysDeclaringDir is the package that declares the CTXLOOM_* environment
// keys the runner reads (today internal/agentcoord/coord; core/sessions
// after slice 2). The keys themselves are READ from its package-level
// consts, never listed here: a key added there is covered the moment it is
// declared.
const envKeysDeclaringDir = "internal/agentcoord/coord"

// envReadHomes are the directories that may spell those keys or read the
// process environment (home, cwd, temp, the current user): the declaring
// package, the composition roots, the project-root finder and the leaf env
// libraries. The filesystem adapters Part 1.1 also permits do not exist yet.
var envReadHomes = []string{
	envKeysDeclaringDir,
	"cmd",
	"internal/projectroot",
	"internal/shared/shellenv",
	"internal/shared/envswitch",
}

// envReadCalls are the process-environment reads Part 1.1 names.
var envReadCalls = [][2]string{
	{"os", "UserHomeDir"},
	{"os", "UserConfigDir"},
	{"os", "Getwd"},
	{"os", "TempDir"},
	{"os", "MkdirTemp"},
	{"user", "Current"},
}

// envLiteralsOnceAllowed is the rule's shrinking allowlist, keyed by FILE,
// mapped to the slice in which the site leaves.
var envLiteralsOnceAllowed = map[string]string{}

// declaredEnvKeys collects the CTXLOOM_* string values of the package-level
// consts declared in envKeysDeclaringDir.
func declaredEnvKeys(t *testing.T, files []ringFile) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, rf := range files {
		for _, decl := range rf.f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, val := range vs.Values {
					if v, ok := vocabStringLit(val); ok && strings.HasPrefix(v, "CTXLOOM_") {
						keys[v] = true
					}
				}
			}
		}
	}
	if len(keys) == 0 {
		t.Fatalf("no CTXLOOM_* const is declared under %s — envKeysDeclaringDir is stale, not the module clean", envKeysDeclaringDir)
	}
	return keys
}

// scanEnvLiterals finds, outside envReadHomes, every string literal equal to
// a declared CTXLOOM_* key and every call to an envReadCalls function. One
// site per file: the key is the file.
func scanEnvLiterals(t *testing.T) []ringSite {
	t.Helper()
	var all []ringFile
	walkRingFiles(t, func(rf ringFile) { all = append(all, rf) })

	var declaring []ringFile
	for _, rf := range all {
		if rf.dir == envKeysDeclaringDir {
			declaring = append(declaring, rf)
		}
	}
	keys := declaredEnvKeys(t, declaring)

	var out []ringSite
	var readsSeen int
	for _, rf := range all {
		if underAny(rf.dir, envReadHomes) {
			continue
		}
		var first *ringSite
		note := func(n ast.Node, what string) {
			if first != nil {
				return
			}
			first = &ringSite{file: rf.rel, what: what, line: rf.fset.Position(n.Pos()).Line}
		}
		ast.Inspect(rf.f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				for _, c := range envReadCalls {
					if selectorCall(node, c[0], c[1]) {
						readsSeen++
						note(n, "reads the process environment ("+c[0]+"."+c[1]+")")
					}
				}
			case *ast.BasicLit:
				if v, ok := vocabStringLit(node); ok && keys[v] {
					note(n, "re-spells the environment key "+strconv.Quote(v)+" declared in "+envKeysDeclaringDir)
				}
			}
			return true
		})
		if first != nil {
			out = append(out, *first)
		}
	}
	if readsSeen == 0 {
		t.Fatal("the walk found no process-environment read anywhere outside envReadHomes — envReadCalls' spellings are stale, not the module clean")
	}
	return out
}

// TestArch_EnvLiteralsOnce is the gate: the CTXLOOM_* keys are spelled once
// and the process environment is read only where Part 1.1 says.
func TestArch_EnvLiteralsOnce(t *testing.T) {
	checkRingAllowlist(t, "env-literals-once", scanEnvLiterals(t), envLiteralsOnceAllowed,
		"Part 1.1: core is handed home, cwd and identity as values; only the composition root, projectroot, the fs adapters and the leaf env libraries read the environment")
}

func TestArch_EnvLiteralsOnce_AllowlistIsLive(t *testing.T) {
	checkRingAllowlistIsLive(t, "env-literals-once", scanEnvLiterals(t), envLiteralsOnceAllowed)
}
