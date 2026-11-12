//! `ctxloom session remove`: one session destroyed whole, or not at all
//! (F2, session_worktrees.feature).
//!
//! Its scratch worktrees are classified first, changing nothing, and the
//! session's own refusals (its lock, its keep, its retention) are asked of a
//! plan, so a refused removal applies nothing. A worktree spared (work in
//! it) or skipped (its owner not proven ended, or its repository
//! unresolved) keeps the whole session: `--even-if-live`, the session's
//! override, reaches no worktree. Otherwise, on `yes`, the clean worktrees
//! are removed through the worktree leaf, and only once none was left is
//! the session destroyed. A session with no worktrees is the sessions use
//! case's removal alone.

use isolation_application::{IsolationError, Report, WorktreeUse};
use sessions_application::{Removal, RemoveRequest, SessionService};

use crate::ReclaimError;

/// What a removal came to: the session's plan or destroy, its worktrees'
/// report (none when it has none), and, when a worktree keeps the session,
/// that refusal. The session's own refusal is `removal.refusal`.
#[derive(Debug)]
pub struct SessionRemoval {
    pub removal: Removal,
    pub worktrees: Option<Report>,
    /// `IsolationError::KeepsSession`: a worktree kept the session, and
    /// nothing was destroyed.
    pub kept: Option<IsolationError>,
}

/// Remove `request.harp` whole, or not at all; see the module.
pub async fn remove<S: SessionService + WorktreeUse>(
    service: &mut S,
    request: RemoveRequest,
) -> Result<SessionRemoval, ReclaimError> {
    let harp = request.harp.clone();
    let ask = |yes| RemoveRequest {
        yes,
        ..request.clone()
    };
    let mut worktrees = session_worktrees(service, &harp, false)?;
    let mut removal = service.remove(ask(false)).await?;
    let mut kept = worktrees.as_ref().and_then(|r| keeps(&harp, r));
    if removal.refusal.is_none() && kept.is_none() && request.yes {
        if worktrees.is_some() {
            worktrees = session_worktrees(service, &harp, true)?;
            kept = worktrees.as_ref().and_then(|r| keeps(&harp, r));
        }
        if kept.is_none() {
            removal = service.remove(ask(true)).await?;
        }
    }
    Ok(SessionRemoval {
        removal,
        worktrees,
        kept,
    })
}

/// A session's scratch worktrees, classified (and with `apply`, the reapable
/// ones removed); none for a session with no directory or no worktree.
fn session_worktrees<S: WorktreeUse>(
    service: &mut S,
    harp: &str,
    apply: bool,
) -> Result<Option<Report>, ReclaimError> {
    match service.worktrees(Some(harp), apply) {
        Ok(report) => Ok(Some(report).filter(|r| !r.worktrees.is_empty())),
        Err(IsolationError::NoSession { .. }) => Ok(None),
        Err(error) => Err(ReclaimError::Isolation(error)),
    }
}

/// The refusal of a session a worktree keeps.
fn keeps(harp: &str, report: &Report) -> Option<IsolationError> {
    (report.spared + report.skipped > 0).then(|| IsolationError::KeepsSession {
        harp: harp.to_string(),
        spared: report.spared,
        skipped: report.skipped,
    })
}

#[cfg(test)]
#[path = "remove.test.rs"]
mod tests;
