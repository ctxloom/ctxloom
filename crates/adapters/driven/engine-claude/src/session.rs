//! What a session's profiles offer it beyond its context, in the session
//! home claude reads as its config dir (`CLAUDE_CONFIG_DIR`, user scope):
//! each command a file of `commands/`, each MCP server under `mcpServers` of
//! its state file (`.claude.json`), each hook beside ctxloom's own in the
//! `--settings` file. Nothing of it reaches the project. Under
//! `session_home: host` the config dir is the user's own, which is never
//! written: the hooks still ride the `--settings` file, and the commands and
//! servers written in the session home are not read.

use std::collections::BTreeMap;

use run_ports::{EngineError, HomeFile, SessionItems};
use surface_ports::{Claim, Command, Hook, Json, McpServer, Planned};

use crate::NAME;

/// Where claude's project layout keeps its own files; its config dir holds
/// the same names at its root.
const PROJECT_DIR: &str = ".claude/";

/// The profile's hooks by unified event, in firing order.
pub(crate) fn hooks(items: &SessionItems) -> BTreeMap<String, Vec<Hook>> {
    let mut by_event: BTreeMap<String, Vec<Hook>> = BTreeMap::new();
    for hook in &items.hooks {
        by_event.entry(hook.event.clone()).or_default().push(Hook {
            kind: hook.kind.clone(),
            command: hook.command.clone(),
            args: hook.args.clone(),
            prompt: hook.prompt.clone(),
            timeout: hook.timeout,
            is_async: hook.is_async,
            tool: hook.tool.clone(),
            matcher: hook.matcher.clone(),
        });
    }
    by_event
}

/// The command files, at the config dir's root as claude's project layout
/// has them under `.claude/`.
pub(crate) fn command_files(items: &SessionItems) -> Vec<HomeFile> {
    let commands: Vec<Command> = items
        .commands
        .iter()
        .map(|c| Command {
            name: c.name.clone(),
            description: c.description.clone(),
            body: c.body.clone(),
        })
        .collect();
    let mut planned = Planned::default();
    crate::commands::plan(&commands, &mut planned);
    planned
        .claims
        .into_iter()
        .filter_map(|claim| match claim {
            Claim::Whole { path, bytes, .. } => Some(HomeFile {
                rel: path.strip_prefix(PROJECT_DIR).unwrap_or(&path).to_string(),
                bytes,
            }),
            _ => None,
        })
        .collect()
}

/// Each server as claude's user scope registers it, by name.
pub(crate) fn servers(items: &SessionItems) -> Vec<(String, Json)> {
    items
        .mcp
        .iter()
        .map(|s| {
            let server = McpServer {
                name: s.name.clone(),
                command: s.command.clone(),
                args: s.args.clone(),
                env: s.env.clone(),
                url: s.url.clone(),
                headers: s.headers.clone(),
            };
            (s.name.clone(), crate::mcp_entry(&server))
        })
        .collect()
}

/// The `--settings` file's hooks document (`home`, ctxloom's own) with the
/// profile's hooks after them, each in the group its matcher selects.
pub(crate) fn with_profile_hooks(
    home: &str,
    items: &SessionItems,
    program: &str,
) -> Result<String, EngineError> {
    let hooks = hooks(items);
    if hooks.is_empty() {
        return Ok(home.to_string());
    }
    let cannot = |ext_err_txt: String| EngineError::CannotCarry {
        engine: NAME.to_string(),
        ext_err_txt,
    };
    // ctxloom's own document, which always reads.
    let mut doc = Json::parse(home).map_err(cannot)?;
    let ops = crate::hooks::ops(&hooks, program).map_err(|e| match e {
        surface_ports::SurfaceError::Unsupported { what, .. } => cannot(what),
        other => cannot(other.to_string()),
    })?;
    for op in ops {
        let surface_ports::JsonOp::HookEntry {
            event,
            matcher,
            value,
        } = op
        else {
            continue;
        };
        let mut group = Vec::new();
        if let Some(matcher) = matcher {
            group.push(("matcher", Json::text(matcher.as_str())));
        }
        group.push(("hooks", Json::Array(vec![value])));
        let events = object_member(&mut doc, "hooks");
        let mut list = match events.get(&event) {
            Some(Json::Array(list)) => list.clone(),
            _ => Vec::new(),
        };
        list.push(Json::object(group));
        events.set(&event, Json::Array(list));
    }
    Ok(format!("{}\n", doc.pretty()))
}

/// The object member `key` of `parent`, made an empty object when it is
/// missing or is not one.
fn object_member<'a>(parent: &'a mut Json, key: &str) -> &'a mut Json {
    if !matches!(parent.get(key), Some(Json::Object(_))) {
        parent.set(key, Json::Object(Vec::new()));
    }
    parent.get_mut(key).expect("the member was just set")
}

#[cfg(test)]
#[path = "session.test.rs"]
mod tests;
