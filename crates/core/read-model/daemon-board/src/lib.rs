//! The user daemon's board (D18, D28 S-d), of every project's streams in the
//! one store (T14): every session
//! stream folded as the session aggregate folds it, and every run stream as
//! the run aggregate folds it, plus the turns each run has ended (which the
//! run's own state forgets). The daemon's policy runner plans from it; it
//! decides nothing itself.
//!
//! The streams are caught up one at a time, in no order across them (there is
//! no global position): a reader never assumes a run's stream was folded
//! after its session's. A book from sequence 0 starts a stream afresh; a book
//! with no event and `next_sequence` 0 says the stream is gone (purged), and
//! it leaves the board.
//!
//! It is HELD in memory ([`catch_up`] is a `CatchUp::new`), rebuilt from each
//! stream's start by a daemon's first step. On the projector port it is a
//! [`Held`] (S32): the same instance is registered in the daemon's kernel,
//! answering a session stream's state (`ctxloom.sessions.v1.SessionState`)
//! as its view.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{
    Any, CatchUp, Domain, PageContext, Payload, PayloadError, ProjectorHandler,
};
use held_projection::{Fill, Held, Lock, Model};
use runs::{RunEvent, RunState, RUN_DOMAIN};
use sessions::{
    ChildLaunched, SessionAggregate, SessionEvent, SessionState, Urgency, SESSION_DOMAIN,
};

/// A turn a run ended: its number, the messages it carried, and whether it
/// failed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EndedTurn {
    pub turn: u64,
    pub message_ids: Vec<String>,
    pub failed: bool,
}

/// One run stream: its folded state and the turns it ended, oldest first.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct RunFold {
    pub state: RunState,
    pub ended_turns: Vec<EndedTurn>,
}

impl RunFold {
    /// Enqueued and not ended.
    pub fn live(&self) -> bool {
        self.state.mode.is_some() && self.state.ended.is_none()
    }
}

/// A message a session was sent, as its `MessageSent` says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Message {
    pub urgency: Urgency,
    pub text: String,
}

/// One session stream: its aggregate, and what of its events the aggregate's
/// state does not keep (the launches it made, the messages it was sent, the
/// clears it was asked).
#[derive(Default)]
struct SessionFold {
    aggregate: SessionAggregate,
    launched: Vec<ChildLaunched>,
    messages: BTreeMap<String, Message>,
    clears: Vec<String>,
}

impl SessionFold {
    fn fold(&mut self, event: &SessionEvent) {
        match event {
            SessionEvent::ChildLaunched(launched) => self.launched.push(launched.clone()),
            SessionEvent::MessageSent(sent) => {
                self.messages.insert(
                    sent.message_id.clone(),
                    Message {
                        urgency: sent.urgency,
                        text: sent.text.clone(),
                    },
                );
            }
            SessionEvent::ClearRequested(clear) => self.clears.push(clear.clear_id.clone()),
            _ => {}
        }
    }
}

/// A project's sessions and runs.
#[derive(Default)]
pub struct Board {
    /// Each session stream, by its root's text.
    sessions: BTreeMap<String, SessionFold>,
    /// Each run stream, by its root's text.
    runs: BTreeMap<String, RunFold>,
}

/// A stream root as UUID text; a root that is no UUID is empty.
fn root_text(book: &EventBook) -> String {
    book.cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

/// The text of the run root of `session`'s `incarnation`.
pub fn run_text(session: &str, incarnation: u32) -> String {
    let root = runs::run_root(session, incarnation);
    uuid::Uuid::from_slice(&root.value)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

impl Board {
    /// An empty board.
    pub fn new() -> Board {
        Board::default()
    }

    /// Fold `book`, one stream's events after the last folded of it.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let domain = book.cover.as_ref().map_or("", |c| c.domain.as_str());
        let root = root_text(book);
        let Some(first) = book.pages.first() else {
            if book.next_sequence == 0 {
                self.sessions.remove(&root);
                self.runs.remove(&root);
            }
            return Ok(());
        };
        let fresh = first.sequence == 0;
        match domain {
            SESSION_DOMAIN => {
                let mut session = match fresh {
                    true => SessionFold::default(),
                    false => self.sessions.remove(&root).unwrap_or_default(),
                };
                for (page, event) in events(book) {
                    let at = PageContext {
                        cover: book.cover.as_ref(),
                        sequence: page.sequence,
                        meta: page.meta.as_ref(),
                    };
                    let event = SessionEvent::from_any(event)?;
                    session.fold(&event);
                    SessionAggregate::apply(&mut session.aggregate, &event, &at);
                }
                self.sessions.insert(root, session);
            }
            RUN_DOMAIN => {
                let mut run = match fresh {
                    true => RunFold::default(),
                    false => self.runs.remove(&root).unwrap_or_default(),
                };
                for (page, event) in events(book) {
                    let event = RunEvent::from_any(event)?;
                    match &event {
                        RunEvent::TurnCompletedRecorded(done) => run.ended_turns.push(EndedTurn {
                            turn: done.turn,
                            message_ids: done.message_ids.clone(),
                            failed: false,
                        }),
                        RunEvent::TurnFailedRecorded(failed) => run.ended_turns.push(EndedTurn {
                            turn: failed.turn,
                            message_ids: failed.message_ids.clone(),
                            failed: true,
                        }),
                        _ => {}
                    }
                    runs::apply(&mut run.state, &event, page.sequence);
                }
                self.runs.insert(root, run);
            }
            _ => {}
        }
        Ok(())
    }

    /// Every session, by its stable id's text, in id order.
    pub fn sessions(&self) -> impl Iterator<Item = (&str, &SessionState)> {
        self.sessions
            .iter()
            .map(|(id, session)| (id.as_str(), session.aggregate.state()))
    }

    /// The session whose stable id is `id`.
    pub fn session(&self, id: &str) -> Option<&SessionState> {
        self.sessions
            .get(id)
            .map(|session| session.aggregate.state())
    }

    /// Every child `owner` (its id) launched, in the order launched, held or
    /// not (its state's `children` says which it still holds).
    pub fn launched(&self, owner: &str) -> &[ChildLaunched] {
        self.sessions
            .get(owner)
            .map_or(&[], |session| session.launched.as_slice())
    }

    /// The message `message_id` sent to `session`.
    pub fn message(&self, session: &str, message_id: &str) -> Option<&Message> {
        self.sessions.get(session)?.messages.get(message_id)
    }

    /// The clears `session` was asked, by id, in the order asked.
    pub fn clears(&self, session: &str) -> &[String] {
        self.sessions
            .get(session)
            .map_or(&[], |session| session.clears.as_slice())
    }

    /// The session whose harp is `harp`.
    pub fn find(&self, harp: &str) -> Option<(&str, &SessionState)> {
        self.sessions().find(|(_, state)| state.harp == harp)
    }

    /// The run of `session`'s `incarnation`, if its stream holds one.
    pub fn run(&self, session: &str, incarnation: u32) -> Option<&RunFold> {
        self.runs.get(&run_text(session, incarnation))
    }
}

/// The event pages of `book` and their bodies; a framework meta page is no
/// domain's event.
fn events(
    book: &EventBook,
) -> impl Iterator<Item = (&angzarr_embedded::v1::EventPage, &angzarr_embedded::Any)> {
    book.pages.iter().filter_map(|page| match &page.body {
        Some(event_page::Body::Event(event)) => Some((page, event)),
        _ => None,
    })
}

impl Model for Board {
    const NAME: &'static str = "daemon-board";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        Board::project(self, book)
    }

    /// A session stream's state, as the session aggregate folds it; a run
    /// stream has no view here.
    fn view(&self, cover: &Cover) -> Option<Any> {
        if cover.domain != SESSION_DOMAIN {
            return None;
        }
        let root = cover.root.as_ref()?;
        let id = uuid::Uuid::from_slice(&root.value)
            .ok()?
            .hyphenated()
            .to_string();
        let state = sessions_proto::v1::SessionState::from(self.session(&id)?.clone());
        Any::from_msg(&state).ok()
    }
}

/// The board on angzarr-embedded's projector port: every clone is the same
/// fold ([`Held`]), so one instance is both the daemon's catch-up's and its
/// kernel's.
#[derive(Clone, Default)]
pub struct Boards(Held<Board>);

impl Boards {
    /// An empty board whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> Boards {
        Boards(Held::new(Some(fill)))
    }

    /// The board, locked.
    pub fn board(&self) -> Lock<'_, Board> {
        self.0.lock()
    }
}

impl ProjectorHandler for Boards {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// The domains the board folds.
pub const SOURCES: [&str; 2] = [SESSION_DOMAIN, RUN_DOMAIN];

/// An empty board on the projector port, held in memory, to catch up from a
/// store.
pub fn catch_up() -> CatchUp<Boards> {
    caught_up(Boards::default())
}

/// `boards` caught up from a store: its catch-up, sharing its fold (S32).
pub fn caught_up(boards: Boards) -> CatchUp<Boards> {
    CatchUp::new(&SOURCES, boards).expect("the board reads the session and run domains")
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
