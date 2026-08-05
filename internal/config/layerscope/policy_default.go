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
		{Path: "agents.*.engine", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.driving", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.escalation", Scope: ScopeShared, Note: "which context this project's roles compose"},
		{Path: "agents.*.runtime", Scope: ScopeMachine, Note: "whether THIS box has a container runtime"},
		{Path: "agents.*.permissions", Scope: ScopeShared, Note: "a privilege grant; a team may decide it, but a user's home config must never fill it in for a project"},
		{Path: "agents.*.coordinator", Scope: ScopeShared, Note: "a privilege grant; a team may decide it, but a user's home config must never fill it in for a project"},

		{Path: "dirty_tree_handler", Scope: ScopeShared, Note: "how this project's delegation behaves; same for everyone"},
		{Path: "workspace", Scope: ScopeShared, Note: "how this project's delegation behaves; same for everyone"},
		// dirty_tree_commit_ack is deliberately absent from the config schema
		// (it moved to paths.DirtyTreeCommitAckPath, an admission.Store file) —
		// this rule is defense in depth should it ever reappear in a decoded
		// layer's raw values regardless.
		{Path: "dirty_tree_commit_ack", Scope: ScopeNever, Note: "prior human authorization to mutate a repo belongs in an admission store, never the config chain"},

		{Path: "agent_turn_cap", Scope: ScopeMachine, Note: "a resource ceiling — a fact about the box"},

		{Path: "llm.configs.*", Scope: ScopePreference, Note: "which model a person likes; harmless in either file"},
		{Path: "llm.configs.*.binary_path", Scope: ScopeMachine, Note: "an absolute path on this filesystem"},
		{Path: "llm.configs.*.env", Scope: ScopeMachine, Note: "credential passthrough; a committed value is a leaked secret"},
		{Path: "llm.configs.*.permissions", Scope: ScopeShared, Note: "a privilege grant; a team may decide it, but a user's home config must never fill it in for a project"},
		{Path: "llm.defaults.*", Scope: ScopePreference, Note: "which label plays which role"},

		{Path: "profiles.definitions.*", Scope: ScopeShared, Note: "authored content"},

		{Path: "mcp.servers.*", Scope: ScopeShared, Note: "what the team wires in"},
		{Path: "mcp.servers.*.command", Scope: ScopeMachine, Note: "a binary path on this box"},
		{Path: "mcp.servers.*.args", Scope: ScopeMachine, Note: "arguments to a binary on this box"},
		{Path: "mcp.servers.*.env", Scope: ScopeMachine, Note: "credentials on this box"},
		{Path: "mcp.plugins.*.*", Scope: ScopeShared, Note: "what the team wires in"},
		{Path: "mcp.auto_register_ctxloom", Scope: ScopePreference},

		{Path: "hooks", Scope: ScopeShared, Note: "authored content"},

		{Path: "isolation_images", Scope: ScopeMachine, Note: "image tags present on this machine"},
		{Path: "isolation_engines", Scope: ScopeMachine, Note: "engines present on this machine"},
		{Path: "isolation_devcontainer_base", Scope: ScopeMachine, Note: "whether THIS box has a devcontainer to auto-detect"},
		{Path: "isolation_devcontainer_service", Scope: ScopeMachine, Note: "a fact about this box's compose setup"},
		{Path: "isolation_base_containerfile", Scope: ScopeShared, Note: "its own doc: relative paths resolve against the project root — a repo file"},
		{Path: "runtime", Scope: ScopeMachine, Note: "whether THIS box has a container runtime"},

		{Path: "sync.auto_sync", Scope: ScopePreference},

		{Path: "config.use_distilled", Scope: ScopePreference},
		{Path: "config.compaction_chunks", Scope: ScopePreference},
		{Path: "config.statusline", Scope: ScopeMachine, Note: "whether ctxloom may own THIS terminal's statusline"},
		{Path: "config.sign.key", Scope: ScopeMachine, Note: "a fingerprint or path to this user's key material"},
		{Path: "config.sign.default", Scope: ScopePreference},

		{Path: "editor.command", Scope: ScopeMachine, Note: "a binary on this box"},
		{Path: "editor.args", Scope: ScopeMachine, Note: "arguments to a binary on this box"},

		{Path: "ui.prefix_key", Scope: ScopePreference, Note: "its own doc: flag/env never lives here — only presentation preferences"},
		{Path: "ui.surround", Scope: ScopePreference, Note: "its own doc: flag/env never lives here — only presentation preferences"},
	}
}
