//go:build arch

// T18/T11: the missing architecture linter, and the six real
// import-cycle-shaped relationships this repo carries today.
//
// T11 found six pairs of packages that are only NOT a production import
// cycle because one direction of the relationship lives entirely in an
// external `_test` package (`coord_test`, `termui_test`, `transcript_test`,
// `agent_test`) rather than in any non-test file:
//
//	internal/cli/tui        -> internal/agentcoord/coord   (production)
//	internal/agentcoord/coord_test -> internal/cli/tui      (test-only)
//
//	internal/cli/tui        -> internal/termui              (production)
//	internal/termui_test     -> internal/cli/tui             (test-only)
//
//	internal/lm/grpc        -> internal/transcript           (production)
//	internal/transcript_test -> internal/lm/grpc             (test-only)
//
//	internal/claude            -> internal/shared/agent      (production)
//	internal/shared/agent_test -> internal/claude            (test-only)
//
// The Go compiler already refuses a real cycle, but only once BOTH edges
// exist in production code — which means the developer who adds the SECOND
// edge gets the signal, and the one who added the first (the actual
// direction-reversing change) got none. `go vet ./...` is clean and none of
// these four production dependencies has a production-file reverse edge
// today, so this is prevention, not repair — the six relationships stay this
// shape rather than closing into a real cycle.
//
// T18 separately found no depguard or architecture linter exists at all.
// Rather than stand up a second mechanism next to it, layeringRules extends
// the same scan()/pkg machinery arch_test.go and
// engine_identity_arch_test.go already use (TestArch_NonTestPackages_
// DoNotImportTestSupport, TestArch_Operations_DoesNotImportEnginePlugins):
// parse production imports once, walk a table of "packages under `from` may
// not import packages under `forbid`" rules, and permit named, reasoned
// exceptions the same way testSupportImporters does. A future rule — in
// particular T20's `cli/<flow> -> operations/<flow> -> domain, never
// cli/<flow> -> cli/<other-flow>` layering, once the per-flow package split
// lands — is one more entry in layeringRules, not a new gate.
// `operationsMustNotImportCLI` below is that rule's coarse ancestor: it is
// expressible today because the operations/cli boundary already exists even
// though the flow subdivision does not.
package arch

import (
	"sort"
	"strings"
	"testing"
)

// layeringRule states that no non-test file in a package under any prefix in
// `from` (the directory itself or any subdirectory) may import a package under
// any prefix in `forbid` (same subtree matching) unless that package is under
// a prefix in `except`. `except` exists for the ring-shaped rules: "core may
// import only core and the toolbox" is `forbid: every in-repo root, except:
// the core and toolbox prefixes`, which forbids a NEW package by default (the
// conservative direction) instead of leaving it unforbidden until someone
// lists it. `allowed` is this rule's shrinking allowlist, keyed by EDGE —
// "<from dir> -> <dep dir>" — mapped to the fix required to remove it, in
// the same spirit as arch_test.go's testSupportImporters and
// internal/cli's formatDebtAllowlist. An edge key rather than a package key
// is what makes the ratchet fine-grained: a package with five forbidden
// imports has five entries, each deleted (by TestArch_LayeringAllowlist_IsLive)
// the moment its own import leaves, instead of one entry that stays live —
// and keeps masking the other four — until the last of them goes. A
// nil/empty map means the rule holds with zero exceptions.
type layeringRule struct {
	name    string
	from    []string
	forbid  []string
	except  []string
	allowed map[string]string
}

// edgeKey is the allowed-map key for one import edge.
func edgeKey(from, dep string) string { return from + " -> " + dep }

// layeringRules is the one table both T11's cycle-prevention rules and
// T18's layering rule live in. Add a rule here; nothing else in this file
// needs to change.
var layeringRules = []layeringRule{
	{
		name:   "coord-must-not-import-cli/tui",
		from:   []string{"internal/agentcoord/coord"},
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "termui-must-not-import-cli/tui",
		from:   []string{"internal/termui"},
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "transcript-must-not-import-lm/grpc",
		from:   []string{"internal/transcript"},
		forbid: []string{"internal/lm/grpc"},
	},
	{
		name:   "shared/agent-must-not-import-engine-plugins",
		from:   []string{"internal/shared/agent"},
		forbid: []string{"internal/claude"},
	},
	{
		// The coarse ancestor of T20's future `cli/<flow> -> operations/<flow>
		// -> domain` rule: internal/operations is the frontend-agnostic layer
		// both the CLI and MCP call into, so it must never import back up
		// into internal/cli. When the per-flow split lands, this entry can
		// be replaced by one per flow (`internal/operations/<flow>` must not
		// import `internal/cli/<other-flow>`) without touching the mechanism.
		name:   "operations-must-not-import-cli",
		from:   []string{"internal/operations"},
		forbid: []string{"internal/cli"},
	},
	{
		// THE DECIDED ARCHITECTURE'S CORE RING (docs/architecture/audit-2026-09-18/
		// 30-decided-architecture.md, Part 1.0): the packages that become
		// internal/core/* import only each other and the toolbox. Until the
		// rename slice makes the rings directories, `from` and `except` name
		// today's paths one by one; after it, each collapses to a prefix.
		// `forbid` is every in-repo root, so anything that is neither core
		// nor toolbox is forbidden by default — a new package needs no row.
		// The allowlist is the MEASURED import list, one edge per entry,
		// each naming the slice in which it leaves; it is a ratchet, not a
		// claim (Part 0, invariant 9), and TestArch_LayeringAllowlist_IsLive
		// deletes an entry the moment its import is gone.
		name: "core-imports-only-core",
		from: []string{
			"internal/trust",
			"internal/sessions",
			"internal/profiles",
			"internal/bundles",
			"internal/config",
			"internal/paths",
			"internal/shared/wire",
			"internal/shared/agent",
			"internal/agentcoord/spool",
			"internal/agentcoord/coord",
			"internal/lm/engine",
		},
		forbid: []string{"cmd", "container", "internal", "pkg", "resources", "scripts"},
		except: []string{
			// core (the from-set again: core may import core)
			"internal/trust",
			"internal/sessions",
			"internal/profiles",
			"internal/bundles",
			"internal/config",
			"internal/paths",
			"internal/shared/wire",
			"internal/shared/agent",
			"internal/agentcoord/spool",
			"internal/agentcoord/coord",
			"internal/lm/engine",
			// the toolbox (Part 0: domain-free leaf libraries)
			"internal/shared/iox",
			"internal/shared/lockwait",
			"internal/shared/collections",
			"internal/shared/keymatch",
			"internal/shared/textutil",
			"internal/shared/yamlx",
			"internal/shared/realpath",
			"internal/shared/harp",
			"internal/shared/pidalive",
			"internal/errs",
			"internal/refuri",
			"internal/schema",
			"internal/liveness",
		},
		allowed: map[string]string{
			// core/trust
			"internal/trust -> internal/remote": "slice 2: URL normalisation already lives in refuri; the remote import goes",

			// core/sessions
			"internal/sessions -> internal/shared/upgrade": "slice 1a: the index migrations are deleted",
			"internal/sessions -> internal/shared/clidiag": "slice 15: clidiag becomes typed reports",

			// core/profiles — Part 1.0 lists remote and shared/agent; shared/agent is a
			// from-package here (its contract half becomes core/engine in 6b), so that
			// edge is not a violation under the prefix rule. The other three were
			// MEASURED, not listed.
			"internal/profiles -> internal/remote":            "slice 5: the pull-walk reader moves to adapters/remote",
			"internal/profiles -> internal/shared/clidiag":    "slice 15: clidiag becomes typed reports (measured; not in Part 1.0's profiles row)",
			"internal/profiles -> internal/shared/strictness": "slice 15: strictness becomes a value (measured; not in Part 1.0's profiles row)",
			"internal/profiles -> internal/shared/upgrade":    "slice 1a: the permanent migrations are deleted (measured; not in Part 1.0's profiles row)",
			"internal/profiles -> resources":                  "slice 5: the embedded builtin profiles are data a reader adapter supplies (measured; Part 1.0 does not classify resources)",

			// core/bundles
			"internal/bundles -> internal/content":            "slice 5: readers become adapters behind bundles.Reader",
			"internal/bundles -> internal/content/attest":     "slice 5: attest.VerifyBundle is called by the reader adapters",
			"internal/bundles -> internal/content/remotetree": "slice 5: readers become adapters behind bundles.Reader",
			"internal/bundles -> internal/remote":             "slice 5: readers become adapters behind bundles.Reader",
			"internal/bundles -> internal/signing":            "slice 5: one verifier, behind the trust ports",
			"internal/bundles -> internal/shared/admission":   "slice 5: admission is decided by composite.Trust, not by the bundle package",
			"internal/bundles -> internal/shared/upgrade":     "slice 1a: the permanent migrations are deleted",
			"internal/bundles -> internal/shared/clidiag":     "slice 15: clidiag becomes typed reports",
			"internal/bundles -> internal/shared/strictness":  "slice 15: strictness becomes a value (measured; not in Part 1.0's bundles row)",
			"internal/bundles -> resources":                   "slice 5: the embedded builtin bundles are data a reader adapter supplies (measured; Part 1.0 does not classify resources)",

			// core/config — Part 1.0 also lists config/layerscope, which is under the
			// from-prefix today and so not a violation until the rename moves it to
			// adapters/configload/layerscope.
			"internal/config -> internal/agents":                  "slice 4: the adapters/configload split",
			"internal/config -> internal/content":                 "slice 4: the adapters/configload split",
			"internal/config -> internal/content/remotetree":      "slice 4: the adapters/configload split",
			"internal/config -> internal/projectroot":             "slice 4: the adapters/configload split; config no longer finds its own root",
			"internal/config -> internal/remote":                  "slice 5: trust ports behind Sources.TrustPorts",
			"internal/config -> internal/shared/admission":        "slice 5: admission is decided by composite.Trust",
			"internal/config -> internal/shared/cliversion":       "slice 4: the adapters/configload split",
			"internal/config -> internal/shared/companionloadout": "slice 4: companion probing moves to adapters/companions",
			"internal/config -> internal/shared/confload":         "slice 4: the file/env/flag chain is adapters/configload's (measured; not in Part 1.0's config row)",
			"internal/config -> internal/signing":                 "slice 5: trust ports behind Sources.TrustPorts",
			"internal/config -> internal/signing/allowedsigners":  "slice 5: trust ports behind Sources.TrustPorts",
			"internal/config -> internal/shared/upgrade":          "slice 1a: the permanent migrations are deleted (measured; not in Part 1.0's config row)",
			"internal/config -> internal/shared/clidiag":          "slice 15: clidiag becomes typed reports",
			"internal/config -> internal/shared/strictness":       "slice 15: strictness becomes a value (measured; not in Part 1.0's config row)",
			"internal/config -> resources":                        "slice 4: the embedded default config is data adapters/configload supplies (measured; Part 1.0 does not classify resources)",

			// core/coord — Part 1.0 also lists shared/agent, a from-package here (see
			// profiles). envswitch is listed there without a slice.
			"internal/agentcoord/coord -> internal/agentcoord":           "slice 10: every generated-type reference re-typed on Go values; the proto goes to adapters/coordgrpc",
			"internal/agentcoord/coord -> internal/agentcoord/discover":  "slice 10: discover moves to adapters/coordgrpc",
			"internal/agentcoord/coord -> internal/agentcoord/mcpschema": "slice 10: mcpschema moves to adapters/coordgrpc",
			"internal/agentcoord/coord -> internal/agents":               "slice 8: harnessspec/SpawnPlan become core/launch types",
			"internal/agentcoord/coord -> internal/lm/isolation":         "slice 8: the isolation axes become core/launch value types",
			"internal/agentcoord/coord -> internal/operations":           "slice 8: operations.DirtyTreeHandler becomes launch.DirtyTreeHandler; operations implements coord.HostApp",
			"internal/agentcoord/coord -> internal/transcript":           "slice 14a: the engine-host files move to adapters/runner",
			"internal/agentcoord/coord -> internal/shared/envswitch":     "Part 1.0 lists this edge without a slice; it leaves with the engine host (14a), which is what reads the switched env",
			"internal/agentcoord/coord -> internal/shared/clidiag":       "slice 15: clidiag becomes typed reports",
			"internal/agentcoord/coord -> internal/shared/strictness":    "slice 15: strictness becomes a value",

			// coord/coordtest is the in-process runner double compiled into no binary;
			// Part 1.0 does not mention it. It stands up the real runner half, so it
			// imports what the runner imports until the runner is a package of its own.
			"internal/agentcoord/coord/coordtest -> internal/lm/backends":  "slice 14a: the double stands up adapters/runner instead of the backends seam (measured; Part 1.0 does not mention coordtest)",
			"internal/agentcoord/coord/coordtest -> internal/lm/isolation": "slice 14a: the double stands up adapters/runner instead of reaching isolation (measured; Part 1.0 does not mention coordtest)",

			// shared/agent → its contract half becomes core/engine. Part 1.0 also
			// lists lockwait and iox, which Part 0 names as toolbox; the toolbox is
			// excepted, so those two are not violations.
			"internal/shared/agent -> internal/shared/ledger":     "slice 12: shared/ledger is deleted",
			"internal/shared/agent -> internal/shared/clidiag":    "slice 15: clidiag becomes typed reports",
			"internal/shared/agent -> internal/shared/strictness": "slice 15: strictness becomes a value",

			// lm/engine → folded into core/engine. Part 1.0 also lists bundles, a
			// from-package here, so that edge is not a violation.
			"internal/lm/engine -> internal/engineversion":           "slice 6b: Descriptor becomes Definition; the version command is the engine's own",
			"internal/lm/engine -> internal/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values the adapter supplies",
		},
	},
	{
		// THE ADAPTER RING (Part 1.1): adapters import core; they do not import
		// each other or the engines. The two sanctioned edges — cli → operations
		// (the CLI is a pure frontend over the application services) and a
		// package's own subpackage (cli → cli/tui; runner → runner/mcp) — are
		// allowed entries with a reason that says "sanctioned", so IsLive still
		// confirms they exist. Today's adapter packages are named one by one;
		// the runner has no package of its own yet (the engine host lives
		// inside coord until slice 14a), so lm/grpc (the plugin wire) and mcp
		// (the MCP server, the future runner/mcp) stand for it. Every other
		// edge is MEASURED and leaves in the slice its reason names.
		name: "adapters-import-core-not-each-other",
		from: []string{
			"internal/cli",
			"internal/termui",
			"internal/operations",
			"internal/lm/grpc",
			"internal/lm/isolation",
			"internal/vpio",
			"internal/remote",
			"internal/shared/companionloadout",
			"internal/signing",
			"internal/content/attest",
			"internal/config/layerscope",
			"internal/transcript",
			"internal/memory",
			"internal/confpatch",
			"internal/mcp",
		},
		forbid: []string{
			// the adapters (the from-set again)
			"internal/cli",
			"internal/termui",
			"internal/operations",
			"internal/lm/grpc",
			"internal/lm/isolation",
			"internal/vpio",
			"internal/remote",
			"internal/shared/companionloadout",
			"internal/signing",
			"internal/content/attest",
			"internal/config/layerscope",
			"internal/transcript",
			"internal/memory",
			"internal/confpatch",
			"internal/mcp",
			// the engines
			"internal/claude",
			"internal/mockengine",
			"internal/lm/engines",
			"internal/lm/backends",
		},
		allowed: map[string]string{
			// sanctioned (Part 1.1): the CLI is a frontend over operations; a
			// package may import its own subpackage.
			"internal/cli -> internal/operations":                                         "sanctioned: cli → operations is one of the two adapter-to-adapter edges Part 0 keeps",
			"internal/cli -> internal/cli/tui":                                            "sanctioned: a package's own subpackage",
			"internal/signing -> internal/signing/allowedsigners":                         "sanctioned: a package's own subpackage",
			"internal/transcript/vendorreader/claude -> internal/transcript/vendorreader": "sanctioned: a package's own parent tree (transcript/*)",
			"internal/transcript/vendorreader/mock -> internal/transcript/vendorreader":   "sanctioned: a package's own parent tree (transcript/*)",
			"internal/transcript/vendorreader/claude -> internal/transcript":              "sanctioned: a package's own parent tree (transcript/*)",
			"internal/transcript/vendorreader/mock -> internal/transcript":                "sanctioned: a package's own parent tree (transcript/*)",
			"internal/transcript/vendorreader -> internal/transcript":                     "sanctioned: a package's own parent tree (transcript/*)",
			"internal/signing/countersign -> internal/signing":                            "sanctioned: a package's own parent tree (signing/*)",
			"internal/vpio/dockerexec -> internal/vpio":                                   "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/vpio/goplugin -> internal/vpio":                                     "slice 13: vpio/goplugin is deleted with the go-plugin protocol",

			// cli reaching past operations
			"internal/cli -> internal/claude":                  "slice 11b: engine packages are reached through engine.Registry, composed under cmd/*",
			"internal/cli -> internal/claude/engine":           "slice 11b: engine packages are reached through engine.Registry, composed under cmd/*",
			"internal/cli -> internal/lm/backends":             "slice 11b: lm/backends is deleted whole",
			"internal/cli -> internal/lm/engines":              "slice 11b: engines.Build() is called by the composition root, cmd/*",
			"internal/cli -> internal/lm/grpc":                 "slice 13: the go-plugin protocol is deleted whole",
			"internal/cli -> internal/lm/isolation":            "slice 7: the CLI hands launch.Resolve the axes; it stops reaching isolation",
			"internal/cli -> internal/mcp":                     "slice 9: the stdio MCP server is deleted; the endpoint lives in runner/mcp",
			"internal/cli -> internal/memory":                  "slice 14a: memory.NewCompactor(entry, source, llm) is called by operations.Compact",
			"internal/cli -> internal/remote":                  "slice 15: operations.ReviewWalk/ResolveLocalSigner take the orchestration out of the CLI",
			"internal/cli -> internal/signing":                 "slice 15: operations.ResolveLocalSigner takes the orchestration out of the CLI",
			"internal/cli -> internal/signing/agentkey":        "slice 15: operations.ResolveLocalSigner takes the orchestration out of the CLI",
			"internal/cli -> internal/termui":                  "slice 13: termui sits over the pty master the runner owns",
			"internal/cli -> internal/transcript":              "slice 13: cli/tui reads the transcript file; the CLI does not open transcripts itself",
			"internal/cli -> internal/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values",
			"internal/cli -> internal/vpio":                    "slice 13: adapters/hostpty and adapters/attach replace vpio; the CLI reaches them through operations",
			"internal/cli -> internal/vpio/dockerexec":         "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/cli -> internal/vpio/goplugin":           "slice 13: vpio/goplugin is deleted with the go-plugin protocol",
			"internal/cli -> internal/confpatch":               "slice 12: delivery.Ownership (adapters/confpatch) is reached through delivery, not from the CLI",

			// cli/tui and termui
			"internal/cli/tui -> internal/lm/grpc":    "slice 13: cli/tui sits on the coordination proto and the transcript file; the plugin wire is deleted",
			"internal/cli/tui -> internal/operations": "slice 13: the watch UI reads the coordination proto and the transcript file, not the application services",
			"internal/cli/tui -> internal/termui":     "slice 13: termui sits over the pty master; the TUI no longer composes it",
			"internal/termui -> internal/lm/grpc":     "slice 13: the go-plugin protocol is deleted whole",

			// operations reaching sibling adapters (it is the application-services
			// layer; it holds ports, not adapters)
			"internal/operations -> internal/content/attest":          "slice 5: attest.VerifyBundle is behind the trust ports composite.Trust holds",
			"internal/operations -> internal/lm/backends":             "slice 11b: lm/backends is deleted whole",
			"internal/operations -> internal/lm/grpc":                 "slice 13: the go-plugin protocol is deleted whole",
			"internal/operations -> internal/lm/isolation":            "slice 7: launch.Cells is the port; isolation is injected at cmd/*",
			"internal/operations -> internal/memory":                  "slice 14a: memory.NewCompactor(entry, source, llm); the compactor is injected",
			"internal/operations -> internal/remote":                  "slice 5: the pull-walk is behind composite.Transport / bundles.Reader",
			"internal/operations -> internal/signing":                 "slice 5: one verifier behind the trust ports",
			"internal/operations -> internal/signing/agentkey":        "slice 5: one verifier behind the trust ports",
			"internal/operations -> internal/signing/allowedsigners":  "slice 5: composite.SignerDecision is core-owned; the adapter is injected",
			"internal/operations -> internal/signing/countersign":     "slice 5: one signature (the .sigs/ manifest); countersigning goes",
			"internal/operations -> internal/transcript":              "slice 14a: sessions.Entry.NativeSession is the one record; transcript is an injected reader",
			"internal/operations -> internal/transcript/policy":       "slice 14a: transcript policy rides with the reader adapter",
			"internal/operations -> internal/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values",

			// the runner's two halves today
			"internal/lm/grpc -> internal/transcript":        "slice 13: the go-plugin protocol is deleted whole",
			"internal/lm/grpc -> internal/transcript/policy": "slice 13: the go-plugin protocol is deleted whole",
			"internal/mcp -> internal/lm/backends":           "slice 9: runner/mcp serves delivery.Dynamic; it holds no backend",
			"internal/mcp -> internal/lm/isolation":          "slice 9: runner/mcp serves delivery.Dynamic; the cell is resolved before it exists",
			"internal/mcp -> internal/memory":                "slice 14a: memory off the plugin; the compactor is an operation",
			"internal/mcp -> internal/operations":            "slice 8: host-relayed tools are Verbs.Host frames to coord.HostApp, which operations implements",
			"internal/mcp -> internal/transcript":            "slice 14a: the engine-host half of the runner records the transcript",

			// isolation, memory, and the leaf adapters
			"internal/lm/isolation -> internal/lm/grpc":                  "slice 13: the go-plugin protocol is deleted whole",
			"internal/memory -> internal/lm/backends":                    "slice 14a: memory off the plugin — NewCompactor(entry, source, llm)",
			"internal/memory -> internal/lm/grpc":                        "slice 14a: memory off the plugin — NewCompactor(entry, source, llm)",
			"internal/vpio/dockerexec -> internal/lm/isolation":          "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/vpio/goplugin -> internal/lm/grpc":                 "slice 13: vpio/goplugin is deleted with the go-plugin protocol",
			"internal/shared/companionloadout -> internal/signing":       "slice 4: adapters/companions probes; signing is reached through the trust ports",
			"internal/content/attest -> internal/signing":                "slice 5: attest.VerifyBundle is the one verifier over the signing adapter — a `must never know: each other` edge Part 1.1 does not resolve; measured",
			"internal/transcript/vendorreader/claude -> internal/claude": "slice 6b: the claude reader becomes an engine.TranscriptReader the engine package supplies",
		},
	},
	{
		// THE GENERATED COORDINATION PROTO (today internal/agentcoord itself;
		// adapters/coordgrpc/pb after the rename) is a wire codec's private
		// vocabulary: only the packages that speak the wire may import it. The
		// proto's sibling subpackages under internal/agentcoord are not the
		// proto, hence the except list — after the rename the proto is a leaf
		// and the list goes. from is the whole module so a new importer is
		// caught wherever it appears.
		name:   "proto-only-in-adapters",
		from:   []string{"cmd", "internal", "pkg"},
		forbid: []string{"internal/agentcoord"},
		except: []string{
			"internal/agentcoord/coord",
			"internal/agentcoord/discover",
			"internal/agentcoord/mcpschema",
			"internal/agentcoord/spool",
		},
		allowed: map[string]string{},
	},
	{
		// pkg/clifmt is the CLI output layer and SHIPS AS A STANDALONE
		// LIBRARY independent of ctxloom (ruled 2026-08-22). It is the
		// outermost edge: adapters import it, it imports nothing of ours.
		// External modules (cobra, and whatever a consumer brings) are
		// deliberately unchecked -- localDir returns "" for anything outside
		// this repo, so only in-repo imports reach this rule.
		name:   "clifmt-must-not-import-ctxloom",
		from:   []string{"pkg/clifmt"},
		forbid: []string{"internal", "cmd"},
	},
}

// underAny reports whether dir is one of the prefixes or inside its subtree.
func underAny(dir string, prefixes []string) bool {
	for _, p := range prefixes {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// matchesFrom reports whether dir is inside a from-subtree (or is one
// itself), and separately whether an import path resolves under any of the
// rule's forbidden subtrees; excepted whether it is nonetheless permitted by
// the rule's except list; violates composes the two.
func (r layeringRule) matchesFrom(dir string) bool { return underAny(dir, r.from) }

func (r layeringRule) matchesForbidden(dep string) bool {
	for _, f := range r.forbid {
		if dep == f || strings.HasPrefix(dep, f+"/") {
			return true
		}
	}
	return false
}

func (r layeringRule) excepted(dep string) bool { return underAny(dep, r.except) }

func (r layeringRule) violates(dep string) bool { return r.matchesForbidden(dep) && !r.excepted(dep) }

// TestArch_LayeringRules is the general layering gate: for every rule in
// layeringRules, no non-test file in a from-subtree package may import a
// forbidden-subtree package unless that directory is named in the rule's
// allowed map. Depending on genuinely shared packages outside both subtrees
// (config, paths, clifmt, shared/*) is unaffected — only imports that
// resolve under `forbid` are checked.
func TestArch_LayeringRules(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range layeringRules {
		rule := rule
		t.Run(rule.name, func(t *testing.T) {
			dirs := make([]string, 0, len(pkgs))
			for dir := range pkgs {
				if rule.matchesFrom(dir) {
					dirs = append(dirs, dir)
				}
			}
			sort.Strings(dirs)
			if len(dirs) == 0 {
				t.Fatalf("the scan found no package under %v — rule %q is looking at the wrong tree", rule.from, rule.name)
			}
			// Every prefix the rule names must match a real package: a
			// from/forbid/except entry that matches nothing is a path that
			// moved out from under the rule, and it would silently stop
			// guarding (or stop excepting) whatever it used to name.
			for _, group := range [][]string{rule.from, rule.forbid, rule.except} {
				for _, prefix := range group {
					if !anyPackageUnder(pkgs, prefix) {
						t.Errorf("rule %q names prefix %q, which matches no package in this module — delete or re-point it", rule.name, prefix)
					}
				}
			}

			for _, dir := range dirs {
				for _, ip := range pkgs[dir].imports {
					dep := localDir(ip)
					if dep == "" || !rule.violates(dep) {
						continue
					}
					if why, ok := rule.allowed[edgeKey(dir, dep)]; ok {
						t.Logf("allowed: %s imports %s (%s)", dir, ip, why)
						continue
					}
					t.Errorf("package %s imports %s, which layering rule %q forbids (packages under %v must not "+
						"import packages under %v). If this is a deliberate, reviewed exception, add %q to that "+
						"rule's allowed map in tests/arch/layering_test.go naming the fix required to remove it.",
						dir, ip, rule.name, rule.from, rule.forbid, edgeKey(dir, dep))
				}
			}
		})
	}
}

// anyPackageUnder reports whether the scan found a package at prefix or in
// its subtree.
func anyPackageUnder(pkgs map[string]*pkg, prefix string) bool {
	for dir := range pkgs {
		if dir == prefix || strings.HasPrefix(dir, prefix+"/") {
			return true
		}
	}
	return false
}

// TestArch_LayeringAllowlist_IsLive fails when a layeringRule's allowed map
// names an edge that no longer exists — the from-package is gone, is not
// under the rule's from-prefixes, no longer imports the dep, or the dep is no
// longer forbidden — the same staleness check
// TestArch_TestSupportAllowlist_IsLive runs for testSupportImporters. A
// stale exception is worse than none: left in place, it would silently
// cover whatever a later, unrelated import lands on that edge. This is the
// ratchet Part 1.0 of the decided architecture names: an allowlist entry is
// deleted in the slice that removes its import, and this test is what says
// so.
func TestArch_LayeringAllowlist_IsLive(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range layeringRules {
		keys := make([]string, 0, len(rule.allowed))
		for k := range rule.allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			from, dep, ok := strings.Cut(key, " -> ")
			if !ok {
				t.Errorf("rule %q allows %q, which is not an edge key (\"<from dir> -> <dep dir>\")", rule.name, key)
				continue
			}
			p, ok := pkgs[from]
			switch {
			case !ok:
				t.Errorf("rule %q allows %q, but %q is not a package in this module — delete the entry", rule.name, key, from)
				continue
			case !rule.matchesFrom(from):
				t.Errorf("rule %q allows %q, but %q is not under the rule's from-prefixes %v — delete the entry", rule.name, key, from, rule.from)
				continue
			case !rule.violates(dep):
				t.Errorf("rule %q allows %q, but %q is not something the rule forbids — delete the entry", rule.name, key, dep)
				continue
			}
			stillImports := false
			for _, ip := range p.imports {
				if localDir(ip) == dep {
					stillImports = true
					break
				}
			}
			if !stillImports {
				t.Errorf("rule %q allows %q (%s) but %s no longer imports %s — delete the entry, or it will silently "+
					"exempt that edge when it comes back", rule.name, key, rule.allowed[key], from, dep)
			}
		}
	}
}
