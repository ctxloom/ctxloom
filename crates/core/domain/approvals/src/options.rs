//! The options a claude ask offers (owner, after P0: built by ctxloom, not
//! taken from claude's suggestions), and an ask's input bounded for the
//! journal.
//!
//! The rule an ask implies is the tool and its exact input (Q5):
//! `Bash(<command>)` for Bash, `<Tool>(<path>)` for a tool that takes a
//! file's path (Read, Write, Edit, MultiEdit: `file_path`; NotebookEdit:
//! `notebook_path`). Such an ask offers `allow-once`, `allow-session`,
//! `allow-always` and `deny`; any other tool has no rule and offers
//! `allow-once` and `deny` alone. The one vocabulary of every engine (Q2)
//! adds `deny-always` (ACP's `reject_always`).

use sha2::{Digest, Sha256};

use crate::{ApprovalOption, OptionKind};

/// Run the tool, this once.
pub const ALLOW_ONCE: &str = "allow-once";
/// Run it, and the same call again unasked, for the rest of the session.
pub const ALLOW_SESSION: &str = "allow-session";
/// As `allow-session`, and the rule written into the agent's permissions.
pub const ALLOW_ALWAYS: &str = "allow-always";
/// Do not run it.
pub const DENY: &str = "deny";
/// Never run it (ACP's `reject_always`).
pub const DENY_ALWAYS: &str = "deny-always";

/// How much of an ask's input is journaled as its excerpt.
pub const EXCERPT_BYTES: usize = 4096;

/// The tools whose rule is their path, and the input key that holds it.
const PATH_TOOLS: [(&str, &str); 5] = [
    ("Read", "file_path"),
    ("Write", "file_path"),
    ("Edit", "file_path"),
    ("MultiEdit", "file_path"),
    ("NotebookEdit", "notebook_path"),
];

/// The rule the ask of `tool` with the input `input_json` implies: none for
/// a tool of neither kind, or an input without its key.
fn rule_of(tool: &str, input_json: &str) -> Option<String> {
    let input: serde_json::Value = serde_json::from_str(input_json).ok()?;
    let key = match tool {
        "Bash" => "command",
        _ => PATH_TOOLS.iter().find(|(name, _)| *name == tool)?.1,
    };
    let value = input.get(key)?.as_str()?;
    Some(format!("{tool}({value})"))
}

fn option(id: &str, kind: OptionKind, rule: &str) -> ApprovalOption {
    ApprovalOption {
        id: id.to_string(),
        kind,
        label: String::new(),
        rule: rule.to_string(),
        engine_option_id: String::new(),
    }
}

/// The options of a claude ask of `tool` with the input `input_json`, in
/// the order offered: four about its rule, or `allow-once` and `deny` alone
/// when it implies none. The labels are the CLI's to say.
pub fn claude_options(tool: &str, input_json: &str) -> Vec<ApprovalOption> {
    match rule_of(tool, input_json) {
        Some(rule) => vec![
            option(ALLOW_ONCE, OptionKind::AllowOnce, ""),
            option(ALLOW_SESSION, OptionKind::AllowSession, &rule),
            option(ALLOW_ALWAYS, OptionKind::AllowAlways, &rule),
            option(DENY, OptionKind::RejectOnce, ""),
        ],
        None => vec![
            option(ALLOW_ONCE, OptionKind::AllowOnce, ""),
            option(DENY, OptionKind::RejectOnce, ""),
        ],
    }
}

/// An option's kind as the API words it (`allow_once`, ...).
pub fn kind_word(kind: OptionKind) -> &'static str {
    match kind {
        OptionKind::AllowOnce => "allow_once",
        OptionKind::AllowSession => "allow_session",
        OptionKind::AllowAlways => "allow_always",
        OptionKind::RejectOnce => "reject_once",
        OptionKind::RejectAlways => "reject_always",
    }
}

/// An ask's input as it is journaled: its first [`EXCERPT_BYTES`] as text
/// (cut on a character's boundary), its size and its SHA-256 (hex).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Bounded {
    pub excerpt: String,
    pub bytes: u64,
    pub sha256: String,
}

/// `input`, bounded.
pub fn bounded(input: &str) -> Bounded {
    let mut end = input.len().min(EXCERPT_BYTES);
    while !input.is_char_boundary(end) {
        end -= 1;
    }
    let digest = Sha256::digest(input.as_bytes());
    Bounded {
        excerpt: input[..end].to_string(),
        bytes: input.len() as u64,
        sha256: digest.iter().map(|b| format!("{b:02x}")).collect(),
    }
}

#[cfg(test)]
#[path = "options.test.rs"]
mod tests;
