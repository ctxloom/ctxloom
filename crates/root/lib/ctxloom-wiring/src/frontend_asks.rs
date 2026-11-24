//! What a terminal session's frontend tells its human of the permission asks
//! that arrive in its project (owner, 2026-11-22, widened 2026-11-23, the
//! line dropped 2026-11-24): the pure half. Each waiting ask is told once, by
//! the bell the caller rings (a line on the terminal would land in the TUI's
//! input box; the count is the statusline's, `hook_asks`), and an ask
//! settled since says nothing more. The watch itself is `frontend`'s.

use std::collections::HashSet;

use daemon_proto::v1;

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
    /// The asks in `waiting` not told of yet, by their own names, oldest
    /// first, now told of. One that is not waiting (the listing asks for
    /// those that are; a daemon that says otherwise is not believed) is
    /// never told.
    pub(crate) fn news(&mut self, waiting: &[v1::Approval]) -> Vec<String> {
        waiting
            .iter()
            .filter(|approval| approval.state == "waiting")
            .filter(|approval| self.0.insert(approval.name.clone()))
            .map(|approval| ask_id(approval).to_string())
            .collect()
    }
}

/// How many of `listed` are waiting: a daemon that lists one that is not
/// (the listing asks for those that are) is not believed, as in
/// [`AsksTold::news`].
pub(crate) fn count_waiting(listed: &[v1::Approval]) -> u32 {
    let waiting = listed.iter().filter(|a| a.state == "waiting").count();
    u32::try_from(waiting).unwrap_or(u32::MAX)
}

/// The ask's own name, `ask-<adjective>-<noun>`.
fn ask_id(approval: &v1::Approval) -> &str {
    approval.name.rsplit('/').next().unwrap_or_default()
}

#[cfg(test)]
#[path = "frontend_asks.test.rs"]
mod tests;
