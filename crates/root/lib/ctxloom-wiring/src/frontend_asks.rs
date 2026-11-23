//! What a terminal session's frontend tells its human of the permission asks
//! that arrive in its project (owner, 2026-11-22, widened 2026-11-23): the
//! pure half. Each
//! waiting ask is told once, as one line (its bell is the caller's), and an
//! ask settled since says nothing more. The watch itself is `frontend`'s.

use std::collections::HashSet;

use daemon_proto::v1;

/// The longest the asked-for command is shown, in characters; a person
/// reads a line, and the full ask is `ctxloom session approvals`.
const SHOWN_CHARS: usize = 160;

/// The listing of every waiting ask of the project of the session `session`
/// (`projects/{project}/sessions/{harp}`), whatever its root: the ones a
/// bare `session approvals` lists. A one-shot is its own root and a
/// sibling session's children have another, and a TUI's human answers
/// them all. None for a name of another shape.
pub(crate) fn project_listing(session: &str) -> Option<v1::ListApprovalsRequest> {
    let (project, _) = session.split_once("/sessions/")?;
    Some(v1::ListApprovalsRequest {
        parent: project.to_string(),
        waiting: true,
        ..v1::ListApprovalsRequest::default()
    })
}

/// The asks already told of, so that each is told once.
#[derive(Default)]
pub(crate) struct AsksTold(HashSet<String>);

impl AsksTold {
    /// The asks in `waiting` not told of yet, as `(ask, its line)`, oldest
    /// first, now told of. One that is not waiting (the listing asks for
    /// those that are; a daemon that says otherwise is not believed) is
    /// never told.
    pub(crate) fn news(&mut self, waiting: &[v1::Approval]) -> Vec<(String, String)> {
        waiting
            .iter()
            .filter(|approval| approval.state == "waiting")
            .filter(|approval| self.0.insert(approval.name.clone()))
            .map(|approval| (ask_id(approval).to_string(), line_of(approval)))
            .collect()
    }
}

/// The ask's own name, `ask-<adjective>-<noun>`.
fn ask_id(approval: &v1::Approval) -> &str {
    approval.name.rsplit('/').next().unwrap_or_default()
}

/// What the line says (a DRAFT for the owner): who asks, what it asks to
/// run, and the command that answers. The ask's words are another agent's:
/// every control character in them (an escape sequence among them) is a
/// space, so the line can ring no bell, move no cursor and set no title of
/// its own.
pub(crate) fn line_of(approval: &v1::Approval) -> String {
    let child = approval.child.rsplit('/').next().unwrap_or_default();
    let what = approval
        .options
        .iter()
        .map(|option| option.rule.as_str())
        .find(|rule| !rule.is_empty())
        .map_or_else(
            || format!("{} {}", approval.tool_name, approval.input_excerpt),
            str::to_string,
        );
    let offered: Vec<&str> = approval
        .options
        .iter()
        .map(|option| option.id.as_str())
        .collect();
    let answer = if offered.is_empty() {
        "<option>".to_string()
    } else {
        format!("<{}>", offered.join("|"))
    };
    format!(
        "{} asks to run {}: answer with `ctxloom session approvals answer {} {answer}`",
        one_line(child),
        one_line(&what),
        ask_id(approval),
    )
}

/// `text` on one line, its controls spaces, bounded.
fn one_line(text: &str) -> String {
    let flat: String = text
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect();
    let flat = flat.trim();
    match flat.char_indices().nth(SHOWN_CHARS) {
        Some((end, _)) => format!("{}...", &flat[..end]),
        None => flat.to_string(),
    }
}

#[cfg(test)]
#[path = "frontend_asks.test.rs"]
mod tests;
