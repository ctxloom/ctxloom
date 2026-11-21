//! The session's run lifeline (plan 3.1): the incarnation it owes, whether one
//! is open. There is no stop flag (owner 2026-11-15): `StopOrdered`, retired,
//! folds to nothing.
//!
//! What drives it (owner ruling 85): a mint owes the first incarnation, and
//! the run application tells the session when that run ends, however it ends
//! (`RecordIncarnationEnded`: `runner-exit`, `launch-failed`, or `orphaned`
//! from a sweep), so no incarnation stays open. A later one is owed only by a
//! resume (`ResumeSession`, owner ruling 115: `run --session`): never a
//! relaunch, which was owed only when mail was pending, and the session has no
//! inbox (owner ruling 101; mail returns as one command to the recipient
//! session, the daemon's design).

use angzarr_embedded::markup::{pack_message, Bug, FactEmit};

use crate::{
    live, IncarnationEnded, IncarnationOwed, RecordIncarnationEnded, RehostSession, ResumeSession,
    SessionAggregate, SessionEvent, SessionRejection, SessionReopened, SessionState,
};

/// The incarnation a mint owes: the first (owner ruling 2026-11-04, S5
/// review Q1 (b)).
pub(crate) fn first_owed() -> IncarnationOwed {
    IncarnationOwed {
        incarnation: 1,
        reason: "first".to_string(),
    }
}

/// Why an incarnation a resume owes is owed.
pub(crate) const RESUMED: &str = "resume";

/// What a resume records (owner ruling 115): the session's next incarnation
/// owed, after its reopen when it was closed (owner 2026-11-15: an explicit
/// resume reopens a closed session; a close is ctxloom's control of its
/// sessions, not the engine's conversation). Refused for a stream holding no session (a destroyed one: the
/// caller asks only for a claimed name), while an incarnation is open, before
/// the engine bound a native session, for another engine, and for another
/// workspace axis than the one it was minted on (none recorded: `none`).
pub(crate) fn resumed(
    held: &SessionState,
    resume: &ResumeSession,
) -> Result<(Option<SessionReopened>, IncarnationOwed), SessionRejection> {
    let harp = || resume.harp.clone();
    if live(held).is_err() {
        return Err(SessionRejection::SessionDestroyed { harp: harp() });
    }
    if held.open_incarnation.is_some() {
        return Err(SessionRejection::SessionRunning { harp: harp() });
    }
    if !resume.engine.is_empty() && resume.engine != held.engine {
        return Err(SessionRejection::EngineMismatch {
            harp: harp(),
            engine: held.engine.clone(),
            asked: resume.engine.clone(),
        });
    }
    // A session from before its axis was recorded worked in the project.
    let workspace = held.workspace.as_deref().unwrap_or("none");
    if !resume.workspace.is_empty() && resume.workspace != workspace {
        return Err(SessionRejection::WorkspaceMismatch {
            harp: harp(),
            workspace: workspace.to_string(),
            asked: resume.workspace.clone(),
        });
    }
    if held.native_id.is_empty() {
        return Err(SessionRejection::NoNativeSession { harp: harp() });
    }
    let reopened = held.closed.then(|| SessionReopened {
        cause: RESUMED.to_string(),
    });
    Ok((
        reopened,
        IncarnationOwed {
            incarnation: held.incarnation + 1,
            reason: RESUMED.to_string(),
        },
    ))
}

/// Why an incarnation a re-host owes is owed, after its host was lost: the
/// one reason the re-host budget counts.
pub const REHOSTED: &str = "rehost";

/// Why an incarnation a draining daemon's hand-over owes is owed (s4-Q5):
/// never counted, never refused for the budget.
pub const DRAINED: &str = "drain";

/// How many re-hosts a child may have in the hour before a loss (S47).
pub const REHOST_BUDGET: u32 = 60;

/// What a re-host records (T19, s4-Q9): the child's next incarnation owed,
/// for the cause it gives (`rehost`, the default, or `drain`). Refused for a
/// stream holding no session, a closed one, one whose incarnation is open,
/// one whose engine bound no native session, and, for a `rehost`, one with
/// [`REHOST_BUDGET`] or more re-hosts in its recent hour.
pub(crate) fn rehosted(
    held: &SessionState,
    rehost: &RehostSession,
) -> Result<IncarnationOwed, SessionRejection> {
    let held = live(held)?;
    let harp = || held.harp.clone();
    if held.closed {
        return Err(SessionRejection::SessionClosed { harp: harp() });
    }
    if held.open_incarnation.is_some() {
        return Err(SessionRejection::SessionRunning { harp: harp() });
    }
    if held.native_id.is_empty() {
        return Err(SessionRejection::NoNativeSession { harp: harp() });
    }
    let cause = match rehost.cause.as_str() {
        "" => REHOSTED,
        cause => cause,
    };
    if cause != DRAINED && rehost.recent_rehosts >= REHOST_BUDGET {
        return Err(SessionRejection::RehostBudgetSpent {
            harp: harp(),
            limit: REHOST_BUDGET,
            recent: rehost.recent_rehosts,
        });
    }
    Ok(IncarnationOwed {
        incarnation: held.incarnation + 1,
        reason: cause.to_string(),
    })
}

/// Go's idle reaper's terminal (`CauseIdleReaped`): an expected end that
/// leaves the session resumable and notices nobody.
const CAUSE_IDLE_REAPED: &str = "idle-reaped";

/// What a run's end records: the end of the open incarnation, the parent to
/// be noticed of it (except for an idle reap, which is no death). An end for
/// an incarnation that is not the open one (already ended, or stale), or on a
/// stream holding no session, records nothing.
pub(crate) fn ended(
    held: &SessionState,
    end: &RecordIncarnationEnded,
) -> Result<FactEmit<SessionAggregate>, Bug> {
    if live(held).is_err() || held.open_incarnation != Some(end.incarnation) {
        return Ok(FactEmit::none());
    }
    Ok(FactEmit::of([pack_message(&IncarnationEnded {
        incarnation: end.incarnation,
        cause: end.cause.clone(),
        notice_parent: end.cause != CAUSE_IDLE_REAPED,
    })]))
}

/// Fold a lifeline event: the incarnation owed, and its end.
pub(crate) fn apply(state: &mut SessionState, event: &SessionEvent) {
    match event {
        SessionEvent::IncarnationOwed(owed) => {
            state.incarnation = owed.incarnation;
            state.open_incarnation = Some(owed.incarnation);
        }
        SessionEvent::IncarnationEnded(_) => state.open_incarnation = None,
        _ => {}
    }
}

#[cfg(test)]
#[path = "lifeline.test.rs"]
mod tests;
