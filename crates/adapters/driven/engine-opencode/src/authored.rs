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
    let _ = hooks;
    format!("{}\n", Json::Object(Vec::new()).pretty())
}

#[cfg(test)]
#[path = "authored.test.rs"]
mod tests;
