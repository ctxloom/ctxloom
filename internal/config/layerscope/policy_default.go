package layerscope

// DefaultPolicy is ctxloom's table: the key×scope assignment from the
// config-layer-scope design doc's "The key × layer table", grounded in
// resources/schema/input/config-schema.json. It is exhaustive against that
// schema by test (see internal/config's layerscope_policy_test.go): a key the
// schema knows and this table does not is a test failure, so no new schema
// key can be added without its scope being decided here first.
func DefaultPolicy() Policy {
	return Policy{
		{Path: "version", Scope: ScopePreference, Note: "the file's own schema generation; per-file by construction"},

		{Path: "default_agent", Scope: ScopeShared, Note: "which agent a bare `ctxloom run` resolves is project policy"},
		{Path: "agents.*.profiles", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.llm", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.driving", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.escalation", Scope: ScopeShared, Note: "which context this project's roles compose"},
		// DIVERGES from the design doc's literal table (which lists this as
		// ScopeMachine): reclassified to Shared. Reasoning, in full, belongs
		// in the human-facing report for this change (it is a deliberate,
		// documented divergence, not an oversight) — in short: (1) none of
		// the three measured escalation paths touch runtime; (2)
		// strictness.ClassIsolation ALREADY independently refuses a
		// container-runtime agent on a machine that lacks the runtime, so
		// ScopeMachine here is redundant with an existing safety net, not a
		// new one; (3) MEASURED — agentBindingMergeFunc's atomic-replace rule
		// (D) means an agent named by the project (as any agent with a real
		// `llm` must be, since agents.*.llm is Shared and can never
		// live in home) can NEVER also inherit a home-only `runtime`: home's
		// contribution is replaced wholesale the instant project names the
		// same agent. Machine-scoping runtime here does not create friction,
		// it creates a hard, unconditional dead end for every
		// project-defined, container-isolated agent — there is no config
		// shape that makes both llm and runtime:container stick together
		// outside a one-off --config-set flag. That is a functional
		// regression far outside this fix's three named targets.
		{Path: "agents.*.runtime", Scope: ScopeShared, Note: "which context (and posture) this project's roles compose; strictness.ClassIsolation already refuses per-machine when the runtime isn't available"},
		// Shared, and the reasoning is the same as agents.*.llm's: which
		// approaches EXIST is the engine's answer, so a preference is only
		// meaningful beside the engine it was validated against. A home config
		// filling one in for a project-defined agent would be naming a
		// delivery for an engine it cannot see — and agentBindingMergeFunc
		// replaces a binding wholesale, so it could not stick anyway.
		{Path: "agents.*.surfaces.*", Scope: ScopeShared, Note: "a delivery choice validated against the binding's engine; it is only meaningful where that engine is named"},
		{Path: "agents.*.permissions", Scope: ScopeShared, Note: "a privilege grant; a team may decide it, but a user's home config must never fill it in for a project"},
		// Shared, same reasoning as agents.*.surfaces.* just above: which
		// config home a binding gets is a pollution/isolation POLICY decision
		// about the project's agents, not a fact about this machine — a team
		// deciding an agent's runs should (or should not) touch a
		// ctxloom-controlled home is exactly the kind of call agents.*.llm and
		// agents.*.permissions already make in this table, and
		// agentBindingMergeFunc's atomic-replace rule means a home-only value
		// could never stick to a project-defined binding anyway.
		{Path: "agents.*.config_home", Scope: ScopeShared, Note: "a pollution/isolation policy decision about this project's agents, not a per-machine fact"},

		{Path: "dirty_tree_handler", Scope: ScopeShared, Note: "how this project's delegation behaves; same for everyone"},
		{Path: "workspace", Scope: ScopeShared, Note: "how this project's delegation behaves; same for everyone"},
		// dirty_tree_commit_ack is deliberately absent from the config schema
		// (it moved to paths.DirtyTreeCommitAckPath, an admission.Store file) —
		// this rule is defense in depth should it ever reappear in a decoded
		// layer's raw values regardless.
		{Path: "dirty_tree_commit_ack", Scope: ScopeNever, Note: "prior human authorization to mutate a repo belongs in an admission store, never the config chain"},

		{Path: "delegation.concurrency", Scope: ScopeMachine, Note: "a resource ceiling — a fact about the box"},
		{Path: "delegation.depth", Scope: ScopeMachine, Note: "a structural safety ceiling, tuned per box like a resource cap; a team's shared policy would belong in agents.*.permissions instead"},
		// Machine, with its siblings, and for the SAME kind of reason
		// delegation.concurrency is: what it decides is a cost this box pays,
		// not behaviour this project has. The tee changes no delivery — every
		// read still comes from the mailbox — so there is nothing about it a
		// team could legitimately decide once for everyone; what it does is
		// spend this machine's disk and two fsyncs per message writing
		// evidence into ~/.ctxloom/sessions/<harp>/persist/spool that only the
		// operator running the soak will ever read. Committing it would opt
		// every clone into paying for that.
		//
		// NOT ScopeInvocation, despite being a rollout switch: a soak is
		// measured over days, and Invocation would restrict it to env and
		// flag — i.e. forbid the home config file that is exactly where an
		// operator leaves it on across many runs. "Per-run" here means each
		// delegated run carries the posture (the coordinator stamps it into
		// the runner's env at spawn), not that it varies per invocation.
		{Path: "delegation.spool_tee", Scope: ScopeMachine, Note: "a soak switch whose cost is this box's disk and fsyncs; it changes no delivery, so there is nothing here for a team to decide once for everyone"},
		// The CUTOVER switch, and ScopeMachine for a different reason than its
		// soak sibling above: this one does change delivery, and what it
		// changes it to depends on the box — the spool lives in THIS machine's
		// ~/.ctxloom/sessions, and a container-backed run only sees it because
		// this machine's mount puts it there. A committed value would opt every
		// clone's coordinator into a substrate whose behaviour is a property of
		// the operator's filesystem, not of the project.
		//
		// NOT ScopeShared despite being a correctness-relevant switch: it is a
		// migration posture an operator carries across many runs while the
		// cutover is rolling out, not a project decision, and it must be
		// settable in a home config for exactly that reason.
		{Path: "delegation.spool_delivery", Scope: ScopeMachine, Note: "a substrate cutover whose behaviour depends on this machine's spool directory and mounts; a team cannot decide it once for every clone"},

		{Path: "llm.configs.*", Scope: ScopePreference, Note: "which model a person likes; harmless in either file"},
		{Path: "llm.configs.*.binary_path", Scope: ScopeMachine, Note: "an absolute path on this filesystem"},
		{Path: "llm.configs.*.env", Scope: ScopeMachine, Note: "credential passthrough; a committed value is a leaked secret"},
		{Path: "llm.configs.*.permissions", Scope: ScopeShared, Note: "a privilege grant; a team may decide it, but a user's home config must never fill it in for a project"},
		{Path: "llm.defaults.*", Scope: ScopePreference, Note: "which label plays which role"},

		{Path: "profiles.definitions.*", Scope: ScopeShared, Note: "authored content"},

		{Path: "mcp.servers.*", Scope: ScopeShared, Note: "what the team wires in"},
		// DIVERGES from the design doc (ScopeMachine there). MEASURED against
		// a real acceptance scenario (features/cli/mcp.feature "Alice
		// registers a shared MCP server"/"Showing an MCP server's
		// configuration"/"Removing an MCP server"): mcpServer's own schema
		// REQUIRES command
		// ("required": ["command"]), so ScopeMachine here does not merely
		// restrict a project-committed value, it makes mcp.servers.* — a
		// group the design's own table calls ScopeShared ("what the team
		// wires in") — impossible to populate with a working entry from the
		// PROJECT layer at all: every entry needs command to exist, and no
		// clone would ever see one. Reclassified to Shared: a command
		// string is frequently portable across machines ("npx some-package",
		// "docker run ...") in exactly the way a local absolute path is not,
		// so "what the team wires in" fairly includes it.
		{Path: "mcp.servers.*.command", Scope: ScopeShared, Note: "required for the server to exist at all; a command string ('npx x', 'docker run x') is what the team wires in, unlike a local absolute path"},
		// DIVERGES from the design doc (ScopeMachine there) for the SAME
		// class of reason as .command, MEASURED directly against the CLI:
		// `mcp server create --args ...` (mcpAddArgs, internal/cli/mcp.go)
		// writes straight into the committed PROJECT file — the only target
		// it has — so Machine-scoping args does not just restrict a
		// project-committed value, it makes THIS COMMAND silently drop
		// exactly the bytes the user typed on its own command line (`ctxloom
		// mcp server create tools --command python --args "-m,mcp_tools"`
		// persists as `command: python` alone, args gone, `python` with no
		// arguments doing nothing useful) while still reporting "added" and
		// exit 0 — the project's own characteristic silent-no-op bug, caught
		// by mutation-style manual verification of this exact fix rather
		// than a pre-existing test. Unlike env below, an argument list is
		// rarely a secret, so there is little to protect by keeping it
		// Machine-only.
		{Path: "mcp.servers.*.args", Scope: ScopeShared, Note: "arguments the CLI's own `mcp server create --args` writes straight into the committed project file; rarely a secret, unlike env"},
		// Kept Machine as the design doc specifies, UNLIKE command/args
		// above: unlike them, no CLI verb writes mcp.servers.*.env today
		// (`mcp server create` has no --env flag, and `mcp server edit`
		// explicitly refuses a config-level ref — see runMCPServerEdit) — so
		// there is no forcing "the CLI silently drops what I typed" case to
		// measure, and dropping a hand-edited env block is exactly the
		// credential-leak protection this scope exists to provide.
		{Path: "mcp.servers.*.env", Scope: ScopeMachine, Note: "credentials on this box"},
		{Path: "mcp.plugins.*.*", Scope: ScopeShared, Note: "what the team wires in"},
		{Path: "mcp.auto_register_ctxloom", Scope: ScopePreference},

		{Path: "hooks", Scope: ScopeShared, Note: "authored content"},

		{Path: "isolation_images", Scope: ScopeMachine, Note: "image tags present on this machine"},
		{Path: "isolation_engines", Scope: ScopeMachine, Note: "engines present on this machine"},
		{Path: "isolation_devcontainer_base", Scope: ScopeMachine, Note: "whether THIS box has a devcontainer to auto-detect"},
		{Path: "isolation_devcontainer_service", Scope: ScopeMachine, Note: "a fact about this box's compose setup"},
		{Path: "isolation_base_containerfile", Scope: ScopeShared, Note: "its own doc: relative paths resolve against the project root — a repo file"},
		// Kept as the design doc specifies (ScopeMachine), UNLIKE
		// agents.*.runtime above: this top-level project DEFAULT is not part
		// of any agent binding, so agentBindingMergeFunc's atomic-replace
		// rule never touches it — it has the SAME "committed file loses a
		// pre-existing Machine value on next write" friction every other
		// Machine-scoped project-committed field now has (accepted per
		// design decision #4), not agents.*.runtime's unconditional dead end.
		{Path: "runtime", Scope: ScopeMachine, Note: "whether THIS box has a container runtime"},
		// Shared, and this key is the sharpest instance of what ScopeShared is
		// FOR. It is a privilege grant — the same class as
		// agents.*.permissions and llm.configs.*.permissions above, which are
		// Shared for the identical reason — but unlike those two it is not
		// attached to a binding a project must also name, so a home value would
		// gap-fill EVERY project on the machine that declared nothing. The whole
		// point of a project permission default is PER-PROJECT consent: "in this
		// directory, and nowhere else, an agent may start at this posture". A
		// home-wide permissive default already exists as the claude-code host
		// stopgap (agent.ResolveDefault's claudeCodeDefault), and letting
		// ~/.ctxloom/config.yaml carry this key would create a second one that
		// silently re-grants every clone on the box the posture a human granted
		// exactly one project. Env is refused for the ordinary ScopeShared
		// reason, which bites harder here than anywhere: env is the one channel
		// every spawned child inherits, so an agent that can run `bash` would be
		// setting its own successors' posture.
		{Path: "permissions", Scope: ScopeShared, Note: "a PER-PROJECT privilege grant — the consent is scoped to one project dir, so a home config filling it in for every project on the machine is the exact escalation this key exists to avoid"},

		{Path: "sync.auto_sync", Scope: ScopePreference},

		{Path: "config.use_distilled", Scope: ScopePreference},
		// DIVERGES from the design doc (ScopeMachine there). MEASURED against
		// a real acceptance scenario (features/manage.feature "Statusline
		// can be disabled and re-enabled"): `manage statusline
		// install/uninstall` (config.SetStatusline) has no file to write
		// this preference to but the committed project file — there is no
		// separate machine-scoped project file yet (design decision #4, not
		// built) — so ScopeMachine here does not add friction, it makes the
		// toggle silently revert the next time ANYTHING re-reads the
		// project config (the disabled preference is dropped, and the
		// default — enabled — re-applies). Reclassified to Shared: whether
		// ctxloom owns the HUD is arguably closer to a per-project UX
		// preference a team can reasonably standardize on (like
		// isolation_base_containerfile, ScopeShared for the identical
		// "only one file exists today" reason) than a hard per-machine
		// capability fact.
		{Path: "config.statusline", Scope: ScopeShared, Note: "a per-project UX preference; the only file this preference's own CLI command (manage statusline) can write to today"},
		{Path: "config.sign.key", Scope: ScopeMachine, Note: "a fingerprint or path to this user's key material"},
		{Path: "config.sign.default", Scope: ScopePreference},

		{Path: "editor.command", Scope: ScopeMachine, Note: "a binary on this box"},
		{Path: "editor.args", Scope: ScopeMachine, Note: "arguments to a binary on this box"},

		{Path: "ui.prefix_key", Scope: ScopePreference, Note: "its own doc: flag/env never lives here — only presentation preferences"},
		{Path: "ui.surround", Scope: ScopePreference, Note: "its own doc: flag/env never lives here — only presentation preferences"},
	}
}
