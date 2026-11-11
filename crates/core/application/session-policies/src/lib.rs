//! The daemon's process managers, as pure functions (D28): each takes the
//! event that triggers it and what it reads (its own stream's folded state,
//! or the pending messages, which are "owed" state, S-d) and answers the
//! commands to send, each KEYED by its cause, so a redelivery or a restart
//! that re-runs it from 0 sends the same command id and is DUPLICATE. Nothing
//! here runs anything: the daemon (a later part) sends what they answer.
//!
//! - delivery (S-a): pending messages and a run's gate give `QueueTurn`,
//!   `send:<message_id>`;
//! - cascade (D29): the owner's `SessionClosed` gives `CloseSession` to each
//!   child it holds, `cascade:<owner>:<child>`; a child's `SessionClosed`
//!   gives `EndChild` on its owner, `child-ended:<owner>:<child>`;
//! - notice: a child's `IncarnationEnded{notice_parent}` gives `SendMessage`
//!   to its owner, `notice:<child>:<incarnation>`;
//! - launch (K2): the owner's `ChildLaunched` gives the child's name claim and
//!   `MintSession{owner}` in the target store, and a refusal of either gives
//!   the fact `RecordChildLaunchFailed` on the owner (the compensation);
//! - destroy (rulings 94, 98; M3): before a purge, `CloseSession` to each
//!   child, `owner-destroyed:<owner>:<child>`, and `ForgetChild` on the
//!   owner, `destroy:<child>`.
//!
//! A policy acts with the user's authority (D-N9): its commands name no
//! session requester.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin, Uuid};
use angzarr_embedded::Any;
use pending_messages::PendingMessage;
use runs::{QueueTurn, TurnUrgency, RUN_DOMAIN};
// RED: the stubs use none of what the policies send yet.
#[allow(unused_imports)]
use sessions::{
    Child, ChildLaunched, ClaimName, CloseSession, EndChild, ForgetChild, IncarnationEnded,
    MintSession, Origin, RecordChildLaunchFailed, SendMessage, SessionClosed, SessionRef, Urgency,
    NAMES_DOMAIN, SESSION_DOMAIN,
};

/// A command a policy sends: the project whose store holds its stream (owner
/// links cross stores, D19), the stream, its key (the command id is
/// `sessions::command_id(key)`), and the command.
#[derive(Clone, Debug, PartialEq)]
pub struct Keyed<C> {
    pub project_id: String,
    pub cover: Cover,
    pub key: String,
    pub command: C,
}

impl<C: DomainMessage> Keyed<C> {
    /// The book that sends it, under its keyed command id.
    pub fn book(&self) -> CommandBook {
        sessions::keyed_book(self.cover.clone(), &self.command, &self.key)
    }

    /// Its command id.
    pub fn command_id(&self) -> Uuid {
        sessions::command_id(&self.key)
    }
}

/// A fact a policy records: keyed by its `external_id`, so a redelivery is
/// DUPLICATE.
#[derive(Clone, Debug, PartialEq)]
pub struct KeyedFact<F> {
    pub project_id: String,
    pub cover: Cover,
    pub external_id: String,
    pub fact: F,
}

impl<F: DomainMessage> KeyedFact<F> {
    /// The book that records it.
    pub fn book(&self) -> CommandBook {
        CommandBook {
            cover: Some(self.cover.clone()),
            page: Some(CommandPage {
                body: Some(command_page::Body::Fact(Fact {
                    payload: Some(Any::from_msg(&self.fact.to_wire()).expect("a fact encodes")),
                    origin: Some(FactOrigin {
                        external_id: self.external_id.clone(),
                        description: String::new(),
                    }),
                })),
                ..CommandPage::default()
            }),
        }
    }
}

/// The stream of the session whose stable id is `session` (UUID text); a
/// session that is not UUID text has none.
pub fn session_cover(session: &str) -> Option<Cover> {
    let id = uuid::Uuid::parse_str(session).ok()?;
    Some(Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes(*id.as_bytes())),
        edition: None,
    })
}

/// The stream of `session`'s run for `incarnation`.
pub fn run_cover(session: &str, incarnation: u32) -> Cover {
    Cover {
        domain: RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(session, incarnation)),
        edition: None,
    }
}

// --------------------------------------------------------------- delivery

/// Where a session's current run stands, as its own stream folds (never a
/// read model): which run, whether it is live (enqueued and not ended), and
/// whether a turn is in flight.
#[derive(Clone, Debug, PartialEq)]
pub struct RunGate {
    pub session: SessionRef,
    pub incarnation: u32,
    pub live: bool,
    pub in_flight: bool,
}

/// The turn urgency of a message's.
pub fn turn_urgency(urgency: Urgency) -> TurnUrgency {
    match urgency {
        Urgency::Inform => TurnUrgency::Inform,
        Urgency::Wake => TurnUrgency::Wake,
        Urgency::Steer => TurnUrgency::Steer,
    }
}

/// The delivery policy (S-a), run on a `MessageSent` and on a turn's end or
/// a run's start: what the next turn carries, if one is to start now.
///
/// - No live run: nothing (a wake owed the incarnation; its run's start runs
///   this again).
/// - Idle: every pending message, in the order sent, if one of them is a
///   wake or a steer; informs alone wait (D30).
/// - A turn in flight: the pending steers, with the informs before them
///   (the steer's turn is the next turn, D30); wakes wait.
///
/// Keyed `send:<message_id>` of the first wake or steer it carries: the
/// message that starts the turn.
pub fn deliver(gate: &RunGate, pending: &[PendingMessage]) -> Option<Keyed<QueueTurn>> {
    let _ = (gate, pending, turn_urgency(Urgency::Wake));
    None
}

// ---------------------------------------------------------------- cascade

/// The children `children` (an owner's own folded state) as sessions.
fn sessions_of(children: &[Child]) -> impl Iterator<Item = &SessionRef> {
    children.iter().filter_map(|child| child.session.as_ref())
}

/// `CloseSession` for `child` with `cause`, keyed `key`.
fn close(child: &SessionRef, cause: &str, key: String) -> Option<Keyed<CloseSession>> {
    Some(Keyed {
        project_id: child.project_id.clone(),
        cover: session_cover(&child.session)?,
        key,
        command: CloseSession {
            cause: cause.to_string(),
            requester: None,
        },
    })
}

/// The cascade (D29): the owner's `SessionClosed` closes every child it
/// still holds (its own stream's `children`, K2, never the cross-project
/// index). Keyed `cascade:<owner>:<child>`.
pub fn cascade(owner: &SessionRef, children: &[Child]) -> Vec<Keyed<CloseSession>> {
    let _ = (
        owner,
        sessions_of(children),
        close(owner, "", String::new()),
    );
    Vec::new()
}

/// A child's `SessionClosed` ends it on its owner, which stops holding it.
/// Keyed `child-ended:<owner>:<child>`.
pub fn child_ended(
    child: &SessionRef,
    owner: &SessionRef,
    closed: &SessionClosed,
) -> Option<Keyed<EndChild>> {
    let _ = (child, owner, closed);
    None
}

// ----------------------------------------------------------------- notice

/// The notice: a child's run ended with `notice_parent`, so its owner is
/// sent a message saying so, a wake when the owner's engine can take one,
/// else an inform, else none. Its message id is its key,
/// `notice:<child>:<incarnation>`.
pub fn notice(
    child: &SessionRef,
    harp: &str,
    owner: &SessionRef,
    ended: &IncarnationEnded,
    supported: &[Urgency],
) -> Option<Keyed<SendMessage>> {
    let _ = (child, harp, owner, ended, supported);
    None
}

// ----------------------------------------------------------------- launch

/// What the launch policy sends the target store for one `ChildLaunched`:
/// the child's name claim, then its mint.
#[derive(Clone, Debug, PartialEq)]
pub struct Mint {
    pub claim: Keyed<ClaimName>,
    pub mint: Keyed<MintSession>,
}

/// The launch (K2): the owner's `ChildLaunched` mints the child, for its
/// owner, in the target store, its name claimed first (as every mint's is).
/// `output_dir` is where the target project puts the child's outputs (the
/// application's, from the target's config). Keyed `claim:<owner>:<child>`
/// and `mint:<owner>:<child>`.
pub fn launch(owner: &SessionRef, launched: &ChildLaunched, output_dir: &str) -> Option<Mint> {
    let _ = (owner, launched, output_dir);
    None
}

/// The launch's compensation: the claim or the mint was refused (`reason`,
/// its code), so the owner stops holding the child. Keyed
/// `launch-failed:<owner>:<child>`.
pub fn launch_failed(
    owner: &SessionRef,
    child: &SessionRef,
    reason: &str,
) -> Option<KeyedFact<RecordChildLaunchFailed>> {
    let _ = (owner, child, reason);
    None
}

// ---------------------------------------------------------------- destroy

/// What a destroy sends before its purge (rulings 94, 98; M3).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Destroy {
    /// A close to each child the destroyed session holds.
    pub close: Vec<Keyed<CloseSession>>,
    /// Its owner's forgetting of it, if it has an owner.
    pub forget: Option<Keyed<ForgetChild>>,
}

/// The destroy of `target`: a purge emits no event (K3), so the children it
/// holds are closed first, `owner-destroyed:<target>:<child>`, and its owner
/// forgets it, `destroy:<target>`.
pub fn destroy(target: &SessionRef, owner: Option<&SessionRef>, children: &[Child]) -> Destroy {
    let _ = (target, owner, children);
    Destroy::default()
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
