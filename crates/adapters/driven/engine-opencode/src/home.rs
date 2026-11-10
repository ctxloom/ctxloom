//! The session home opencode is given (`OPENCODE_CONFIG_DIR`): the context,
//! the plugin, and the pre-seed that keeps opencode from installing its plugin
//! package from the network into each config directory it reads.

use run_ports::HomeFile;

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
pub const PRESEED_LOCK: &str = concat!(
    r#"{"lockfileVersion":3,"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.18.4"}}}}"#,
    "\n"
);

/// opencode's global config directory under `XDG_CONFIG_HOME`: a config
/// directory opencode reads (and installs its plugin package into) too.
const GLOBAL_CONFIG: &str = "xdg/config/opencode";

/// The marker that makes a config directory's `node_modules/` exist: a
/// session file can only be a file.
const NODE_MODULES_MARKER: &str = "node_modules/.ctxloom";

/// The files of a session home, `context` its `AGENTS.md`: the same for a
/// child's turn and the human's session (each is heard through the plugin).
pub(crate) fn files(context: &str) -> Vec<HomeFile> {
    let file = |rel: String, bytes: &[u8]| HomeFile {
        rel,
        bytes: bytes.to_vec(),
    };
    let mut files = vec![
        file(CONTEXT_FILE.to_string(), context.as_bytes()),
        file(PLUGIN_FILE.to_string(), PLUGIN.as_bytes()),
    ];
    for dir in ["", GLOBAL_CONFIG] {
        let at = |rel: &str| {
            if dir.is_empty() {
                rel.to_string()
            } else {
                format!("{dir}/{rel}")
            }
        };
        files.push(file(at(NODE_MODULES_MARKER), b""));
        files.push(file(at("package-lock.json"), PRESEED_LOCK.as_bytes()));
    }
    files
}

#[cfg(test)]
#[path = "home.test.rs"]
mod tests;
