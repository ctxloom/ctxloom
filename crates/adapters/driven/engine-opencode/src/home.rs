//! The session home opencode is given (`OPENCODE_CONFIG_DIR`): the context,
//! the plugin, the config that carries the agent's permissions, and the
//! pre-seed that keeps opencode from installing its plugin package from the
//! network into each config directory it reads.

use run_ports::HomeFile;
use surface_ports::Json;

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

/// The config file of the session home: opencode 1.18.4 reads `opencode.json`
/// (and `.jsonc`) of `OPENCODE_CONFIG_DIR`, after the project's, so what it
/// says is the session's own (read from the 1.18.4 bundle, not run).
pub const CONFIG_FILE: &str = "opencode.json";

/// opencode's global config directory under `XDG_CONFIG_HOME`: a config
/// directory opencode reads (and installs its plugin package into) too.
const GLOBAL_CONFIG: &str = "xdg/config/opencode";

/// The marker that makes a config directory's `node_modules/` exist: a
/// session file can only be a file.
const NODE_MODULES_MARKER: &str = "node_modules/.ctxloom";

/// The session home's config: the agent's permissions document, exactly as
/// handed, as its `permission` object (owner rulings 135, 137: opencode's own
/// words, never checked; 1.18.4 reads the keyed `permission` object and makes
/// its rule list from it); none, an empty config, so a document an earlier run
/// of the session wrote does not outlive the one that took it out.
pub(crate) fn config(permissions: Option<&Json>) -> String {
    let doc = match permissions {
        Some(document) => Json::object([("permission", document.clone())]),
        None => Json::Object(Vec::new()),
    };
    format!("{}\n", doc.pretty())
}

/// The files of a session home, `context` its `AGENTS.md` and `config` its
/// `opencode.json`: the same for a child's turn and the human's session (each
/// is heard through the plugin).
pub(crate) fn files(context: &str, config: &str) -> Vec<HomeFile> {
    let file = |rel: String, bytes: &[u8]| HomeFile {
        rel,
        bytes: bytes.to_vec(),
    };
    let mut files = vec![
        file(CONTEXT_FILE.to_string(), context.as_bytes()),
        file(PLUGIN_FILE.to_string(), PLUGIN.as_bytes()),
        file(CONFIG_FILE.to_string(), config.as_bytes()),
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
