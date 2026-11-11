//! The session's run lifeline (plan 3.1): the incarnation it owes, whether one
//! is open, and the stop flag.
//!
//! What drives it (owner ruling 85): a mint owes the first incarnation, and
//! the run application tells the session when that run ends, however it ends
//! (`RecordIncarnationEnded`: `runner-exit`, `launch-failed`, or `orphaned`
//! from a sweep), so no incarnation stays open. Nothing owes a later one: the
//! relaunch was owed only when mail was pending, and the session has no inbox
//! (owner ruling 101; mail returns as one command to the recipient session,
//! the daemon's design).

use angzarr_embedded::markup::{pack_message, Bug, FactEmit};

use crate::{
    live, IncarnationEnded, IncarnationOwed, RecordIncarnationEnded, SessionAggregate,
    SessionEvent, SessionRejection, SessionState, StopOrdered, StopSession,
};

/// The incarnation a mint owes: the first (owner ruling 2026-11-04, S5
/// review Q1 (b)).
pub(crate) fn first_owed() -> IncarnationOwed {
    IncarnationOwed {
        incarnation: 1,
        reason: "first".to_string(),
    }
}

/// What a stop order records: refused from a session that is not the
/// target's owner (D25, S-f), else, with no session requester, from a child
/// (Go's I2); a second order records nothing.
pub(crate) fn stop_ordered(
    held: &SessionState,
    stop: &StopSession,
) -> Result<Option<StopOrdered>, SessionRejection> {
    let held = live(held)?;
    if stop.requester.is_some() {
        crate::daemon::stop_may(held, stop)?;
    } else if stop.requester_depth > 0 {
        return Err(SessionRejection::ChildCannotStop {
            depth: u64::from(stop.requester_depth),
        });
    }
    Ok((!held.stopped).then(|| StopOrdered {
        reason: stop.reason.clone(),
    }))
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

/// Fold a lifeline event: the incarnation owed, its end, and the stop flag.
pub(crate) fn apply(state: &mut SessionState, event: &SessionEvent) {
    match event {
        SessionEvent::IncarnationOwed(owed) => {
            state.incarnation = owed.incarnation;
            state.open_incarnation = Some(owed.incarnation);
        }
        SessionEvent::IncarnationEnded(_) => state.open_incarnation = None,
        SessionEvent::StopOrdered(_) => state.stopped = true,
        _ => {}
    }
}

#[cfg(test)]
#[path = "lifeline.test.rs"]
mod tests;
