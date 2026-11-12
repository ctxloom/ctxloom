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

/// The plugin as written, calling [`surface_ports::CALLBACK_PROGRAM`]: the
/// same text in every session but for the program it runs ([`plugin`]).
pub const PLUGIN: &str = include_str!("plugin.js");

/// The plugin's line that names the program it runs for each event.
const COMMAND_LINE: &str = "const COMMAND = \"ctxloom\"";

/// The plugin, running `program` (the running binary's file name, owner
/// ruling 163) for each event, the name a JSON string (a JavaScript one).
pub fn plugin(program: &str) -> String {
    PLUGIN.replacen(
        COMMAND_LINE,
        &format!("const COMMAND = {}", Json::text(program).pretty()),
        1,
    )
}

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

/// The session home's seed of the user's recent models (owner ruling 167):
/// opencode 1.18.4 keeps them in `model.json` of its state directory
/// (`$XDG_STATE_HOME/opencode`, relocated into the home), and starts a
/// session with no configured model and no `-m` on the first of them it
/// still knows (`recent`); with none it picks one itself.
pub const MODEL_SEED: &str = "xdg/state/opencode/model.json";

/// opencode's state directory under the user's home (XDG's default, on every
/// platform: 1.18.4 has no platform branch); `XDG_STATE_HOME`, set to an
/// absolute path, moves it to `$XDG_STATE_HOME/opencode`.
const USER_STATE_DIR: &str = ".local/state/opencode";

/// The user's own `model.json`: in `$XDG_STATE_HOME/opencode` when that is an
/// absolute path on `os` ([`run_ports::is_absolute`], as the login's
/// directory is read), else under her `home`; none when she has neither.
pub fn user_model_file(
    home: Option<&str>,
    xdg_state_home: Option<&str>,
    os: &str,
) -> Option<String> {
    xdg_file(home, xdg_state_home, USER_STATE_DIR, "model.json", os)
}

/// opencode's global config directory under the user's home (XDG's default,
/// on every platform, as its state directory); `XDG_CONFIG_HOME`, set to an
/// absolute path, moves it to `$XDG_CONFIG_HOME/opencode`.
const USER_CONFIG_DIR: &str = ".config/opencode";

/// The user's own global `opencode.json` (owner ruling 169): in
/// `$XDG_CONFIG_HOME/opencode` when that is an absolute path on `os`, else
/// under her `home`; none when she has neither.
pub fn user_config_file(
    home: Option<&str>,
    xdg_config_home: Option<&str>,
    os: &str,
) -> Option<String> {
    xdg_file(home, xdg_config_home, USER_CONFIG_DIR, CONFIG_FILE, os)
}

/// `name` of opencode's directory under the XDG base `xdg` when that is an
/// absolute path on `os`, else under `home`'s `default_dir`.
fn xdg_file(
    home: Option<&str>,
    xdg: Option<&str>,
    default_dir: &str,
    name: &str,
    os: &str,
) -> Option<String> {
    match xdg.filter(|dir| run_ports::is_absolute(os, dir)) {
        Some(dir) => Some(format!("{dir}/opencode/{name}")),
        None => home
            .filter(|home| !home.is_empty())
            .map(|home| format!("{home}/{default_dir}/{name}")),
    }
}

/// The session home's config (`opencode.json`): `base` (the user's own global
/// config under a session home, owner ruling 169; `{}` otherwise) with its
/// `permission` replaced WHOLE by the agent's permissions document, exactly
/// as handed (owner rulings 135, 137: opencode's own words, never checked;
/// 1.18.4 reads the keyed `permission` object and makes its rule list from
/// it); with no document, none of hers left either. Written whole at every
/// launch, so a document an earlier run of the session wrote does not
/// outlive the one that took it out.
pub(crate) fn config(base: Json, permissions: Option<&Json>) -> String {
    let mut doc = base;
    match permissions {
        Some(document) => {
            doc.set("permission", document.clone());
        }
        None => {
            doc.remove("permission");
        }
    }
    format!("{}\n", doc.pretty())
}

/// What the session config starts from (owner ruling 169): the user's own
/// global config when it is a JSON object; none of hers, `{}`; one that is
/// not an object (JSONC with comments among them) or did not read, `{}` and
/// the file named as left out.
pub(crate) fn user_base(
    user: Option<&run_ports::UserSettings>,
) -> (Json, Option<run_ports::LeftOut>) {
    let Some(user) = user else {
        return (Json::Object(Vec::new()), None);
    };
    let parsed = user
        .bytes
        .as_deref()
        .and_then(|bytes| std::str::from_utf8(bytes).ok())
        .and_then(|text| Json::parse(text).ok());
    match parsed {
        Some(doc @ Json::Object(_)) => (doc, None),
        _ => (
            Json::Object(Vec::new()),
            Some(run_ports::LeftOut {
                engine: crate::NAME.to_string(),
                path: user.path.clone(),
            }),
        ),
    }
}

/// The files of a session home, `context` its `AGENTS.md`, `config` its
/// `opencode.json` and its plugin running `program`: the same for a child's
/// turn and the human's session (each is heard through the plugin).
pub(crate) fn files(context: &str, config: &str, program: &str) -> Vec<HomeFile> {
    let file = |rel: String, bytes: &[u8]| HomeFile {
        rel,
        bytes: bytes.to_vec(),
    };
    let mut files = vec![
        file(CONTEXT_FILE.to_string(), context.as_bytes()),
        file(PLUGIN_FILE.to_string(), plugin(program).as_bytes()),
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
