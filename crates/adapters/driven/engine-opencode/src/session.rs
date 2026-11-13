//! What a session's profiles offer it beyond its context, in the session
//! home opencode reads as a config dir (`OPENCODE_CONFIG_DIR`): each command
//! a file of `commands/`, each MCP server under `mcp` of its `opencode.json`,
//! and the hooks of the session's start run by a plugin of its own
//! (`plugins/ctxloom-session-hooks.js`, [`SESSION_HOOKS`]). Nothing of it
//! reaches the project. opencode has no hook commands: a hook on any other
//! event is not carried (`EngineSurfaces::hook_losses` says so). The command
//! files and the server entries are opencode's documented shapes, read from
//! its docs and not run live.

use run_ports::{HomeFile, SessionItems};
use surface_ports::Json;

/// The session-hooks plugin's file, in the session home.
pub const SESSION_HOOKS_FILE: &str = "plugins/ctxloom-session-hooks.js";

/// The session-hooks plugin as written, running no hook: the same text in
/// every session but for the hooks it runs ([`session_hooks`]).
pub const SESSION_HOOKS: &str = include_str!("session_hooks.js");

/// The plugin's line that lists the hooks it runs.
const HOOKS_LINE: &str = "const HOOKS = []";

/// The session-hooks plugin running the `items`' hooks of the session's start
/// (command hooks: one asking a model has no way to run here), in order.
/// Written at every launch, none to run included, so a hook taken out of the
/// profiles does not outlive it.
pub fn session_hooks(items: &SessionItems) -> String {
    let hooks: Vec<Json> = items
        .hooks
        .iter()
        .filter(|h| crate::SESSION_HOOK_EVENTS.contains(&h.event.as_str()))
        .filter(|h| matches!(h.kind.as_str(), "" | "command") && !h.command.is_empty())
        .map(|h| {
            Json::object([
                ("command", Json::text(h.command.as_str())),
                ("args", Json::strings(&h.args)),
                ("timeout", Json::Number(h.timeout.max(0).to_string())),
            ])
        })
        .collect();
    SESSION_HOOKS.replacen(
        HOOKS_LINE,
        &format!("const HOOKS = {}", Json::Array(hooks).pretty()),
        1,
    )
}

/// Each command as opencode reads one: `commands/<name>.md`, the path
/// separators of its name made dashes, its description in the frontmatter
/// (when it has one), then its body, the command's template.
pub(crate) fn command_files(items: &SessionItems) -> Vec<HomeFile> {
    items
        .commands
        .iter()
        .filter(|c| safe(&c.name))
        .map(|c| {
            let mut text = String::new();
            if !c.description.is_empty() {
                text.push_str("---\ndescription: ");
                // A JSON string is a YAML double-quoted scalar.
                text.push_str(&Json::text(c.description.as_str()).pretty());
                text.push_str("\n---\n\n");
            }
            text.push_str(&c.body);
            HomeFile {
                rel: format!("commands/{}.md", c.name.replace('/', "-")),
                bytes: text.into_bytes(),
            }
        })
        .collect()
}

/// Whether a command's name is safe to make a file of, its separators made
/// dashes: something, and nothing that climbs or leaves the directory.
fn safe(name: &str) -> bool {
    !name.is_empty()
        && !name.contains("..")
        && !name.contains(['\\', ':'])
        && !name.starts_with('/')
}

/// `doc` (the session's `opencode.json`) with each server set under `mcp` by
/// its name: a program as `{type: local, command: [program, ...args],
/// environment}`, a URL as `{type: remote, url, headers}`. A server of the
/// user's own of another name is left as it is.
pub(crate) fn with_servers(mut doc: Json, items: &SessionItems) -> Json {
    if items.mcp.is_empty() {
        return doc;
    }
    if !matches!(doc.get("mcp"), Some(Json::Object(_))) {
        doc.set("mcp", Json::Object(Vec::new()));
    }
    let map = |m: &std::collections::BTreeMap<String, String>| {
        Json::Object(
            m.iter()
                .map(|(k, v)| (k.clone(), Json::text(v.as_str())))
                .collect(),
        )
    };
    let servers = doc.get_mut("mcp").expect("the member was just set");
    for server in &items.mcp {
        let entry = if server.url.is_empty() {
            let mut command = vec![server.command.clone()];
            command.extend(server.args.iter().cloned());
            let mut members = vec![
                ("type", Json::text("local")),
                ("command", Json::strings(&command)),
            ];
            if !server.env.is_empty() {
                members.push(("environment", map(&server.env)));
            }
            Json::object(members)
        } else {
            let mut members = vec![
                ("type", Json::text("remote")),
                ("url", Json::text(server.url.as_str())),
            ];
            if !server.headers.is_empty() {
                members.push(("headers", map(&server.headers)));
            }
            Json::object(members)
        };
        servers.set(&server.name, entry);
    }
    doc
}

#[cfg(test)]
#[path = "session.test.rs"]
mod tests;
