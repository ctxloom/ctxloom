//! The terminal wake's journal on the session (owner ruling 2026-11-18): a
//! primary terminal session's ctxloom MCP server posts a wake on its engine's
//! messaging socket, a best-effort nudge. The session's pending messages are
//! the truth; the wake only starts the turn whose prompt's hook delivers
//! them. One wake is outstanding at a time; a wake not answered in time is
//! disarmed (the messages wait for the next prompt); a prompt that is the
//! wake line redeems it. A disarm and a redeem of one wake race: whichever
//! comes second records nothing that changes the outcome, and either order
//! ends with the wake answered when its turn did start.

use crate::{
    live, DeliverMessages, DisarmWake, IssueWake, MessagesDelivered, SessionEvent,
    SessionRejection, SessionState, WakeDisarmed, WakeIssued, WakeRedeemed,
};

/// What a wake's issue records: refused on a closed session and while
/// another wake is outstanding.
pub(crate) fn wake_issued(
    held: &SessionState,
    issue: &IssueWake,
) -> Result<WakeIssued, SessionRejection> {
    let held = live(held)?;
    if held.closed {
        return Err(SessionRejection::SessionClosed {
            harp: held.harp.clone(),
        });
    }
    if !held.wake_outstanding.is_empty() {
        return Err(SessionRejection::WakeOutstanding {
            nonce: held.wake_outstanding.clone(),
        });
    }
    Ok(WakeIssued {
        nonce: issue.nonce.clone(),
    })
}

/// What a disarm records: the outstanding wake `disarm` names, disarmed;
/// nothing for any other.
pub(crate) fn wake_disarmed(
    held: &SessionState,
    disarm: &DisarmWake,
) -> Result<Option<WakeDisarmed>, SessionRejection> {
    let held = live(held)?;
    let outstanding = !disarm.nonce.is_empty() && held.wake_outstanding == disarm.nonce;
    Ok(outstanding.then(|| WakeDisarmed {
        nonce: disarm.nonce.clone(),
    }))
}

/// What a delivery records: the messages delivered, if any (each id still
/// pending, so a message is delivered ONCE whatever asks again: two hooks
/// within one round both read it pending, and the second records nothing
/// of it), and the wake its prompt redeemed, when its nonce is the
/// outstanding wake's or the last unanswered one's (a turn that came after
/// its wake was disarmed).
pub(crate) fn delivered(
    held: &SessionState,
    deliver: &DeliverMessages,
) -> Result<(Option<MessagesDelivered>, Option<WakeRedeemed>), SessionRejection> {
    let held = live(held)?;
    let nonce = deliver.wake_nonce.as_str();
    let answers =
        !nonce.is_empty() && (held.wake_outstanding == nonce || held.wake_unanswered == nonce);
    let pending: Vec<String> = deliver
        .message_ids
        .iter()
        .filter(|id| held.pending_messages.contains(id))
        .cloned()
        .collect();
    let messages = (!pending.is_empty()).then_some(MessagesDelivered {
        message_ids: pending,
    });
    let redeemed = answers.then(|| WakeRedeemed {
        nonce: nonce.to_string(),
    });
    Ok((messages, redeemed))
}

/// `event` folded into the session's wake.
pub(crate) fn apply(state: &mut SessionState, event: &SessionEvent) {
    match event {
        SessionEvent::WakeIssued(issued) => {
            state.wake_outstanding = issued.nonce.clone();
            state.wake_unanswered.clear();
        }
        SessionEvent::WakeDisarmed(disarmed) => {
            state.wake_outstanding.clear();
            state.wake_unanswered = disarmed.nonce.clone();
        }
        SessionEvent::WakeRedeemed(_) => {
            state.wake_outstanding.clear();
            state.wake_unanswered.clear();
        }
        SessionEvent::MessageSent(sent) => {
            if !state.pending_messages.contains(&sent.message_id) {
                state.pending_messages.push(sent.message_id.clone());
            }
        }
        SessionEvent::MessagesDelivered(delivered) => {
            state
                .pending_messages
                .retain(|id| !delivered.message_ids.contains(id));
        }
        _ => {}
    }
}

#[cfg(test)]
#[path = "wake.test.rs"]
mod tests;
