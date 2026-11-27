//! The user daemon's board (D18, D28 S-d), of every project's streams in the
//! one store (T14): every session
//! stream folded as the session aggregate folds it, and every run stream as
//! the run aggregate folds it, plus the turns each run has ended (which the
//! run's own state forgets). The daemon's policy runner plans from it; it
//! decides nothing itself.
//!
//! The streams are caught up one at a time, in no order across them (there is
//! no global position): a reader never assumes a run's stream was folded
//! after its session's. A tombstone (a purge's `StreamClosed`) closes a stream:
//! it leaves the board, and what follows starts afresh.
//!
//! It is HELD in memory ([`catch_up`] is a `CatchUp::new`), rebuilt from each
//! stream's start by a daemon's first step. On the projector port it is a
//! [`Held`] (S32): the same instance is registered in the daemon's kernel,
//! answering a session stream's state (`ctxloom.sessions.v1.SessionState`)
//! as its view.

mod hosting;

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{
    Any, CatchUp, Domain, PageContext, Payload, PayloadError, ProjectorHandler,
};
use held_projection::{Fill, Held, Lock, Model};
use runs::{RunEvent, RunState, TurnFailureClass, RUN_DOMAIN};
use sessions::{
    ChildLaunched, IncarnationEnded, SessionAggregate, SessionEvent, SessionState, Urgency,
    SESSION_DOMAIN,
};

pub use hosting::{Agent, RestartDue};

/// A turn a run ended: its number, the messages it carried, and whether it
/// failed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EndedTurn {
    pub turn: u64,
    pub message_ids: Vec<String>,
    pub failed: bool,
}

/// How a turn failed: its class and the engine's own words.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TurnFailure {
    pub class: TurnFailureClass,
    pub detail: String,
}

/// One run stream: its folded state and the turns it ended, oldest first.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct RunFold {
    pub state: RunState,
    pub ended_turns: Vec<EndedTurn>,
    /// How its last turn failed, until a later turn of it completed (step
    /// 9: `session show` says it).
    pub last_failure: Option<TurnFailure>,
    /// The words its end was recorded with (a launch that failed); empty
    /// before it ended.
    pub end_detail: String,
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
    /// Its last incarnation's end: the incarnation and its cause.
    last_end: Option<(u32, String)>,
    /// Every incarnation's end, in the order ended.
    ends: Vec<IncarnationEnded>,
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
            SessionEvent::IncarnationEnded(ended) => {
                self.last_end = Some((ended.incarnation, ended.cause.clone()));
                self.ends.push(ended.clone());
            }
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

    /// Fold `book`, one stream's events after the last folded of it. A
    /// tombstone closes the stream: it leaves the board.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let domain = book.cover.as_ref().map_or("", |c| c.domain.as_str());
        let root = root_text(book);
        if book.pages.is_empty() {
            return Ok(());
        }
        match domain {
            SESSION_DOMAIN => {
                let mut session = self.sessions.remove(&root).unwrap_or_default();
                let mut closed = false;
                for page in &book.pages {
                    if tombstone::is(page) {
                        session = SessionFold::default();
                        closed = true;
                        continue;
                    }
                    let Some(event_page::Body::Event(event)) = &page.body else {
                        continue;
                    };
                    closed = false;
                    let at = PageContext {
                        cover: book.cover.as_ref(),
                        sequence: page.sequence,
                        meta: page.meta.as_ref(),
                    };
                    let event = SessionEvent::from_any(event)?;
                    session.fold(&event);
                    SessionAggregate::apply(&mut session.aggregate, &event, &at);
                }
                if !closed {
                    self.sessions.insert(root, session);
                }
            }
            RUN_DOMAIN => {
                let mut run = self.runs.remove(&root).unwrap_or_default();
                let mut closed = false;
                for page in &book.pages {
                    if tombstone::is(page) {
                        run = RunFold::default();
                        closed = true;
                        continue;
                    }
                    let Some(event_page::Body::Event(event)) = &page.body else {
                        continue;
                    };
                    closed = false;
                    let event = RunEvent::from_any(event)?;
                    match &event {
                        RunEvent::TurnCompletedRecorded(done) => {
                            run.last_failure = None;
                            run.ended_turns.push(EndedTurn {
                                turn: done.turn,
                                message_ids: done.message_ids.clone(),
                                failed: false,
                            });
                        }
                        RunEvent::TurnFailedRecorded(failed) => {
                            run.last_failure = Some(TurnFailure {
                                class: failed.class,
                                detail: failed.detail.clone(),
                            });
                            run.ended_turns.push(EndedTurn {
                                turn: failed.turn,
                                message_ids: failed.message_ids.clone(),
                                failed: true,
                            });
                        }
                        RunEvent::RunEnded(ended) => run.end_detail = ended.detail.clone(),
                        _ => {}
                    }
                    runs::apply(&mut run.state, &event, page.sequence);
                }
                if !closed {
                    self.runs.insert(root, run);
                }
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

    /// The last incarnation of `session` that ended, and its cause (`orphaned`
    /// for one whose run's owner was lost, T19); none before any ended.
    pub fn last_end(&self, session: &str) -> Option<(u32, &str)> {
        self.sessions
            .get(session)?
            .last_end
            .as_ref()
            .map(|(incarnation, cause)| (*incarnation, cause.as_str()))
    }

    /// Every incarnation of `session` that ended, in the order ended.
    pub fn ends(&self, session: &str) -> &[IncarnationEnded] {
        self.sessions
            .get(session)
            .map_or(&[], |session| session.ends.as_slice())
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
