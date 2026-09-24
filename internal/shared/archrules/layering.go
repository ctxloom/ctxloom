package archrules

// LayeringRule states that no non-test file in a package under any prefix in
// From (the directory itself or any subdirectory) may import a package under
// any prefix in Forbid (same subtree matching) unless that package is under a
// prefix in Except. Except exists for the ring-shaped rules: "core may import
// only core and the toolbox" is `Forbid: every in-repo root, Except: the core
// and toolbox prefixes`, which forbids a NEW package by default (the
// conservative direction) instead of leaving it unforbidden until someone
// lists it.
//
// Allowed is the rule's shrinking allowlist, keyed by EDGE — EdgeKey(from,
// dep) — mapped to the fix required to remove it. An edge key rather than a
// package key is what makes the ratchet fine-grained: a package with five
// forbidden imports has five entries, each deleted the moment its own import
// leaves, instead of one entry that stays live — and keeps masking the other
// four — until the last of them goes. An entry whose edge no longer exists
// fails: archlint's LayeringAnalyzer when the import is gone, tests/arch's
// TestArch_LayeringAllowlist_IsLive when the from-package is. A nil map means
// the rule holds with zero exceptions.
type LayeringRule struct {
	Name    string
	From    []string
	Forbid  []string
	Except  []string
	Allowed map[string]string
}

// EdgeKey is the Allowed-map key for one import edge.
func EdgeKey(from, dep string) string { return from + " -> " + dep }

// MatchesFrom reports whether dir is inside a From subtree (or is one itself).
func (r LayeringRule) MatchesFrom(dir string) bool { return UnderAny(dir, r.From) }

// Violates reports whether importing dep from a From package breaks the rule:
// dep resolves under a Forbid prefix and under no Except prefix.
func (r LayeringRule) Violates(dep string) bool {
	return UnderAny(dep, r.Forbid) && !UnderAny(dep, r.Except)
}

// LayeringRules is the table every layering rule lives in. Adding a rule is an
// entry here; nothing else changes.
//
// The cycle-prevention rows each pin a relationship that is only NOT an
// import cycle because one direction lives entirely in an external _test
// package. The compiler refuses a real cycle, but only once BOTH edges exist
// in production code — so the change that reverses a direction gets no
// signal, and the later, innocent change gets the error. These rows move the
// signal to the reversal.
//
// The ring rows pin the decided architecture's rings (docs/architecture/
// audit-2026-09-18/30-decided-architecture.md, Part 1): each allowlisted edge
// is MEASURED, one entry per edge, and its reason names the slice in which it
// leaves — a ratchet, not a claim.
var LayeringRules = []LayeringRule{
	{
		Name:   "coord-must-not-import-cli/tui",
		From:   []string{"internal/core/coord"},
		Forbid: []string{"internal/adapters/cli/tui"},
	},
	{
		Name:   "termui-must-not-import-cli/tui",
		From:   []string{"internal/adapters/termui"},
		Forbid: []string{"internal/adapters/cli/tui"},
	},
	{
		Name:   "shared/agent-must-not-import-engine-plugins",
		From:   []string{"internal/core/agent"},
		Forbid: []string{"internal/engines/claude"},
	},
	{
		// The coarse ancestor of the future `cli/<flow> -> operations/<flow>
		// -> domain` rule: internal/adapters/operations is the frontend-agnostic layer
		// both the CLI and MCP call into, so it must never import back up
		// into internal/adapters/cli. When the per-flow split lands, this entry can
		// be replaced by one per flow (`internal/adapters/operations/<flow>` must not
		// import `internal/adapters/cli/<other-flow>`) without touching the mechanism.
		Name:   "operations-must-not-import-cli",
		From:   []string{"internal/adapters/operations"},
		Forbid: []string{"internal/adapters/cli"},
	},
	{
		// operations is the ports-and-adapters core: it reads engines through
		// engine.Registry and the port, never a concrete engine package, so
		// backend identity cannot be branched on directly.
		Name:   "operations-must-not-import-engine-plugins",
		From:   []string{"internal/adapters/operations"},
		Forbid: []string{"internal/engines/claude"},
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
		Name: "core-imports-only-core",
		From: []string{
			"internal/core",
		},
		Forbid: []string{"cmd", "container", "internal", "pkg", "resources", "scripts"},
		Except: []string{
			// core (the from-set again: core may import core)
			"internal/core",
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
			"internal/shared/report",
			"internal/shared/filelock",
			"internal/shared/exectoken",
			"internal/shared/textblocks",
		},
		Allowed: map[string]string{
			// core/profiles — Part 1.0 lists remote; the others were MEASURED,
			// not listed.
			"internal/core/profiles -> internal/adapters/remote": "slice 5: the pull-walk reader moves to adapters/remote",
			"internal/core/profiles -> internal/shared/upgrade":  "the live schema-upgrade pipeline (upgrade.Pipeline), not slice 1a's deleted migrations — ruled 2026-09-19 (worrisome-subsidy, item 2): it moves with the reader to the adapter side (Part 1.0: slice 5); slice 5 landed without the move, which is still open",
			"internal/core/profiles -> resources":                "top-level resources/ is DATA a reader adapter supplies, not toolbox, and the core ring does not import it — ruled 2026-09-22 (Part 3.3); cutting this edge is task row decent-porthole",

			// core/bundles
			"internal/core/bundles -> internal/adapters/content":            "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/content/attest":     "slice 5: attest.VerifyBundle is called by the reader adapters",
			"internal/core/bundles -> internal/adapters/content/remotetree": "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/remote":             "slice 5: readers become adapters behind bundles.Reader",
			"internal/core/bundles -> internal/adapters/signing":            "slice 5: one verifier, behind the trust ports",
			"internal/core/bundles -> internal/shared/admission":            "slice 5: admission is decided by composite.Trust, not by the bundle package",
			"internal/core/bundles -> internal/shared/upgrade":              "the live schema-upgrade pipeline (upgrade.Pipeline), not slice 1a's deleted migrations — ruled 2026-09-19 (worrisome-subsidy, item 2): it moves with the reader to the adapter side (Part 1.0: slice 5); slice 5 landed without the move, which is still open",

			// core/config
			"internal/core/config -> internal/shared/admission": "slice 5: admission is decided by composite.Trust",

			// shared/agent → its contract half becomes core/engine. Part 1.0 also
			// lists lockwait and iox, which Part 0 names as toolbox; the toolbox is
			// excepted, so those two are not violations.
			"internal/core/agent -> internal/shared/ledger": "slice 12: shared/ledger is deleted",
		},
	},
	{
		// THE ADAPTER RING (Part 1.1): adapters import core; they do not import
		// each other or the engines. The two sanctioned edges — cli → operations
		// (the CLI is a pure frontend over the application services) and a
		// package's own subpackage (cli → cli/tui; runner → runner/mcp) — are
		// allowed entries with a reason that says "sanctioned", so IsLive still
		// confirms they exist. Every other edge is MEASURED and leaves in the
		// slice its reason names.
		Name: "adapters-import-core-not-each-other",
		From: []string{
			"internal/adapters",
		},
		Forbid: []string{
			// the adapters (the from-set again)
			"internal/adapters",
			// the engines
			"internal/engines",
		},
		Allowed: map[string]string{
			// sanctioned (Part 1.1): the trust adapters compose each other at the
			// root — config.Sources.TrustPorts builds the generation's three ports
			// from the config's trust root (already the port), the countersignature
			// stores and the lockfile.
			"internal/adapters/configload -> internal/adapters/signing/countersign":    "sanctioned: Sources.TrustPorts builds the generation's review records",
			"internal/adapters/configload -> internal/adapters/remote":                 "sanctioned: Sources.TrustPorts reads the generation's retraction records from the lockfile",
			"internal/adapters/configload -> internal/adapters/signing/allowedsigners": "sanctioned: Sources.TrustPorts builds the generation's trust root from the embedded and on-disk allowed_signers stores",
			"internal/adapters/companions -> internal/adapters/companions/loadout":     "sanctioned: a package's own subpackage",
			"internal/adapters/companions/loadout -> internal/adapters/signing":        "slice 5: the loadout envelope is signed and verified through the trust ports",
			"internal/adapters/configload -> internal/adapters/projectroot":            "slice 7: launch.HostFacts carries the project root from cmd/*",
			"internal/adapters/operations -> internal/adapters/configload":             "slice 7: the process is composed at cmd/*; operations.App receives the Sources",
			"internal/adapters/operations -> internal/adapters/companions":             "slice 7: the process is composed at cmd/*; the companion Prober is injected",
			"internal/adapters/operations -> internal/adapters/fsstatic":               "slice 14a: the composition root hands operations the static writer; until then operations composes the at-rest delivery itself (DeliverProject, RemoveProject)",
			"internal/adapters/fsstatic -> internal/adapters/confpatch":                "sanctioned (Part 1.4): the ownership record diffs its structured reversals through confpatch's hew machinery; the record lives beside the static writer because the lean companions link confpatch and must not link the package model delivery carries",
			"internal/adapters/cli -> internal/adapters/fsstatic":                      "slice 14a: runner.Main is composed under cmd/*; until then the runner command stands for the composition root and composes the runner's static writer",
			"internal/adapters/cli -> internal/adapters/fsstore":                       "slice 14a: runner.Main is composed under cmd/*; until then the runner command stands for the composition root and roots the runner's claim store",
			"internal/adapters/cli -> internal/adapters/runner":                        "slice 14a: runner.Main is composed under cmd/*; until then the runner command stands for the composition root",
			"internal/adapters/cli -> internal/adapters/hostpty":                       "composition root (cmd/*): the interactive owner's runner is started on its pty by spawn.Runtimes composed there; until then `ctxloom run` starts it itself",
			"internal/adapters/cli -> internal/adapters/attach":                        "composition root (cmd/*): the interactive owner's container runner is attached on its pty by spawn.Runtimes composed there; until then `ctxloom run` attaches it itself",
			"internal/adapters/attach -> internal/adapters/hostpty":                    "sanctioned (Part 1.5): attach is the container's shape of the SAME pty-held runner hostpty owns for the host; one master for the frontend, wherever the runner runs",
			"internal/adapters/runner -> internal/adapters/coordgrpc":                  "slice 10: the runner's RunnerChannel client is coordgrpc's, which decodes the frame and calls runner.Execute; until then runner.Host decodes it",
			"internal/adapters/runner -> internal/adapters/coordgrpc/pb":               "slice 10: the runner's RunnerChannel client is coordgrpc's; until then runner.Host sees the frame's Launch",
			"internal/adapters/cli -> internal/adapters/configload":                    "slice 7: the process is composed at cmd/*; the CLI receives the composition (init's pinned target)",
			"internal/adapters/cli -> internal/adapters/companions":                    "slice 7: the companion list/show/status commands drive the probe; composed at cmd/*",
			"internal/adapters/cli -> internal/adapters/companions/loadout":            "ctxloom is its own companion: the CLI owns `ctxloom loadout`'s place in the documented tree while cmd/ctxloom owns the embedded bytes; removed when the companion-side loadout package (cobra + envelope encode, no other adapter) moves out of adapters",
			// sanctioned (Part 1.1): the CLI is a frontend over operations; a
			// package may import its own subpackage.
			"internal/adapters/cli -> internal/adapters/operations":                                         "sanctioned: cli → operations is one of the two adapter-to-adapter edges Part 0 keeps",
			"internal/adapters/cli -> internal/adapters/cli/tui":                                            "sanctioned: a package's own subpackage",
			"internal/adapters/transcript/vendorreader/claude -> internal/adapters/transcript/vendorreader": "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/mock -> internal/adapters/transcript/vendorreader":   "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/claude -> internal/adapters/transcript":              "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader/mock -> internal/adapters/transcript":                "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript/vendorreader -> internal/adapters/transcript":                     "sanctioned: a package's own parent tree (transcript/*)",
			"internal/adapters/transcript -> internal/adapters/transcript/policy":                           "sanctioned: a package's own subpackage (the read-side content policy FilteredSource applies)",
			"internal/adapters/signing/countersign -> internal/adapters/signing":                            "sanctioned: a package's own parent tree (signing/*)",
			"internal/adapters/content/archive -> internal/adapters/content":                                "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/content/attest -> internal/adapters/content":                                 "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/content/remotetree -> internal/adapters/content":                             "sanctioned: a package's own parent tree (content/*)",
			"internal/adapters/coordgrpc/mcpschema/gen -> internal/adapters/coordgrpc/mcpschema":            "sanctioned: a package's own parent tree (coordgrpc/*)",
			"internal/adapters/coordgrpc/mcpschema -> internal/adapters/coordgrpc/pb":                       "sanctioned: the proto is coordgrpc's own subpackage (slice 10 folds mcpschema into coordgrpc)",
			"internal/adapters/coordgrpc -> internal/adapters/coordgrpc/discover":                           "sanctioned: a package's own subpackage — the servers record the endpoint they bound in the file discover reads",
			"internal/adapters/coordgrpc -> internal/adapters/coordgrpc/pb":                                 "sanctioned: a package's own subpackage — the codec speaks its own proto",
			"internal/adapters/cli/tui -> internal/adapters/coordgrpc/pb":                                   "sanctioned: cli/tui is the watch UI on the coordination proto",
			"internal/adapters/mcp -> internal/adapters/coordgrpc/mcpschema":                                "the host relay's distill handlers bound their work to mcpschema.DistillBudget, the one number both sides of the relay share",
			"internal/adapters/runner/mcp -> internal/adapters/coordgrpc/pb":                                "sanctioned: runner/mcp is the session endpoint and speaks the wire (Part 1.1's proto-only-in-adapters)",
			"internal/adapters/runner/mcp -> internal/adapters/coordgrpc/mcpschema":                         "slice 10: mcpschema is generated from coord.Verbs inside coordgrpc; runner/mcp speaks the wire through it (measured)",

			// edges the prefix form surfaced (packages unit A's explicit
			// lists did not name); each MEASURED, with the slice that
			// removes it where Part 1.1 names one
			"internal/adapters/cli -> internal/adapters/contextmetrics":                 "measured; Part 1.1 does not place contextmetrics — no slice names this edge",
			"internal/adapters/operations -> internal/adapters/coordgrpc/discover":      "slice 10 remainder: the endpoint file's reader (discover.List) becomes operations' when the servers leave core",
			"internal/adapters/cli -> internal/adapters/coordgrpc/pb":                   "slice 13: allowlisted until then per Part 1.0",
			"internal/adapters/cli -> internal/adapters/coordgrpc":                      "slice 13: the CLI hands the Launch to the spawner and stops encoding the run-start message itself",
			"internal/adapters/cli -> internal/adapters/gitignore":                      "measured; Part 1.1 does not place gitignore — no slice names this edge",
			"internal/adapters/cli -> internal/adapters/projectroot":                    "slice 7: launch.HostFacts carries the project root from cmd/*",
			"internal/adapters/cli -> internal/adapters/selfexec":                       "slice 13: hostpty spawns the runner; the self-exec path is a HostFacts value (measured; Part 1.1 does not place selfexec)",
			"internal/adapters/isolation -> internal/adapters/selfexec":                 "composition root (cmd/*): the runner binary is launch.HostFacts.Binary handed to spawn.Runtimes; until then the host cell's runner command resolves its own self-exec path (measured; Part 1.1 does not place selfexec)",
			"internal/adapters/cli -> internal/adapters/tmuxhost":                       "slice 13: tmuxhost goes with vpio; adapters/hostpty replaces it",
			"internal/adapters/cli -> internal/adapters/turnchange":                     "measured; Part 1.1 does not place turnchange — no slice names this edge",
			"internal/adapters/content -> internal/adapters/signing":                    "slice 5: one verifier behind the trust ports",
			"internal/adapters/content/remotetree -> internal/adapters/remote":          "slice 5: the pull-walk is behind composite.Transport / bundles.Reader",
			"internal/adapters/isolation -> internal/adapters/git":                      "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/isolation -> internal/adapters/gitignore":                "measured; Part 1.1 does not place gitignore — no slice names this edge",
			"internal/adapters/mcp -> internal/adapters/contextmetrics":                 "the host relay's context_status handler reads contextmetrics (measured; Part 1.1 does not place contextmetrics)",
			"internal/adapters/operations -> internal/adapters/content":                 "slice 5: readers become adapters behind bundles.Reader",
			"internal/adapters/operations -> internal/adapters/content/remotetree":      "slice 5: readers become adapters behind bundles.Reader",
			"internal/adapters/operations -> internal/adapters/coordgrpc/pb":            "slice 13: allowlisted until then per Part 1.0",
			"internal/adapters/operations -> internal/adapters/engineversion":           "slice 11b: the version command is the engine's own, on the instance half of the port",
			"internal/adapters/operations -> internal/adapters/git":                     "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/operations -> internal/adapters/gitignore":               "measured; Part 1.1 does not place gitignore — no slice names this edge (the doctor's gitignore-posture row reads its detector)",
			"internal/adapters/operations -> internal/adapters/projectroot":             "slice 7: launch.HostFacts carries the project root from cmd/*",
			"internal/adapters/remote -> internal/adapters/git":                         "measured; Part 1.1 does not place git — no slice names this edge",
			"internal/adapters/turnchange -> internal/adapters/transcript/vendorreader": "slice 11b: the readers become engine.TranscriptReader values (Engine.Transcripts)",

			// cli reaching past operations
			"internal/adapters/cli -> internal/engines/claude":            "slice 11b: engine packages are reached through engine.Registry, composed under cmd/*",
			"internal/adapters/cli -> internal/engines":                   "slice 11b: engines.Build() is called by the composition root, cmd/*",
			"internal/adapters/cli -> internal/adapters/isolation":        "slice 7: the CLI hands launch.Resolve the axes; it stops reaching isolation",
			"internal/adapters/cli -> internal/adapters/mcp":              "the session host composes the coordinator's hosting helper and the host relay (mcp.HostCoordinatorForSession); the session endpoint lives in runner/mcp",
			"internal/adapters/cli -> internal/adapters/memory":           "slice 14a: memory.NewCompactor(entry, source, llm) is called by operations.Compact",
			"internal/adapters/cli -> internal/adapters/remote":           "measured: `bundle push` drives remote.PublishManager, `deps list` reads the lockfile, the item listing parses references and `remote discover` normalises URLs directly; Part 1.1 places these behind operations and no slice names them",
			"internal/adapters/cli -> internal/adapters/signing":          "measured: init and `signer trust` spell signing.NamespacePublish, the trust namespace they write into; leaves when the namespace is a value operations hands back",
			"internal/adapters/cli -> internal/adapters/signing/agentkey": "measured: the signing frontends (review, sign, bundle push) hold the *agentkey.Discoverer operations.SignerDiscoverer composes and render agentkey's own candidate listing and hardware-key posture over operations.ResolveLocalSigner; a rendering vocabulary, not an orchestration",
			"internal/adapters/cli -> internal/adapters/termui":           "slice 13: termui sits over the pty master the runner owns",
			"internal/adapters/cli -> internal/adapters/transcript":       "slice 13: cli/tui reads the transcript file; the CLI does not open transcripts itself",
			"internal/adapters/cli -> internal/adapters/confpatch":        "slice 12: delivery.Ownership (adapters/confpatch) is reached through delivery, not from the CLI",

			// cli/tui and termui
			"internal/adapters/cli/tui -> internal/adapters/operations": "slice 13: the watch UI reads the coordination proto and the transcript file, not the application services",
			"internal/adapters/cli/tui -> internal/adapters/termui":     "slice 13: termui sits over the pty master; the TUI no longer composes it",

			// operations reaching sibling adapters (it is the application-services
			// layer; it holds ports, not adapters)
			"internal/adapters/operations -> internal/adapters/content/attest":          "slice 5: attest.VerifyBundle is behind the trust ports composite.Trust holds",
			"internal/adapters/operations -> internal/adapters/isolation":               "slice 7: launch.Cells is the port; isolation is injected at cmd/*",
			"internal/adapters/operations -> internal/adapters/memory":                  "slice 14a: memory.NewCompactor(entry, source, llm); the compactor is injected",
			"internal/adapters/operations -> internal/adapters/remote":                  "slice 5: the pull-walk is behind composite.Transport / bundles.Reader",
			"internal/adapters/operations -> internal/adapters/operations/managedhooks": "sanctioned: a package's own subpackage — the managed hook set operations assembles and reports",
			"internal/adapters/operations/managedhooks -> internal/adapters/remote":     "slice 5: the profile gate's bundle refs are parsed through the pull-walk's ref grammar (remote.ParseReference); behind composite.Transport / bundles.Reader with the operations edge above",
			"internal/adapters/runner -> internal/adapters/tmuxhost":                    "sanctioned: the runner hosts an interactive engine in a tmux pane on its own terminal (runner.RunLaunchSpec)",
			"internal/adapters/operations -> internal/adapters/signing":                 "slice 5: one verifier behind the trust ports",
			"internal/adapters/operations -> internal/adapters/signing/agentkey":        "slice 5: one verifier behind the trust ports",
			"internal/adapters/operations -> internal/adapters/signing/allowedsigners":  "slice 5: composite.SignerDecision is core-owned; the adapter is injected",
			"internal/adapters/operations -> internal/adapters/signing/countersign":     "slice 5: one signature (the .sigs/ manifest); countersigning goes",
			"internal/adapters/operations -> internal/adapters/transcript":              "slice 14a: sessions.Entry.NativeSession is the one record; transcript is an injected reader",
			"internal/adapters/operations -> internal/adapters/transcript/policy":       "slice 14a: transcript policy rides with the reader adapter",
			"internal/adapters/operations -> internal/adapters/transcript/vendorreader": "slice 11b: the readers become engine.TranscriptReader values (Engine.Transcripts)",

			// adapters/spawn — the production coord.Spawner, composed at cmd/*. It
			// SELECTS over the App's generations, RESOLVES through the launch trunk
			// and STARTS runners through isolation; Part 1.1 gives it launch.Deps
			// and a Runtimes port instead, both handed in at cmd/*.
			"internal/adapters/spawn -> internal/adapters/operations": "slice 13: spawn holds launch.Deps and the session store, not the App; the launch trunk's operations are reached through them",
			"internal/adapters/spawn -> internal/adapters/isolation":  "slice 13: spawn.Runtimes is the port; isolation implements it and is injected at cmd/*",

			// runner/mcp — the session endpoint (delivery.Dynamic). The relay
			// contract and the shared DTOs it advertises live in operations
			// beside the application services that answer them.
			"internal/adapters/runner/mcp -> internal/adapters/operations": "the relays advertise the host-tool contract operations declares (the DTOs and descriptions the relay's handlers decode)",
			// the session host's hosting helper (mcp.HostCoordinatorForSession)
			// stands the coordinator's wire up (coordgrpc.Serve) beside the
			// host relay it composes
			"internal/adapters/mcp -> internal/adapters/coordgrpc": "the hosting helper serves the coordinator's wire; leaves when hosting moves to the composition root",
			// runner/mcp is the runner's own subpackage: the endpoint serves over
			// the Home the runner owns
			"internal/adapters/runner/mcp -> internal/adapters/runner": "sanctioned: a package's own parent tree (runner/*)",
			"internal/adapters/cli -> internal/adapters/runner/mcp":    "slice 14a: runner.Main composes its Dynamic port under cmd/*; until then the runner command stands for the composition root",

			// the runner's two halves today
			"internal/adapters/mcp -> internal/adapters/memory":     "slice 14a: memory off the plugin; the compactor is an operation",
			"internal/adapters/mcp -> internal/adapters/operations": "carried from slice 8, deferred by slice 9: the seven relayed handler bodies behind mcp.HostApp move under operations (which then implements coord.HostApp itself); the relay CONTRACT already lives there",
			"internal/adapters/mcp -> internal/adapters/transcript": "slice 14a: the engine-host half of the runner records the transcript",
			// the engine host records the canonical transcript through
			// adapters/transcript's recorder; the composition hands the runner a
			// recorder port instead when runner.Main is composed under cmd/*
			"internal/adapters/runner -> internal/adapters/transcript": "slice 13: the transcript recorder is a port runner.Deps carries, injected at cmd/*; until then the engine host opens it",

			// runner/coordtest is the in-process runner double compiled into no
			// binary (PATH A's stdio-server tests stand it up); it imports what
			// the runner it stands up imports, and dies with those tests.
			"internal/adapters/runner/coordtest -> internal/adapters/runner":    "sanctioned: a package's own parent tree (runner/*)",
			"internal/adapters/runner/coordtest -> internal/adapters/isolation": "the double stands in for a runner in the host relay's tests (measured)",

			// isolation, memory, and the leaf adapters
			"internal/adapters/companions -> internal/adapters/signing":                   "slice 4: adapters/companions probes; signing is reached through the trust ports",
			"internal/adapters/content/attest -> internal/adapters/signing":               "slice 5: attest.VerifyBundle is the one verifier over the signing adapter — a `must never know: each other` edge Part 1.1 does not resolve; measured",
			"internal/adapters/transcript/vendorreader/claude -> internal/engines/claude": "slice 11b: the claude reader becomes an engine.TranscriptReader the engine package supplies (Engine.Transcripts)",
		},
	},
	{
		// THE ENGINES RING (Part 1.1, `engines-import-nothing-above-the-port`):
		// an engine package imports the port (core/engine) and the leaves its
		// vocabulary names, and no adapter. core/agent still carries the
		// instance half's remaining contract (agent.Backend, agent.Hosted, the
		// writers), so it stands in the except list beside the port. Every
		// allowlisted edge is MEASURED and leaves in the slice its reason
		// names.
		Name:   "engines-import-nothing-above-the-port",
		From:   []string{"internal/engines"},
		Forbid: []string{"internal/core", "internal/adapters"},
		Except: []string{
			"internal/core/agent",
			"internal/core/engine",
			"internal/core/present",
			"internal/core/sessions",
			"internal/core/wire",
		},
		Allowed: map[string]string{
			"internal/engines -> internal/adapters/transcript/vendorreader/mock":                 "sanctioned: the composition root hands each kind the readers of its own store (Engine.Transcripts)",
			"internal/engines/claude/engine -> internal/adapters/engineversion":                  "sanctioned: the reading of `claude --version` is the version adapter's parse, handed to the kind by the root (Definition.Version)",
			"internal/engines/claude/engine -> internal/adapters/transcript/vendorreader/claude": "sanctioned: the composition root hands the kind the readers of its own store (Engine.Transcripts)",
			"internal/engines/claude -> internal/adapters/confpatch":                             "slice 12: delivery.Ownership (adapters/confpatch) is reached through delivery, not from the engine",
			"internal/engines/claude -> internal/core/paths":                                     "slice 11b: Engine.Home() is a HomeSpec the runner realises; the engine reads no paths",
		},
	},
	{
		// THE GENERATED COORDINATION PROTO (adapters/coordgrpc/pb) is a wire
		// codec's private vocabulary: only the packages that speak the wire
		// may import it. from is the whole module so a new importer is caught
		// wherever it appears.
		Name:   "proto-only-in-adapters",
		From:   []string{"cmd", "internal", "pkg"},
		Forbid: []string{"internal/adapters/coordgrpc/pb"},
		Allowed: map[string]string{
			"internal/adapters/cli/tui -> internal/adapters/coordgrpc/pb":             "sanctioned: cli/tui is the watch UI on the coordination proto",
			"internal/adapters/runner/mcp -> internal/adapters/coordgrpc/pb":          "sanctioned: runner/mcp is the session endpoint and speaks the wire",
			"internal/adapters/coordgrpc/mcpschema -> internal/adapters/coordgrpc/pb": "sanctioned: mcpschema projects the proto into the tool schemas, beside it under coordgrpc",
			"internal/adapters/coordgrpc -> internal/adapters/coordgrpc/pb":           "sanctioned: the codec is the proto's owner",
			"internal/adapters/runner -> internal/adapters/coordgrpc/pb":              "slice 10: the runner's RunnerChannel client is coordgrpc's; until then runner.Host sees the frame's Launch",
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
		Name:   "clifmt-must-not-import-ctxloom",
		From:   []string{"pkg/clifmt"},
		Forbid: []string{"internal", "cmd"},
	},
}
