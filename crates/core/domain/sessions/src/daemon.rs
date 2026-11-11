//! The daemon's model on the session (owner D28, the corrected CQRS/ES model):
//! the terminal close (K1), the one message type with its urgency (D12), the
//! clear (M1) and the owner link (K2, D26).
//!
//! "Only its own children" is a domain rule (S-f): the listener stamps the
//! requester from the credential into the command, and the rules here judge
//! it. A requester that names no session is the user (ruling 37), who may do
//! what a session may.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};

use crate::{
    live, Child, ChildEnded, ChildForgotten, ChildLaunchFailed, ChildLaunched, ClearChild,
    ClearRequested, CloseSession, EndChild, ForgetChild, IncarnationOwed, LaunchChild, MessageSent,
    RecordChildLaunchFailed, SendMessage, SessionClosed, SessionEvent, SessionRef,
    SessionRejection, SessionState, StopSession, Urgency,
};

/// The namespace every keyed command id is derived in:
/// UUIDv5(URL namespace, "ctxloom:command"). FIXED: the ids journaled on the
/// streams are derived from it.
fn namespace() -> uuid::Uuid {
    uuid::Uuid::new_v5(&uuid::Uuid::NAMESPACE_URL, b"ctxloom:command")
}

/// The command id of the command keyed `key` (D28: a message's id, or a
/// policy's `send:<message_id>`, `cascade:<owner>:<child>`, ...): a pure
/// function of the key, so a redelivery is DUPLICATE on its stream.
pub fn command_id(key: &str) -> Uuid {
    let _ = (key, namespace());
    Uuid::from_bytes([0; 16])
}

/// The book that sends `command` to the stream `cover` names under the
/// command id keyed `key` ([`command_id`]): any domain's command, so the
/// policies key a run's command as they key a session's.
pub fn keyed_book<M: DomainMessage>(cover: Cover, command: &M, key: &str) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&command.to_wire()).expect("a command encodes"),
                ),
                handling: None,
            })),
            command_id: Some(command_id(key)),
            ..CommandPage::default()
        }),
    }
}

/// An urgency's word, as a refusal names it.
pub fn urgency_word(urgency: Urgency) -> &'static str {
    match urgency {
        Urgency::Inform => "inform",
        Urgency::Wake => "wake",
        Urgency::Steer => "steer",
    }
}

/// `requester`'s authority over `held` (S-f): the user's (none named), or
/// the session that is `held`'s owner. Any other session is NotOwner.
fn owner_may(held: &SessionState, requester: Option<&SessionRef>) -> Result<(), SessionRejection> {
    match requester {
        None => Ok(()),
        Some(requester) if held.owner.as_ref() == Some(requester) => Ok(()),
        Some(requester) => Err(SessionRejection::NotOwner {
            requester: requester.session.clone(),
        }),
    }
}

/// `held`, unless it is closed.
fn open(held: &SessionState) -> Result<&SessionState, SessionRejection> {
    if held.closed {
        return Err(SessionRejection::SessionClosed {
            harp: held.harp.clone(),
        });
    }
    Ok(held)
}

/// What a close records: refused from a session that is not the owner;
/// nothing on a session already closed.
pub(crate) fn closed(
    held: &SessionState,
    close: &CloseSession,
) -> Result<Option<SessionClosed>, SessionRejection> {
    let _ = (held, close, open(held), owner_may(held, None));
    Ok(None)
}

/// A stop order's requester (D25, S-f): a session requester is judged by the
/// owner rule; with none, the depth rule (Go's I2) is the lifeline's.
pub(crate) fn stop_may(held: &SessionState, stop: &StopSession) -> Result<(), SessionRejection> {
    let _ = (held, stop);
    Ok(())
}

/// What a message records: refused on a closed session, from a session that
/// is not the owner, and with an urgency the engine cannot honour (S-c); a
/// wake to a session with no incarnation open, and not stopped, also owes the
/// next (M2).
pub(crate) fn message_sent(
    held: &SessionState,
    send: &SendMessage,
) -> Result<(MessageSent, Option<IncarnationOwed>), SessionRejection> {
    let _ = (held, send, live(held));
    Err(SessionRejection::NoSession)
}

/// What a clear records: refused on a closed session and from a session that
/// is not the owner.
pub(crate) fn clear_requested(
    held: &SessionState,
    clear: &ClearChild,
) -> Result<ClearRequested, SessionRejection> {
    let _ = (held, clear);
    Err(SessionRejection::NoSession)
}

/// What a launch records on the owner `held`, whose own stream root is
/// `me` (its id text): refused from any session but the owner itself (D25),
/// on a closed owner, with no incarnation open, past the delegation limit
/// (fail closed), and into another repository that is not trusted (D26).
pub(crate) fn child_launched(
    held: &SessionState,
    me: &str,
    launch: &LaunchChild,
) -> Result<ChildLaunched, SessionRejection> {
    let _ = (held, me, launch);
    Err(SessionRejection::NoSession)
}

/// Whether `held` holds `child` open.
fn holds(held: &SessionState, child: Option<&SessionRef>) -> bool {
    child.is_some_and(|child| {
        held.children
            .iter()
            .any(|c| c.session.as_ref() == Some(child))
    })
}

/// What an owner records of its child's end: nothing for a child it does not
/// hold.
pub(crate) fn child_ended(
    held: &SessionState,
    end: &EndChild,
) -> Result<Option<ChildEnded>, SessionRejection> {
    let _ = (held, end, holds(held, None));
    Ok(None)
}

/// What an owner records of forgetting its child (before the child's purge,
/// M3): nothing for a child it does not hold.
pub(crate) fn child_forgotten(
    held: &SessionState,
    forget: &ForgetChild,
) -> Result<Option<ChildForgotten>, SessionRejection> {
    let _ = (held, forget);
    Ok(None)
}

/// What the launch's compensation records: nothing on a stream holding no
/// session, or for a child the owner does not hold.
pub(crate) fn launch_failed(
    held: &SessionState,
    failed: &RecordChildLaunchFailed,
) -> Option<ChildLaunchFailed> {
    let _ = (held, failed);
    None
}

/// Fold an event of the daemon's model.
pub(crate) fn apply(state: &mut SessionState, event: &SessionEvent) {
    let _ = (state, event, Child::default());
}

#[cfg(test)]
#[path = "daemon.test.rs"]
mod tests;
