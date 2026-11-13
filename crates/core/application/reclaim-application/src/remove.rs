//! `ctxloom session remove`: one session destroyed whole, or not at all
//! (owner, 2026-11-12: the safe order, run by the daemon for `DeleteSession`
//! and for each purge of `SweepSessions`, the one function both share).
//!
//! Its scratch worktrees are classified first, changing nothing, and the
//! session's own refusals (its lock, its keep, its retention) are asked of a
//! plan, so a refused removal applies nothing. A worktree spared (work in
//! it) or skipped (its owner not proven ended, or its repository
//! unresolved) keeps the whole session: `--even-if-live`, the session's
//! override, reaches no worktree. Otherwise, on `yes`, the clean worktrees
//! are removed through the worktree leaf, and only once none was left is
//! the session destroyed. A session with no worktrees is the removal alone.

use std::future::Future;

use isolation_application::{IsolationError, Report};
use sessions_application::{AppError, Removal, RemoveRequest};

use crate::ReclaimError;

/// What removing a session takes, over whatever holds the sessions (the
/// daemon's store and the session's own project): its removal, planned or
/// (`yes`) applied, and its scratch worktrees, classified or (`apply`) the
/// reapable ones removed.
pub trait Removals {
    /// The removal of `request.harp` (of the project `request.project`
    /// names): its plan, its refusal, and on `yes` the destroy.
    fn remove(
        &mut self,
        request: RemoveRequest,
    ) -> impl Future<Output = Result<Removal, AppError>> + Send;

    /// The scratch worktrees of the session `harp` of `project`.
    fn worktrees(
        &mut self,
        project: Option<&str>,
        harp: &str,
        apply: bool,
    ) -> Result<Report, IsolationError>;
}

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
pub async fn remove<S: Removals + Send>(
    service: &mut S,
    request: RemoveRequest,
) -> Result<SessionRemoval, ReclaimError> {
    let harp = request.harp.clone();
    let project = request.project.clone();
    let ask = |yes| RemoveRequest {
        yes,
        ..request.clone()
    };
    let mut worktrees = session_worktrees(service, project.as_deref(), &harp, false)?;
    let mut removal = service.remove(ask(false)).await?;
    // Only counts are held across a wait: an isolation error is not `Send`.
    if removal.refusal.is_none() && !keeps_any(worktrees.as_ref()) && request.yes {
        if worktrees.is_some() {
            worktrees = session_worktrees(service, project.as_deref(), &harp, true)?;
        }
        if !keeps_any(worktrees.as_ref()) {
            removal = service.remove(ask(true)).await?;
        }
    }
    let kept = worktrees.as_ref().and_then(|r| keeps(&harp, r));
    Ok(SessionRemoval {
        removal,
        worktrees,
        kept,
    })
}

/// Whether a worktree of `report` keeps its session.
fn keeps_any(report: Option<&Report>) -> bool {
    report.is_some_and(|r| r.spared + r.skipped > 0)
}

/// A session's scratch worktrees, classified (and with `apply`, the reapable
/// ones removed); none for a session with no directory or no worktree.
fn session_worktrees<S: Removals>(
    service: &mut S,
    project: Option<&str>,
    harp: &str,
    apply: bool,
) -> Result<Option<Report>, ReclaimError> {
    match service.worktrees(project, harp, apply) {
        Ok(report) => Ok(Some(report).filter(|r| !r.worktrees.is_empty())),
        Err(IsolationError::NoSession { .. }) => Ok(None),
        Err(error) => Err(ReclaimError::Isolation(error)),
    }
}

/// The refusal of a session a worktree keeps.
fn keeps(harp: &str, report: &Report) -> Option<IsolationError> {
    keeps_any(Some(report)).then(|| IsolationError::KeepsSession {
        harp: harp.to_string(),
        spared: report.spared,
        skipped: report.skipped,
    })
}

#[cfg(test)]
#[path = "remove.test.rs"]
mod tests;
