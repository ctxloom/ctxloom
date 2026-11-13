//! The bundles' authored hooks on opencode (owner ruling 2026-11-12: in
//! this slice, through the plugin). opencode has no hook commands (ruling
//! 58): its plugin runs them. The launch writes them, from the run's
//! selection, as `ctxloom-authored-hooks.json` in the session home (always
//! an opencode config directory, `OPENCODE_CONFIG_DIR`, in both home modes),
//! and `plugins/ctxloom.js` reads it once and runs each hook on the trigger
//! it names:
//!
//! | unified | trigger (the plugin's) | opencode's hook |
//! |---|---|---|
//! | `pre_tool`, `pre_shell` | `tool.before` | `tool.execute.before` (exit 2 blocks the tool) |
//! | `post_tool`, `post_file_edit` | `tool.after` | `tool.execute.after` |
//! | `session_start` | `session.start` | the bus's `session.created` |
//! | `turn_start` | `turn.start` | `chat.message` |
//! | `turn_end` | `turn.end` | the bus's `session.idle` |
//!
//! Any other event has no form here, and a hook of ctxloom's own (a
//! callback) is not one of them: the plugin itself is ctxloom's on
//! opencode. A tool class narrows a hook to opencode's tool names. Read
//! from opencode 1.18.4's plugin interface, not verified live.

use std::collections::BTreeMap;

use surface_ports::{Hook, Json};

/// The file, in the session home.
pub const AUTHORED_FILE: &str = "ctxloom-authored-hooks.json";

/// The authored hooks of `hooks` (unified events to their hooks, in firing
/// order) as the plugin reads them: each trigger to its hooks.
pub(crate) fn authored(hooks: &BTreeMap<String, Vec<Hook>>) -> String {
    let mut triggers: Vec<(&'static str, Vec<Json>)> = Vec::new();
    for (event, list) in hooks {
        let Some((trigger, narrowed)) = route(event) else {
            continue;
        };
        for hook in list {
            let Some(entry) = entry(hook, narrowed) else {
                continue;
            };
            match triggers.iter_mut().find(|(t, _)| *t == trigger) {
                Some((_, entries)) => entries.push(entry),
                None => triggers.push((trigger, vec![entry])),
            }
        }
    }
    triggers.sort_by_key(|(t, _)| *t);
    let doc = Json::object(
        triggers
            .into_iter()
            .map(|(trigger, entries)| (trigger, Json::Array(entries))),
    );
    format!("{}\n", doc.pretty())
}

/// The plugin's trigger for a unified event, and the tool class an event
/// narrowed by its name carries.
fn route(event: &str) -> Option<(&'static str, Option<&'static str>)> {
    Some(match event {
        "pre_tool" => ("tool.before", None),
        "pre_shell" => ("tool.before", Some("shell")),
        "post_tool" => ("tool.after", None),
        "post_file_edit" => ("tool.after", Some("file_edit")),
        "session_start" => ("session.start", None),
        "turn_start" => ("turn.start", None),
        "turn_end" => ("turn.end", None),
        _ => return None,
    })
}

/// opencode's tools of a tool class.
fn tools_of(class: &str) -> Option<&'static [&'static str]> {
    match class {
        "shell" => Some(&["bash"]),
        "file_edit" => Some(&["edit", "write", "patch", "multiedit"]),
        "skill" => Some(&["skill"]),
        _ => None,
    }
}

/// One hook as the plugin runs it: a command line (run by a shell, as
/// claude runs one), its timeout, and the tools it is narrowed to; none for
/// a callback (the plugin is ctxloom's), a hook that is not a command, or
/// one narrowed to a class opencode has no tool for.
fn entry(hook: &Hook, narrowed: Option<&'static str>) -> Option<Json> {
    let command = hook.kind.is_empty() || hook.kind == "command";
    if hook.is_callback() || !command || hook.command.is_empty() {
        return None;
    }
    let class = if hook.tool.is_empty() {
        narrowed
    } else {
        Some(hook.tool.as_str())
    };
    let tools = match class {
        Some(class) => Some(tools_of(class)?),
        None => None,
    };
    let mut line = vec![hook.command.clone()];
    line.extend(hook.args.iter().cloned());
    let mut members = vec![("command", Json::text(line.join(" ")))];
    if hook.timeout > 0 {
        members.push(("timeout", Json::Number(hook.timeout.to_string())));
    }
    if let Some(tools) = tools {
        members.push(("tools", Json::strings(tools.iter())));
    }
    Some(Json::object(members))
}

#[cfg(test)]
#[path = "authored.test.rs"]
mod tests;
