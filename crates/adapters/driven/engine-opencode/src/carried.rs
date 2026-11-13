//! What the launch carries (`run_ports::LaunchSurfaces`, the run agent's
//! selection) into the session home opencode reads as a config dir
//! (`OPENCODE_CONFIG_DIR`) beside the context and the authored hooks
//! ([`crate::authored`]): each command a file of `commands/`, each MCP
//! server under `mcp` of its `opencode.json`. Nothing of it reaches the
//! project. The command files and the server entries are opencode's
//! documented shapes, read from its docs and not run live.

use run_ports::HomeFile;
use surface_ports::{Command, Json, McpServer};

/// Each command as opencode reads one: `commands/<name>.md`, the path
/// separators of its name made dashes, its description in the frontmatter
/// (when it has one), then its body, the command's template.
pub(crate) fn command_files(commands: &[Command]) -> Vec<HomeFile> {
    commands
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
/// `doc` with ctxloom's own server set (slice 3 step 3, T20): a local
/// entry running `program`'s `mcp serve`, with no identity in it (opencode's
/// environment carries the session's name), replacing any of that name
/// (OD-6).
pub(crate) fn with_ctxloom(mut doc: Json, program: &str) -> Json {
    if !matches!(doc.get("mcp"), Some(Json::Object(_))) {
        doc.set("mcp", Json::Object(Vec::new()));
    }
    let mut command = vec![program.to_string()];
    command.extend(surface_ports::ctxloom_server_args());
    let servers = doc.get_mut("mcp").expect("the member was just set");
    servers.set(
        surface_ports::CTXLOOM_SERVER,
        Json::object([
            ("type", Json::text("local")),
            ("command", Json::strings(&command)),
            ("enabled", Json::Bool(true)),
        ]),
    );
    doc
}

pub(crate) fn with_servers(mut doc: Json, mcp: &[McpServer]) -> Json {
    if mcp.is_empty() {
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
    for server in mcp {
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
#[path = "carried.test.rs"]
mod tests;
