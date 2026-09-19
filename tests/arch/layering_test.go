//go:build arch

// T18/T11: the missing architecture linter, and the six real
// import-cycle-shaped relationships this repo carries today.
//
// T11 found six pairs of packages that are only NOT a production import
// cycle because one direction of the relationship lives entirely in an
// external `_test` package (`coord_test`, `termui_test`, `transcript_test`,
// `agent_test`) rather than in any non-test file:
//
//	internal/adapters/cli/tui        -> internal/core/coord   (production)
//	internal/adapters/coordgrpc/pb/coord_test -> internal/adapters/cli/tui      (test-only)
//
//	internal/adapters/cli/tui        -> internal/adapters/termui              (production)
//	internal/termui_test     -> internal/adapters/cli/tui             (test-only)
//
//	internal/lm/grpc        -> internal/adapters/transcript           (production)
//	internal/transcript_test -> internal/lm/grpc             (test-only)
//
//	internal/engines/claude            -> internal/core/agent      (production)
//	internal/shared/agent_test -> internal/engines/claude            (test-only)
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
// internal/adapters/cli's formatDebtAllowlist. An edge key rather than a package key
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
		from:   []string{"internal/core/coord"},
		forbid: []string{"internal/adapters/cli/tui"},
	},
	{
		name:   "termui-must-not-import-cli/tui",
		from:   []string{"internal/adapters/termui"},
		forbid: []string{"internal/adapters/cli/tui"},
	},
	{
		name:   "transcript-must-not-import-lm/grpc",
		from:   []string{"internal/adapters/transcript"},
		forbid: []string{"internal/lm/grpc"},
	},
	{
		name:   "shared/agent-must-not-import-engine-plugins",
		from:   []string{"internal/core/agent"},
		forbid: []string{"internal/engines/claude"},
	},
	{
		// The coarse ancestor of T20's future `cli/<flow> -> operations/<flow>
		// -> domain` rule: internal/adapters/operations is the frontend-agnostic layer
		// both the CLI and MCP call into, so it must never import back up
		// into internal/adapters/cli. When the per-flow split lands, this entry can
		// be replaced by one per flow (`internal/adapters/operations/<flow>` must not
		// import `internal/adapters/cli/<other-flow>`) without touching the mechanism.
		name:   "operations-must-not-import-cli",
		from:   []string{"internal/adapters/operations"},
		forbid: []string{"internal/adapters/cli"},
	},
	{
		// THE DECIDED ARCHITECTURE'S CORE RING (docs/architecture/audit-2026-09-18/
		// 30-decided-architecture.md, Part 1.0): the packages that become
		// internal/core/* import only each other and the toolbox. The rename
		// slice made the ring a directory, so `from` and `except` are the
		// prefix. `forbid` is every in-repo root, so anything that is neither core
		// nor toolbox is forbidden by default — a new package needs no row.
		// The allowlist is the MEASURED import list, one edge per entry,
		// each naming the slice in which it leaves; it is a ratchet, not a
		// claim (Part 0, invariant 9), and TestArch_LayeringAllowlist_IsLive
		// deletes an entry the moment its import is gone.
		name: "core-imports-only-core",
		from: []string{
			"internal/core",
			// measured core, retired in place (slice 6b): outside the prefix
			// until it is deleted, so it is named on its own.
			"internal/lm/engine",
		},
		forbid: []string{"cmd", "container", "internal", "pkg", "resources", "scripts"},
		except: []string{
			// core (the from-set again: core may import core)
			"internal/core",
			"internal/lm/engine",
			// the toolbox (Part 0: domain-free leaf libraries), listed by
			// member rather than as the internal/shared prefix: a package
			// that merely sits under that directory is not thereby a
			// toolbox member core may reach.
			"internal/shared/iox",
			"internal/shared/lockwait",
			"internal/shared/collections",
			"internal/shared/keymatch",
			"internal/shared/textutil",
			"internal/shared/yamlx",
			"internal/shared/realpath",
			"internal/shared/harp",
			"internal/shared/pidalive",
			"internal/shared/errs",
			"internal/shared/refuri",
			"internal/shared/schema",
			"internal/shared/liveness",
		},
		allowed: map[string]string{
			// core/trust
			"internal/core/trust -> internal/adapters/remote": "slice 2: URL normalisation already lives in refuri; the remote import goes",

			// core/sessions
			"internal/core/sessions -> internal/shared/upgrade": "slice 1a: the index migrations are deleted",
			"internal/core/sessions -> internal/shared/clidiag": "slice 15: clidiag becomes typed reports",

			// core/profiles — Part 1.0 lists remote and shared/agent; shared/agent is a
			// from-package here (its contract half becomes core/engine in 6b), so that
			// edge is not a violation under the prefix rule. The other three were
			// MEASURED, not listed.
			"internal/core/profiles -> internal/adapters/remote":   "slice 5: the pull-walk reader moves to adapters/remote",
			"internal/core/profiles -> internal/shared/clidiag":    "slice 15: clidiag becomes typed reports (measured; not in Part 1.0's profiles row)",
			"internal/core/profiles -> internal/shared/strictness": "slice 15: strictness becomes a value (measured; not in Part 1.0's profiles row)",
			"internal/core/profiles -> internal/shared/upgrade":    "slice 1a: the permanent migrations are deleted (measured; not in Part 1.0's profiles row)",
			"internal/core/profiles -> resources":                  "slice 5: the embedded builtin profiles are data a reader adapter supplies (measured; Part 1.0 does not classify resources)",

			// core/bundles
			"internal/core/bundles -> internal/adapters/content":            "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/content/attest":     "slice 5: attest.VerifyBundle is called by the reader adapters",
			"internal/core/bundles -> internal/adapters/content/remotetree": "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/remote":             "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/signing":            "slice 5: one verifier, behind the trust ports",
			"internal/core/bundles -> internal/shared/admission":            "slice 5: admission is decided by composite.Trust, not by the bundle package",
			"internal/core/bundles -> internal/shared/upgrade":              "slice 1a: the permanent migrations are deleted",
			"internal/core/bundles -> internal/shared/clidiag":              "slice 15: clidiag becomes typed reports",
			"internal/core/bundles -> internal/shared/strictness":           "slice 15: strictness becomes a value (measured; not in Part 1.0's bundles row)",
			"internal/core/bundles -> resources":                            "slice 5: the embedded builtin bundles are data a reader adapter supplies (measured; Part 1.0 does not classify resources)",

			// core/config — Part 1.0 also lists config/layerscope, which is under the
			// from-prefix today and so not a violation until the rename moves it to
			// adapters/configload/layerscope.
			"internal/core/config -> internal/adapters/agents":                 "slice 4: the adapters/configload split",
			"internal/core/config -> internal/adapters/content":                "slice 4: the adapters/configload split",
			"internal/core/config -> internal/adapters/content/remotetree":     "slice 4: the adapters/configload split",
			"internal/core/config -> internal/adapters/projectroot":            "slice 4: the adapters/configload split; config no longer finds its own root",
			"internal/core/config -> internal/adapters/remote":                 "slice 5: trust ports behind Sources.TrustPorts",
			"internal/core/config -> internal/shared/admission":                "slice 5: admission is decided by composite.Trust",
			"internal/core/config -> internal/shared/cliversion":               "slice 4: the adapters/configload split",
			"internal/core/config -> internal/adapters/configload/layerscope":  "slice 4: the adapters/configload split; layerscope is configload's",
			"internal/core/config -> internal/adapters/companions":             "slice 4: companion probing moves to adapters/companions",
			"internal/core/config -> internal/shared/confload":                 "slice 4: the file/env/flag chain is adapters/configload's (measured; not in Part 1.0's config row)",
			"internal/core/config -> internal/adapters/signing":                "slice 5: trust ports behind Sources.TrustPorts",
			"internal/core/config -> internal/adapters/signing/allowedsigners": "slice 5: trust ports behind Sources.TrustPorts",
			"internal/core/config -> internal/shared/upgrade":                  "slice 1a: the permanent migrations are deleted (measured; not in Part 1.0's config row)",
			"internal/core/config -> internal/shared/clidiag":                  "slice 15: clidiag becomes typed reports",
			"internal/core/config -> internal/shared/strictness":               "slice 15: strictness becomes a value (measured; not in Part 1.0's config row)",
			"internal/core/config -> resources":                                "slice 4: the embedded default config is data adapters/configload supplies (measured; Part 1.0 does not classify resources)",

			// core/coord — Part 1.0 also lists shared/agent, a from-package here (see
			// profiles). envswitch is listed there without a slice.
			"internal/core/coord -> internal/adapters/coordgrpc/pb":        "slice 10: every generated-type reference re-typed on Go values; the proto goes to adapters/coordgrpc",
			"internal/core/coord -> internal/agentcoord/discover":          "slice 10: discover moves to adapters/coordgrpc",
			"internal/core/coord -> internal/adapters/coordgrpc/mcpschema": "slice 10: mcpschema moves to adapters/coordgrpc",
			"internal/core/coord -> internal/adapters/agents":              "slice 8: harnessspec/SpawnPlan become core/launch types",
			"internal/core/coord -> internal/adapters/isolation":           "slice 8: the isolation axes become core/launch value types",
			"internal/core/coord -> internal/adapters/operations":          "slice 8: operations.DirtyTreeHandler becomes launch.DirtyTreeHandler; operations implements coord.HostApp",
			"internal/core/coord -> internal/adapters/transcript":          "slice 14a: the engine-host files move to adapters/runner",
			"internal/core/coord -> internal/shared/envswitch":             "Part 1.0 lists this edge without a slice; it leaves with the engine host (14a), which is what reads the switched env",
			"internal/core/coord -> internal/shared/clidiag":               "slice 15: clidiag becomes typed reports",
			"internal/core/coord -> internal/shared/strictness":            "slice 15: strictness becomes a value",

			// coord/coordtest is the in-process runner double compiled into no binary;
			// Part 1.0 does not mention it. It stands up the real runner half, so it
			// imports what the runner imports until the runner is a package of its own.
			"internal/core/coord/coordtest -> internal/lm/backends":        "slice 14a: the double stands up adapters/runner instead of the backends seam (measured; Part 1.0 does not mention coordtest)",
			"internal/core/coord/coordtest -> internal/adapters/isolation": "slice 14a: the double stands up adapters/runner instead of reaching isolation (measured; Part 1.0 does not mention coordtest)",

			// shared/agent → its contract half becomes core/engine. Part 1.0 also
			// lists lockwait and iox, which Part 0 names as toolbox; the toolbox is
			// excepted, so those two are not violations.
			"internal/core/agent -> internal/shared/ledger":     "slice 12: shared/ledger is deleted",
			"internal/core/agent -> internal/shared/clidiag":    "slice 15: clidiag becomes typed reports",
			"internal/core/agent -> internal/shared/strictness": "slice 15: strictness becomes a value",

			// lm/engine → folded into core/engine. Part 1.0 also lists bundles, a
			// from-package here, so that edge is not a violation.
			"internal/lm/engine -> internal/adapters/engineversion":           "slice 6b: Descriptor becomes Definition; the version command is the engine's own",
			"internal/lm/engine -> internal/adapters/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values the adapter supplies",
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
			"internal/adapters",
			// measured adapters, retired in place (slice 13): outside the
			// prefix until they are deleted, so they are named on their own.
			"internal/lm/grpc",
			"internal/vpio/dockerexec",
			"internal/vpio/goplugin",
		},
		forbid: []string{
			// the adapters (the from-set again)
			"internal/adapters",
			"internal/lm/grpc",
			"internal/vpio/dockerexec",
			"internal/vpio/goplugin",
			// the engines, and the retired-in-place backends (slice 11b)
			"internal/engines",
			"internal/lm/backends",
		},
		allowed: map[string]string{
			// sanctioned (Part 1.1): the CLI is a frontend over operations; a
			// package may import its own subpackage.
			"internal/adapters/cli -> internal/adapters/operations":                                         "sanctioned: cli → operations is one of the two adapter-to-adapter edges Part 0 keeps",
			"internal/adapters/cli -> internal/adapters/cli/tui":                                            "sanctioned: a package's own subpackage",
			"internal/adapters/signing -> internal/adapters/signing/allowedsigners":                         "sanctioned: a package's own subpackage",
			"internal/adapters/transcript/vendorreader/claude -> internal/adapters/transcript/vendorreader": "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/mock -> internal/adapters/transcript/vendorreader":   "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/claude -> internal/adapters/transcript":              "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/mock -> internal/adapters/transcript":                "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader -> internal/adapters/transcript":                     "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/signing/countersign -> internal/adapters/signing":                            "sanctioned: a package's own parent tree (signing/*)",
			"internal/adapters/content/archive -> internal/adapters/content":                                "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/content/attest -> internal/adapters/content":                                 "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/content/convert -> internal/adapters/content":                                "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/content/remotetree -> internal/adapters/content":                             "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/coordgrpc/mcpschema/gen -> internal/adapters/coordgrpc/mcpschema":            "sanctioned: a package's own parent tree (coordgrpc/*)",
			"internal/adapters/coordgrpc/mcpschema -> internal/adapters/coordgrpc/pb":                       "sanctioned: the proto is coordgrpc's own subpackage (slice 10 folds mcpschema into coordgrpc)",
			"internal/adapters/cli/tui -> internal/adapters/coordgrpc/pb":                                   "sanctioned: cli/tui is the watch UI on the coordination proto",
			"internal/adapters/mcp -> internal/adapters/coordgrpc/pb":                                       "sanctioned: today's MCP server is the future runner/mcp, which speaks the wire",
			"internal/adapters/mcp -> internal/adapters/coordgrpc/mcpschema":                                "slice 10: mcpschema is generated from coord.Verbs inside coordgrpc; runner/mcp speaks the wire through it (measured)",

			// edges the prefix form surfaced (packages unit A's explicit
			// lists did not name); each MEASURED, with the slice that
			// removes it where Part 1.1 names one
			"internal/adapters/cli -> internal/adapters/agents":                         "slice 4: the adapters/configload split; the agent binding is read through operations.App's Snapshot (measured; Part 1.1 does not place agents)",
			"internal/adapters/cli -> internal/adapters/contextmetrics":                 "measured; Part 1.1 does not place contextmetrics — no slice names this edge",
			"internal/adapters/cli -> internal/adapters/coordgrpc/pb":                   "slice 13: allowlisted until then per Part 1.0",
			"internal/adapters/cli -> internal/adapters/git":                            "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/cli -> internal/adapters/gitignore":                      "measured; Part 1.1 does not place gitignore — no slice names this edge",
			"internal/adapters/cli -> internal/adapters/projectroot":                    "slice 7: launch.HostFacts carries the project root from cmd/*",
			"internal/adapters/cli -> internal/adapters/selfexec":                       "slice 13: hostpty spawns the runner; the self-exec path is a HostFacts value (measured; Part 1.1 does not place selfexec)",
			"internal/adapters/cli -> internal/adapters/tmuxhost":                       "slice 13: tmuxhost goes with vpio; adapters/hostpty replaces it",
			"internal/adapters/cli -> internal/adapters/turnchange":                     "measured; Part 1.1 does not place turnchange — no slice names this edge",
			"internal/adapters/content -> internal/adapters/signing":                    "slice 5: one verifier behind the trust ports",
			"internal/adapters/content/convert -> internal/adapters/signing":            "slice 5: one verifier behind the trust ports",
			"internal/adapters/content/remotetree -> internal/adapters/remote":          "slice 5: the pull-walk is behind composite.Transport / bundles.Reader",
			"internal/adapters/isolation -> internal/adapters/git":                      "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/isolation -> internal/adapters/gitignore":                "measured; Part 1.1 does not place gitignore — no slice names this edge",
			"internal/adapters/mcp -> internal/adapters/contextmetrics":                 "slice 9: runner/mcp serves delivery.Dynamic (measured; Part 1.1 does not place contextmetrics)",
			"internal/adapters/operations -> internal/adapters/agents":                  "slice 4: the adapters/configload split (measured; Part 1.1 does not place agents)",
			"internal/adapters/operations -> internal/adapters/content":                 "slice 5: readers become adapters behind bundles.Reader",
			"internal/adapters/operations -> internal/adapters/content/convert":         "slice 5: readers become adapters behind bundles.Reader",
			"internal/adapters/operations -> internal/adapters/content/remotetree":      "slice 5: readers become adapters behind bundles.Reader",
			"internal/adapters/operations -> internal/adapters/coordgrpc/pb":            "slice 13: allowlisted until then per Part 1.0",
			"internal/adapters/operations -> internal/adapters/engineversion":           "slice 6b: Descriptor becomes Definition; the version command is the engine's own",
			"internal/adapters/operations -> internal/adapters/git":                     "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/operations -> internal/adapters/projectroot":             "slice 7: launch.HostFacts carries the project root from cmd/*",
			"internal/adapters/remote -> internal/adapters/git":                         "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/turnchange -> internal/adapters/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values",
			"internal/lm/grpc -> internal/adapters/projectroot":                         "slice 13: the go-plugin protocol is deleted whole",
			"internal/lm/grpc -> internal/adapters/selfexec":                            "slice 13: the go-plugin protocol is deleted whole",
			"internal/vpio/dockerexec -> internal/adapters/vpio":                        "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/vpio/goplugin -> internal/adapters/vpio":                          "slice 13: vpio/goplugin is deleted with the go-plugin protocol",

			// cli reaching past operations
			"internal/adapters/cli -> internal/engines/claude":                   "slice 11b: engine packages are reached through engine.Registry, composed under cmd/*",
			"internal/adapters/cli -> internal/engines/claude/engine":            "slice 11b: engine packages are reached through engine.Registry, composed under cmd/*",
			"internal/adapters/cli -> internal/lm/backends":                      "slice 11b: lm/backends is deleted whole",
			"internal/adapters/cli -> internal/engines":                          "slice 11b: engines.Build() is called by the composition root, cmd/*",
			"internal/adapters/cli -> internal/lm/grpc":                          "slice 13: the go-plugin protocol is deleted whole",
			"internal/adapters/cli -> internal/adapters/isolation":               "slice 7: the CLI hands launch.Resolve the axes; it stops reaching isolation",
			"internal/adapters/cli -> internal/adapters/mcp":                     "slice 9: the stdio MCP server is deleted; the endpoint lives in runner/mcp",
			"internal/adapters/cli -> internal/adapters/memory":                  "slice 14a: memory.NewCompactor(entry, source, llm) is called by operations.Compact",
			"internal/adapters/cli -> internal/adapters/remote":                  "slice 15: operations.ReviewWalk/ResolveLocalSigner take the orchestration out of the CLI",
			"internal/adapters/cli -> internal/adapters/signing":                 "slice 15: operations.ResolveLocalSigner takes the orchestration out of the CLI",
			"internal/adapters/cli -> internal/adapters/signing/agentkey":        "slice 15: operations.ResolveLocalSigner takes the orchestration out of the CLI",
			"internal/adapters/cli -> internal/adapters/termui":                  "slice 13: termui sits over the pty master the runner owns",
			"internal/adapters/cli -> internal/adapters/transcript":              "slice 13: cli/tui reads the transcript file; the CLI does not open transcripts itself",
			"internal/adapters/cli -> internal/adapters/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values",
			"internal/adapters/cli -> internal/adapters/vpio":                    "slice 13: adapters/hostpty and adapters/attach replace vpio; the CLI reaches them through operations",
			"internal/adapters/cli -> internal/vpio/dockerexec":                  "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/adapters/cli -> internal/vpio/goplugin":                    "slice 13: vpio/goplugin is deleted with the go-plugin protocol",
			"internal/adapters/cli -> internal/adapters/confpatch":               "slice 12: delivery.Ownership (adapters/confpatch) is reached through delivery, not from the CLI",

			// cli/tui and termui
			"internal/adapters/cli/tui -> internal/lm/grpc":             "slice 13: cli/tui sits on the coordination proto and the transcript file; the plugin wire is deleted",
			"internal/adapters/cli/tui -> internal/adapters/operations": "slice 13: the watch UI reads the coordination proto and the transcript file, not the application services",
			"internal/adapters/cli/tui -> internal/adapters/termui":     "slice 13: termui sits over the pty master; the TUI no longer composes it",
			"internal/adapters/termui -> internal/lm/grpc":              "slice 13: the go-plugin protocol is deleted whole",

			// operations reaching sibling adapters (it is the application-services
			// layer; it holds ports, not adapters)
			"internal/adapters/operations -> internal/adapters/content/attest":          "slice 5: attest.VerifyBundle is behind the trust ports composite.Trust holds",
			"internal/adapters/operations -> internal/lm/backends":                      "slice 11b: lm/backends is deleted whole",
			"internal/adapters/operations -> internal/lm/grpc":                          "slice 13: the go-plugin protocol is deleted whole",
			"internal/adapters/operations -> internal/adapters/isolation":               "slice 7: launch.Cells is the port; isolation is injected at cmd/*",
			"internal/adapters/operations -> internal/adapters/memory":                  "slice 14a: memory.NewCompactor(entry, source, llm); the compactor is injected",
			"internal/adapters/operations -> internal/adapters/remote":                  "slice 5: the pull-walk is behind composite.Transport / bundles.Reader",
			"internal/adapters/operations -> internal/adapters/signing":                 "slice 5: one verifier behind the trust ports",
			"internal/adapters/operations -> internal/adapters/signing/agentkey":        "slice 5: one verifier behind the trust ports",
			"internal/adapters/operations -> internal/adapters/signing/allowedsigners":  "slice 5: composite.SignerDecision is core-owned; the adapter is injected",
			"internal/adapters/operations -> internal/adapters/signing/countersign":     "slice 5: one signature (the .sigs/ manifest); countersigning goes",
			"internal/adapters/operations -> internal/adapters/transcript":              "slice 14a: sessions.Entry.NativeSession is the one record; transcript is an injected reader",
			"internal/adapters/operations -> internal/adapters/transcript/policy":       "slice 14a: transcript policy rides with the reader adapter",
			"internal/adapters/operations -> internal/adapters/transcript/vendorreader": "slice 6b: the readers become engine.TranscriptReader values",

			// the runner's two halves today
			"internal/lm/grpc -> internal/adapters/transcript":        "slice 13: the go-plugin protocol is deleted whole",
			"internal/lm/grpc -> internal/adapters/transcript/policy": "slice 13: the go-plugin protocol is deleted whole",
			"internal/adapters/mcp -> internal/lm/backends":           "slice 9: runner/mcp serves delivery.Dynamic; it holds no backend",
			"internal/adapters/mcp -> internal/adapters/isolation":    "slice 9: runner/mcp serves delivery.Dynamic; the cell is resolved before it exists",
			"internal/adapters/mcp -> internal/adapters/memory":       "slice 14a: memory off the plugin; the compactor is an operation",
			"internal/adapters/mcp -> internal/adapters/operations":   "slice 8: host-relayed tools are Verbs.Host frames to coord.HostApp, which operations implements",
			"internal/adapters/mcp -> internal/adapters/transcript":   "slice 14a: the engine-host half of the runner records the transcript",

			// isolation, memory, and the leaf adapters
			"internal/adapters/isolation -> internal/lm/grpc":                             "slice 13: the go-plugin protocol is deleted whole",
			"internal/adapters/memory -> internal/lm/backends":                            "slice 14a: memory off the plugin — NewCompactor(entry, source, llm)",
			"internal/adapters/memory -> internal/lm/grpc":                                "slice 14a: memory off the plugin — NewCompactor(entry, source, llm)",
			"internal/vpio/dockerexec -> internal/adapters/isolation":                     "slice 13: vpio/dockerexec is deleted with the go-plugin protocol",
			"internal/vpio/goplugin -> internal/lm/grpc":                                  "slice 13: vpio/goplugin is deleted with the go-plugin protocol",
			"internal/adapters/companions -> internal/adapters/signing":                   "slice 4: adapters/companions probes; signing is reached through the trust ports",
			"internal/adapters/content/attest -> internal/adapters/signing":               "slice 5: attest.VerifyBundle is the one verifier over the signing adapter — a `must never know: each other` edge Part 1.1 does not resolve; measured",
			"internal/adapters/transcript/vendorreader/claude -> internal/engines/claude": "slice 6b: the claude reader becomes an engine.TranscriptReader the engine package supplies",
		},
	},
	{
		// THE ENGINES RING (Part 1.1, `engines-import-nothing-above-the-port`):
		// an engine package imports the port and the leaves its vocabulary
		// names, and no adapter. Today the port is core/agent (the engine base
		// that slice 6b turns into core/engine), so it stands in the except
		// list beside present, sessions and wire. Every allowlisted edge is
		// MEASURED and leaves in the slice its reason names; lm/backends is
		// measured engines-ring and retired in place (slice 11b).
		name:   "engines-import-nothing-above-the-port",
		from:   []string{"internal/engines", "internal/lm/backends"},
		forbid: []string{"internal/core", "internal/adapters"},
		except: []string{
			"internal/core/agent",
			"internal/core/present",
			"internal/core/sessions",
			"internal/core/wire",
		},
		allowed: map[string]string{
			"internal/engines/claude/engine -> internal/adapters/engineversion":                  "slice 6b: Descriptor becomes Definition; the version command is the engine's own",
			"internal/engines/claude/engine -> internal/adapters/transcript/vendorreader/claude": "slice 6b: the reader becomes an engine.TranscriptReader the engine package supplies",
			"internal/engines/claude/engine -> internal/core/bundles":                            "slice 6: bundles.LLMExports become opaque; Exports(items engine.Items) imports only core/engine",
			"internal/engines/claude -> internal/adapters/confpatch":                             "slice 12: delivery.Ownership (adapters/confpatch) is reached through delivery, not from the engine",
			"internal/engines/claude -> internal/core/paths":                                     "slice 11b: Engine.Home() is a HomeSpec the runner realises; the engine reads no paths",
			"internal/lm/backends -> internal/adapters/engineversion":                            "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/adapters/isolation":                                "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/adapters/remote":                                   "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/adapters/tmuxhost":                                 "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/adapters/transcript/vendorreader":                  "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/adapters/transcript/vendorreader/mock":             "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/core/bundles":                                      "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/core/config":                                       "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/core/paths":                                        "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/core/profiles":                                     "slice 11b: lm/backends is deleted whole",
			"internal/lm/backends -> internal/core/trust":                                        "slice 11b: lm/backends is deleted whole",
		},
	},
	{
		// THE GENERATED COORDINATION PROTO (adapters/coordgrpc/pb) is a wire
		// codec's private vocabulary: only the packages that speak the wire
		// may import it. from is the whole module so a new importer is caught
		// wherever it appears.
		name:   "proto-only-in-adapters",
		from:   []string{"cmd", "internal", "pkg"},
		forbid: []string{"internal/adapters/coordgrpc/pb"},
		allowed: map[string]string{
			"internal/adapters/cli/tui -> internal/adapters/coordgrpc/pb":             "sanctioned: cli/tui is the watch UI on the coordination proto",
			"internal/adapters/mcp -> internal/adapters/coordgrpc/pb":                 "sanctioned: today's MCP server is the future runner/mcp, which speaks the wire",
			"internal/core/coord -> internal/adapters/coordgrpc/pb":                   "slice 10: every remaining generated-type reference in core/coord is re-typed on Go values",
			"internal/adapters/coordgrpc/mcpschema -> internal/adapters/coordgrpc/pb": "slice 10: mcpschema moves into adapters/coordgrpc beside the proto",
			"internal/adapters/cli -> internal/adapters/coordgrpc/pb":                 "slice 13: allowlisted until then per Part 1.0",
			"internal/adapters/operations -> internal/adapters/coordgrpc/pb":          "slice 13: allowlisted until then per Part 1.0",
		},
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
