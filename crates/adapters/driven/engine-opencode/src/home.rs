//! The session home opencode is given (`OPENCODE_CONFIG_DIR`): the context,
//! the plugin, and the pre-seed that keeps opencode from installing its plugin
//! package from the network into each config directory it reads.

/// The context file, in the session home: opencode reads `AGENTS.md` in a
/// config directory.
pub const CONTEXT_FILE: &str = "AGENTS.md";

/// The plugin's file, in the session home: opencode loads
/// `{plugin,plugins}/*.{ts,js}` of every config directory.
pub const PLUGIN_FILE: &str = "plugins/ctxloom.js";

/// The plugin: the same text in every session.
pub const PLUGIN: &str = include_str!("plugin.js");

/// The lock that tells opencode's install its plugin package is already
/// there (opencode 1.18.4 reifies a config directory without `node_modules/`,
/// or whose lock lacks `@opencode-ai/plugin` in `packages[""].dependencies`;
/// the version is not compared).
pub const PRESEED_LOCK: &str = "";

#[cfg(test)]
#[path = "home.test.rs"]
mod tests;
