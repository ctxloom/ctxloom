//! The pending messages (D28 K3): a read model a projector folds from the
//! session and run streams. A message is pending from its `MessageSent` on
//! the recipient session until a `TurnQueued` on one of the session's runs
//! carries its id; a session's close drops what it still has pending (no turn
//! will carry it: the record of it stays in the stream).
//!
//! The streams are caught up one at a time, in no order across them (there is
//! no global position, S-d), so a run's `TurnQueued` may be folded before the
//! `MessageSent` it consumes: the consumed ids are kept per run, and a message
//! is pending when no run of its session consumed it, whichever came first.
//!
//! Nothing decides from it: the delivery policy reads it to choose what a
//! `QueueTurn` carries, and the run's turn gate and the command ids
//! (`send:<message_id>`) decide. `session show` lists it (D30).
//!
//! It is HELD in memory ([`catch_up`] is a `CatchUp::new`), rebuilt from each
//! stream's start by a process's first step, like `pending-launches`.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, EventBook};
use angzarr_embedded::{CatchUp, Payload, PayloadError, ProjectorHandler};
use runs::{RunEvent, RUN_DOMAIN};
use sessions::{SessionEvent, SessionRef, Urgency, SESSION_DOMAIN};

/// A message sent a session that no turn has carried yet.
#[derive(Clone, Debug, PartialEq)]
pub struct PendingMessage {
    pub message_id: String,
    pub urgency: Urgency,
    pub text: String,
    /// The session that sent it; `None`: the user.
    pub sender: Option<SessionRef>,
}

/// One session's messages, as its stream holds them.
#[derive(Default)]
struct Inbound {
    /// Every message sent it, in the order sent.
    sent: Vec<PendingMessage>,
    /// Closed: nothing it was sent is owed any more.
    closed: bool,
}

/// What one run stream says: its session, and the message ids its turns
/// carried.
#[derive(Default)]
struct Carried {
    session: String,
    ids: BTreeSet<String>,
}

/// The pending messages.
#[derive(Default)]
pub struct Pending {
    /// Each session's messages, by its id text.
    sessions: BTreeMap<String, Inbound>,
    /// Each run stream's carried ids, by its root.
    runs: BTreeMap<Vec<u8>, Carried>,
}

fn root_of(book: &EventBook) -> Vec<u8> {
    book.cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default()
}

/// A stream root as the text a run names its session by.
fn text_of(root: &[u8]) -> String {
    uuid::Uuid::from_slice(root)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

impl Pending {
    /// An empty model.
    pub fn new() -> Pending {
        Pending::default()
    }

    /// Fold `book`, one stream's events after the last projected of it, as the
    /// projector port hands it: a book from sequence 0 starts the stream
    /// afresh; a book with no event and `next_sequence` 0 says the stream is
    /// gone (purged), and it leaves the model.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let domain = book.cover.as_ref().map_or("", |c| c.domain.as_str());
        let root = root_of(book);
        let fresh = book.pages.first().is_none_or(|page| page.sequence == 0);
        match domain {
            RUN_DOMAIN => {
                if fresh {
                    self.runs.remove(&root);
                }
                for event in events(book) {
                    self.fold_run(&root, RunEvent::from_any(event)?);
                }
            }
            SESSION_DOMAIN => {
                let session = text_of(&root);
                if fresh {
                    self.sessions.remove(&session);
                }
                for event in events(book) {
                    self.fold_session(&session, SessionEvent::from_any(event)?);
                }
            }
            _ => {}
        }
        Ok(())
    }

    fn fold_run(&mut self, root: &[u8], event: RunEvent) {
        match event {
            RunEvent::RunEnqueued(enqueued) => {
                self.runs.entry(root.to_vec()).or_default().session = enqueued.session;
            }
            RunEvent::TurnQueued(queued) => {
                self.runs
                    .entry(root.to_vec())
                    .or_default()
                    .ids
                    .extend(queued.message_ids);
            }
            _ => {}
        }
    }

    fn fold_session(&mut self, session: &str, event: SessionEvent) {
        match event {
            SessionEvent::MessageSent(sent) => {
                self.sessions
                    .entry(session.to_string())
                    .or_default()
                    .sent
                    .push(PendingMessage {
                        message_id: sent.message_id,
                        urgency: sent.urgency,
                        text: sent.text,
                        sender: sent.sender,
                    });
            }
            SessionEvent::SessionClosed(_) => {
                self.sessions.entry(session.to_string()).or_default().closed = true;
            }
            _ => {}
        }
    }

    /// Whether a run of `session` carried `message_id`.
    fn carried(&self, session: &str, message_id: &str) -> bool {
        self.runs
            .values()
            .any(|run| run.session == session && run.ids.contains(message_id))
    }

    /// The messages `session` (its id text) was sent and no turn has carried,
    /// in the order sent; none once it is closed.
    pub fn pending(&self, session: &str) -> Vec<PendingMessage> {
        let Some(inbound) = self.sessions.get(session) else {
            return Vec::new();
        };
        if inbound.closed {
            return Vec::new();
        }
        inbound
            .sent
            .iter()
            .filter(|message| !self.carried(session, &message.message_id))
            .cloned()
            .collect()
    }

    /// Every session with a message pending, by its id text, and its pending
    /// messages: what a driver acts on.
    pub fn owed(&self) -> Vec<(String, Vec<PendingMessage>)> {
        self.sessions
            .keys()
            .map(|session| (session.clone(), self.pending(session)))
            .filter(|(_, pending)| !pending.is_empty())
            .collect()
    }
}

/// The event bodies of `book`; a framework meta page is no domain's event.
fn events(book: &EventBook) -> impl Iterator<Item = &angzarr_embedded::Any> {
    book.pages.iter().filter_map(|page| match &page.body {
        Some(event_page::Body::Event(event)) => Some(event),
        _ => None,
    })
}

/// The model on angzarr-embedded's projector port.
#[derive(Default)]
pub struct PendingMessages(Mutex<Pending>);

impl PendingMessages {
    /// The model, locked.
    pub fn pending(&self) -> MutexGuard<'_, Pending> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl ProjectorHandler for PendingMessages {
    fn name(&self) -> &str {
        "pending-messages"
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let events = request.events.clone().unwrap_or_default();
        self.pending()
            .project(&events)
            .map_err(|error| bug(&error))?;
        Ok(ProjectResponse { projection: None })
    }
}

/// An empty model on the projector port, held in memory, to catch up from a
/// store.
pub fn catch_up() -> CatchUp<PendingMessages> {
    CatchUp::new(&[SESSION_DOMAIN, RUN_DOMAIN], PendingMessages::default())
        .expect("the pending messages read the session and run domains")
}

/// `error`, an event the model cannot read, as the projector port's bug: its
/// code, and its text args (the type URL it names).
fn bug(error: &PayloadError) -> CodedError {
    use angzarr_embedded::angzarr_diagnostic::{Arg, Diagnostic as _};
    let args = error
        .args()
        .into_iter()
        .filter_map(|(name, arg)| match arg {
            Arg::Str(value) => Some((name.to_string(), value)),
            _ => None,
        });
    CodedError::bug(error.code(), args)
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
