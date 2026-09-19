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
	"strings"
	"testing"
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
// (the durable key), what was found there, and the line for the message.
type ringSite struct {
	file   string
	symbol string
	what   string
	line   int
}

func (s ringSite) key() string { return s.file + "#" + s.symbol }

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
		t.Errorf("%s:%d (%s) %s — %s. If this is a deliberate, reviewed exception, add %q to the %s "+
			"allowlist in tests/arch/ring_symbols_test.go naming the slice in which it leaves.",
			s.file, s.line, s.symbol, s.what, remedy, s.key(), rule)
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
					out = append(out, ringSite{file: rf.rel, symbol: funcSymbol(fd), what: p.what, line: rf.fset.Position(call.Pos()).Line})
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
