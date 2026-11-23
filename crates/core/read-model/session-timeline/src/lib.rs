//! A session's timeline (`ctxloom top`'s log): one time-ordered list per
//! session of what happened to it, folded from the streams that already
//! record it, with nothing of its own persisted:
//!
//! - its lifecycle, from the session stream (started, exited with its
//!   cause, re-hosted, restarted, closed, reopened, a launch that failed);
//! - its asks, from the approvals streams (asked, settled);
//! - its transcript lines, the same lines the session index folds from the
//!   capture events (`session_index::Projection`), each at its event's
//!   recorded instant.
//!
//! Held in memory on the projector port like the other read models
//! ([`SessionTimelines`]), woken watchers see each change pushed.

use std::sync::Arc;

use std::collections::BTreeMap;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, Payload, PayloadError, ProjectorHandler, Timestamp};
use approvals::{ApprovalEvent, APPROVALS_DOMAIN};
use held_projection::{Fill, Held, Lock, Model};
use session_index::Projection;
use sessions::{SessionEvent, DRAINED, REHOSTED, RESTARTED, SESSION_DOMAIN};

/// The projector's name.
pub const SESSION_TIMELINE: &str = "session-timeline";

/// The domains the timeline folds.
pub const SOURCES: [&str; 2] = [SESSION_DOMAIN, APPROVALS_DOMAIN];

/// The most entries kept for one session; the oldest go first.
pub const KEPT: usize = 2000;

/// What an entry says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Content {
    /// A transcript line, as the transcript has it (no newline).
    Transcript { line: String },
    /// An event of the daemon: a word and its text.
    Daemon { event: String, text: String },
}

/// One entry of a session's timeline.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    /// The order this timeline received it in, from 1: what a watch says it
    /// has told up to.
    pub n: u64,
    pub time: Timestamp,
    pub content: Content,
}

/// One session's entries, oldest first.
#[derive(Default)]
struct Timeline {
    entries: Vec<Entry>,
    received: u64,
}

impl Timeline {
    fn push(&mut self, time: Timestamp, content: Content) {
        self.received += 1;
        let entry = Entry {
            n: self.received,
            time,
            content,
        };
        let key = |t: &Timestamp| (t.seconds, t.nanos);
        // After every entry at the same instant: ties keep arrival order.
        let at = self
            .entries
            .partition_point(|held| key(&held.time) <= key(&time));
        self.entries.insert(at, entry);
        if self.entries.len() > KEPT {
            self.entries.remove(0);
        }
    }
}

/// Every session's timeline.
#[derive(Default)]
pub struct TimelineIndex {
    /// By the session's stable id (its stream root as UUID text).
    sessions: BTreeMap<String, Timeline>,
    /// Each session stream's transcript fold, by its root.
    transcripts: BTreeMap<Vec<u8>, Projection>,
    /// Each ask stream's session and name, by its root.
    asks: BTreeMap<Vec<u8>, (String, String)>,
}

fn root_of(book: &EventBook) -> Vec<u8> {
    book.cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default()
}

fn uid_of(root: &[u8]) -> String {
    uuid::Uuid::from_slice(root)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

fn said(event: &str, text: impl Into<String>) -> Content {
    Content::Daemon {
        event: event.to_string(),
        text: text.into(),
    }
}

/// What the daemon says of a lifecycle event; none for one that is not.
fn lifecycle(event: &SessionEvent) -> Option<Content> {
    Some(match event {
        SessionEvent::SessionMinted(minted) => {
            said("started", format!("started on {}", minted.engine))
        }
        SessionEvent::IncarnationOwed(owed) => match owed.reason.as_str() {
            REHOSTED | DRAINED => said("rehosted", format!("re-hosted, run {}", owed.incarnation)),
            RESTARTED => said("restarted", format!("restarted, run {}", owed.incarnation)),
            "resume" => said("resumed", format!("resumed, run {}", owed.incarnation)),
            _ => return None,
        },
        SessionEvent::IncarnationEnded(ended) => said(
            "exited",
            format!("run {} ended: {}", ended.incarnation, ended.cause),
        ),
        SessionEvent::RestartOwed(owed) => said(
            "restart_owed",
            format!("a restart is owed for run {}", owed.incarnation),
        ),
        SessionEvent::SessionClosed(closed) => said("closed", format!("closed: {}", closed.cause)),
        SessionEvent::SessionReopened(reopened) => {
            said("reopened", format!("reopened: {}", reopened.cause))
        }
        SessionEvent::ChildLaunchFailed(failed) => {
            said("launch_failed", format!("launch failed: {}", failed.reason))
        }
        _ => return None,
    })
}

fn recorded_of(page: &angzarr_embedded::v1::EventPage) -> Timestamp {
    page.meta
        .as_ref()
        .and_then(|meta| meta.recorded_at)
        .unwrap_or_default()
}

impl TimelineIndex {
    /// Fold `book`, one stream's events after the last projected of it; a
    /// book from sequence 0 starts the stream afresh, and a book with no
    /// event and `next_sequence` 0 says the stream is gone. Another domain's
    /// book is none of its.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        match book.cover.as_ref().map(|cover| cover.domain.as_str()) {
            Some(SESSION_DOMAIN) => self.project_session(book),
            Some(APPROVALS_DOMAIN) => self.project_ask(book),
            _ => Ok(()),
        }
    }

    fn project_session(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = root_of(book);
        let id = uid_of(&root);
        let fresh = book
            .pages
            .first()
            .map_or(book.next_sequence == 0, |page| page.sequence == 0);
        if fresh {
            self.sessions.remove(&id);
            self.transcripts.remove(&root);
        }
        for page in &book.pages {
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let recorded = recorded_of(page);
            let event = SessionEvent::from_any(event)?;
            if let SessionEvent::SessionMinted(minted) = &event {
                self.transcripts.insert(
                    root.clone(),
                    Projection::new(&minted.harp, &id, &minted.engine),
                );
            }
            let timeline = self.sessions.entry(id.clone()).or_default();
            if let Some(content) = lifecycle(&event) {
                timeline.push(recorded, content);
            }
            if let Some(transcript) = self.transcripts.get_mut(&root) {
                for line in transcript.fold(page.sequence, recorded.seconds, &event) {
                    timeline.push(
                        recorded,
                        Content::Transcript {
                            line: line.trim_end_matches('\n').to_string(),
                        },
                    );
                }
            }
        }
        Ok(())
    }

    fn project_ask(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = root_of(book);
        if book.pages.first().is_none_or(|page| page.sequence == 0) {
            self.asks.remove(&root);
        }
        for page in &book.pages {
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let recorded = recorded_of(page);
            let (word, text) = match ApprovalEvent::from_any(event)? {
                ApprovalEvent::ApprovalAsked(asked) => {
                    let text = format!(
                        "{}: {} {}",
                        asked.ask_id, asked.tool_name, asked.input_excerpt
                    );
                    self.asks
                        .insert(root.clone(), (asked.child.session, asked.ask_id));
                    ("approval_asked", text)
                }
                ApprovalEvent::ApprovalAnswered(answered) => (
                    "approval_settled",
                    self.settled(&root, &format!("answered {}", answered.option)),
                ),
                ApprovalEvent::ApprovalExpired(_) => {
                    ("approval_settled", self.settled(&root, "expired"))
                }
                ApprovalEvent::ApprovalWithdrawn(_) => {
                    ("approval_settled", self.settled(&root, "withdrawn"))
                }
            };
            if let Some((session, _)) = self.asks.get(&root) {
                self.sessions
                    .entry(session.clone())
                    .or_default()
                    .push(recorded, said(word, text));
            }
        }
        Ok(())
    }

    fn settled(&self, root: &[u8], how: &str) -> String {
        let ask = self.asks.get(root).map_or("", |(_, ask)| ask.as_str());
        format!("{ask}: {how}")
    }

    /// The entries of the session with the stable id `session`, oldest first.
    pub fn entries(&self, session: &str) -> Vec<Entry> {
        self.sessions
            .get(session)
            .map(|timeline| timeline.entries.clone())
            .unwrap_or_default()
    }

    /// The entries of `session` received after `n`, oldest first.
    pub fn after(&self, session: &str, n: u64) -> Vec<Entry> {
        self.entries(session)
            .into_iter()
            .filter(|entry| entry.n > n)
            .collect()
    }
}

impl Model for TimelineIndex {
    const NAME: &'static str = SESSION_TIMELINE;

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        TimelineIndex::project(self, book)
    }

    fn view(&self, _cover: &Cover) -> Option<Any> {
        None
    }
}

/// The timelines on the projector port: every clone is the same fold.
#[derive(Clone, Default)]
pub struct SessionTimelines(Held<TimelineIndex>);

impl SessionTimelines {
    /// An empty index whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> SessionTimelines {
        SessionTimelines(Held::new(Some(fill)))
    }

    /// The index, locked.
    pub fn index(&self) -> Lock<'_, TimelineIndex> {
        self.0.lock()
    }

    /// As the other read models': woken through `wake` each time a fold
    /// changes it.
    pub fn watch_into(&self, wake: std::sync::mpsc::Sender<()>) {
        self.0.watch_into(wake);
    }
}

impl ProjectorHandler for SessionTimelines {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// `model` caught up from a store, sharing its fold.
pub fn caught_up(model: SessionTimelines) -> CatchUp<SessionTimelines> {
    CatchUp::new(&SOURCES, model).expect("the timelines read the sessions and approvals domains")
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
