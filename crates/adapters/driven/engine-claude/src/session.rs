//! What a session is offered beside its context, where claude reads it in the
//! session home (its config dir, `CLAUDE_CONFIG_DIR`): each command a file of
//! `commands/`, each skill a package under `skills/`, each MCP server an
//! entry of the state file's `mcpServers` (user scope), and each hook an
//! entry of ctxloom's hooks file, the one `--settings` names. The first three
//! are the at-rest surfaces' own renderings ([`crate::commands`],
//! [`crate::skills`], [`crate::mcp_entry`]) at the config dir's paths instead
//! of a project's `.claude/`.
//!
//! Not verified against a live claude: that user-scope commands, skills and
//! `mcpServers` are read from a relocated `CLAUDE_CONFIG_DIR`.

use std::collections::BTreeMap;

use run_ports::{HomeFile, SessionSurfaces};
use surface_ports::{Claim, Command, Hook, Json, McpServer, Planned, Skill, SkillFile};

/// The project-scope directory the at-rest renderings write under; in the
/// session home the same files sit at its root.
const PROJECT_DIR: &str = ".claude/";

/// The commands and skills as files of the session home, and what was left
/// out (a skill whose `SKILL.md` claude would refuse, a name that is no safe
/// file name), each said as a sentence.
pub(crate) fn home_files(surfaces: &SessionSurfaces) -> (Vec<HomeFile>, Vec<String>) {
    let mut planned = Planned::default();
    let commands: Vec<Command> = surfaces
        .commands
        .iter()
        .map(|c| Command {
            name: c.name.clone(),
            description: c.description.clone(),
            body: c.body.clone(),
        })
        .collect();
    let skills: Vec<Skill> = surfaces
        .skills
        .iter()
        .map(|s| Skill {
            name: s.name.clone(),
            description: s.description.clone(),
            files: s
                .files
                .iter()
                .map(|f| SkillFile {
                    path: f.path.clone(),
                    bytes: f.bytes.clone(),
                    executable: f.executable,
                })
                .collect(),
        })
        .collect();
    crate::commands::plan(&commands, &mut planned);
    crate::skills::plan(&skills, &mut planned);
    let files = planned
        .claims
        .into_iter()
        .filter_map(|claim| match claim {
            Claim::Whole { path, bytes, .. } => Some(HomeFile {
                rel: path
                    .strip_prefix(PROJECT_DIR)
                    .map_or(path.clone(), str::to_string),
                bytes,
            }),
            _ => None,
        })
        .collect();
    (files, planned.skipped)
}

/// The hooks, by unified event, in the order they fire.
pub(crate) fn hooks(surfaces: &SessionSurfaces) -> BTreeMap<String, Vec<Hook>> {
    let mut out: BTreeMap<String, Vec<Hook>> = BTreeMap::new();
    for h in &surfaces.hooks {
        out.entry(h.event.clone()).or_default().push(Hook {
            kind: h.kind.clone(),
            command: h.command.clone(),
            args: h.args.clone(),
            prompt: h.prompt.clone(),
            timeout: h.timeout,
            is_async: h.is_async,
            tool: h.tool.clone(),
            matcher: h.matcher.clone(),
        });
    }
    out
}

/// The state file with each MCP server registered at user scope, replacing
/// one of its name; the rest of the file as it was.
pub(crate) fn register_mcp(state: &mut Json, surfaces: &SessionSurfaces) {
    if surfaces.mcp.is_empty() {
        return;
    }
    if !matches!(state.get("mcpServers"), Some(Json::Object(_))) {
        state.set("mcpServers", Json::Object(Vec::new()));
    }
    let Some(servers) = state.get_mut("mcpServers") else {
        return;
    };
    for server in &surfaces.mcp {
        servers.set(
            &server.name,
            crate::mcp_entry(&McpServer {
                name: server.name.clone(),
                command: server.command.clone(),
                args: server.args.clone(),
                env: server.env.clone(),
                url: server.url.clone(),
                headers: server.headers.clone(),
            }),
        );
    }
}

#[cfg(test)]
#[path = "session.test.rs"]
pub(crate) mod tests;
