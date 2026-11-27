//! A session's timeline (`ctxloom top`'s log): one time-ordered list per
//! session of what happened to it, folded from the streams that already
//! record it, with nothing of its own persisted:
//!
//! - its lifecycle, from the session stream (started, exited with its
//!   cause, re-hosted, restarted, closed, reopened, a launch that failed);
//! - its asks, from the approvals streams (asked, settled);
//! - its transcript entries, the same typed entries the session index folds
//!   from the capture events (`session_index::Projection`), each at its
//!   event's recorded instant.
//!
//! Every entry is one `ctxloom.v1.LogEntry`: the daemon's own (lifecycle, ask)
//! and the engine-neutral transcript's alike, naming no session, harp or
//! engine; `seq` is the order the timeline received it, from 0.
//!
//! Held in memory on the projector port like the other read models
//! ([`SessionTimelines`]), woken watchers see each change pushed.

use std::sync::Arc;

use std::collections::BTreeMap;

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, Payload, PayloadError, ProjectorHandler, Timestamp};
use approvals::{ApprovalEvent, APPROVALS_DOMAIN};
use daemon_proto::v1::{self, log_ask, log_entry::Entry as Arm, log_lifecycle};
use held_projection::{Fill, Held, Lock, Model};
use session_index::Projection;
use sessions::{SessionEvent, DRAINED, REHOSTED, RESTARTED, SESSION_DOMAIN};

/// The projector's name.
pub const SESSION_TIMELINE: &str = "session-timeline";

/// The domains the timeline folds.
pub const SOURCES: [&str; 2] = [SESSION_DOMAIN, APPROVALS_DOMAIN];

/// The most entries kept for one session; the oldest go first.
pub const KEPT: usize = 2000;

/// Whether `entry` changes a session's status: its lifecycle (started,
/// exited, re-hosted, restarted, closed, reopened) and its asks (asked,
/// settled); a transcript entry or any other lifecycle word is no change.
fn changes_status(entry: &v1::LogEntry) -> bool {
    use log_lifecycle::Kind;
    match &entry.entry {
        Some(Arm::Lifecycle(lifecycle)) => matches!(
            Kind::try_from(lifecycle.kind),
            Ok(Kind::Started
                | Kind::Exited
                | Kind::Rehosted
                | Kind::Restarted
                | Kind::Closed
                | Kind::Reopened)
        ),
        Some(Arm::Ask(_)) => true,
        _ => false,
    }
}

/// One entry of a session's timeline.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    /// The order this timeline received it in, from 1: what a watch says it
    /// has told up to.
    pub n: u64,
    pub time: Timestamp,
    /// What it says, typed; its `seq` is `n - 1`.
    pub log: v1::LogEntry,
}

/// One session's entries, oldest first.
#[derive(Default)]
struct Timeline {
    entries: Vec<Entry>,
    received: u64,
}

impl Timeline {
    fn push(&mut self, time: Timestamp, mut log: v1::LogEntry) {
        self.received += 1;
        log.time = Some(time.into());
        log.seq = self.received - 1;
        let entry = Entry {
            n: self.received,
            time,
            log,
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

fn log(arm: Arm) -> v1::LogEntry {
    v1::LogEntry {
        entry: Some(arm),
        ..v1::LogEntry::default()
    }
}

fn lifecycle_entry(kind: log_lifecycle::Kind, run: u32, cause: &str) -> v1::LogEntry {
    log(Arm::Lifecycle(v1::LogLifecycle {
        kind: kind as i32,
        run,
        cause: cause.to_string(),
        engine: String::new(),
    }))
}

/// What the daemon says of a lifecycle event; none for one that is not.
fn lifecycle(event: &SessionEvent) -> Option<v1::LogEntry> {
    use log_lifecycle::Kind;
    Some(match event {
        SessionEvent::SessionMinted(minted) => log(Arm::Lifecycle(v1::LogLifecycle {
            kind: Kind::Started as i32,
            run: 0,
            cause: String::new(),
            engine: minted.engine.clone(),
        })),
        SessionEvent::IncarnationOwed(owed) => {
            let kind = match owed.reason.as_str() {
                REHOSTED | DRAINED => Kind::Rehosted,
                RESTARTED => Kind::Restarted,
                "resume" => Kind::Resumed,
                _ => return None,
            };
            lifecycle_entry(kind, owed.incarnation, "")
        }
        SessionEvent::IncarnationEnded(ended) => {
            lifecycle_entry(Kind::Exited, ended.incarnation, &ended.cause)
        }
        SessionEvent::RestartOwed(owed) => lifecycle_entry(Kind::RestartOwed, owed.incarnation, ""),
        SessionEvent::SessionClosed(closed) => lifecycle_entry(Kind::Closed, 0, &closed.cause),
        SessionEvent::SessionReopened(reopened) => {
            lifecycle_entry(Kind::Reopened, 0, &reopened.cause)
        }
        SessionEvent::ChildLaunchFailed(failed) => {
            lifecycle_entry(Kind::LaunchFailed, 0, &failed.reason)
        }
        _ => return None,
    })
}

fn ask_entry(state: log_ask::State, ask: &str, fill: impl FnOnce(&mut v1::LogAsk)) -> v1::LogEntry {
    let mut entry = v1::LogAsk {
        state: state as i32,
        ask: ask.to_string(),
        ..v1::LogAsk::default()
    };
    fill(&mut entry);
    log(Arm::Ask(entry))
}

fn recorded_of(page: &angzarr_embedded::v1::EventPage) -> Timestamp {
    page.meta
        .as_ref()
        .and_then(|meta| meta.recorded_at)
        .unwrap_or_default()
}

impl TimelineIndex {
    /// Fold `book`, one stream's events after the last projected of it. A
    /// tombstone (a purge's `StreamClosed`) closes the stream: what the
    /// index held of it leaves. Another domain's book is none of its.
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
        for page in &book.pages {
            if tombstone::is(page) {
                self.sessions.remove(&id);
                self.transcripts.remove(&root);
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let recorded = recorded_of(page);
            let event = SessionEvent::from_any(event)?;
            if let SessionEvent::SessionMinted(_) = &event {
                self.transcripts.insert(root.clone(), Projection::new());
            }
            let timeline = self.sessions.entry(id.clone()).or_default();
            if let Some(entry) = lifecycle(&event) {
                timeline.push(recorded, entry);
            }
            if let Some(transcript) = self.transcripts.get_mut(&root) {
                if let Some(entry) = transcript.fold(page.sequence, recorded, &event) {
                    timeline.push(recorded, entry);
                }
            }
        }
        Ok(())
    }

    fn project_ask(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = root_of(book);
        for page in &book.pages {
            if tombstone::is(page) {
                self.asks.remove(&root);
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let recorded = recorded_of(page);
            let entry = match ApprovalEvent::from_any(event)? {
                ApprovalEvent::ApprovalAsked(asked) => {
                    self.asks
                        .insert(root.clone(), (asked.child.session, asked.ask_id.clone()));
                    ask_entry(log_ask::State::Asked, &asked.ask_id, |ask| {
                        ask.tool = asked.tool_name.clone();
                        ask.input_excerpt = asked.input_excerpt.clone();
                    })
                }
                ApprovalEvent::ApprovalAnswered(answered) => {
                    self.settled(&root, log_ask::State::Answered, |ask| {
                        ask.option = answered.option.clone();
                    })
                }
                ApprovalEvent::ApprovalExpired(_) => {
                    self.settled(&root, log_ask::State::Expired, |_| {})
                }
                ApprovalEvent::ApprovalWithdrawn(_) => {
                    self.settled(&root, log_ask::State::Withdrawn, |_| {})
                }
            };
            if let Some((session, _)) = self.asks.get(&root) {
                self.sessions
                    .entry(session.clone())
                    .or_default()
                    .push(recorded, entry);
            }
        }
        Ok(())
    }

    fn settled(
        &self,
        root: &[u8],
        state: log_ask::State,
        fill: impl FnOnce(&mut v1::LogAsk),
    ) -> v1::LogEntry {
        let ask = self.asks.get(root).map_or("", |(_, ask)| ask.as_str());
        ask_entry(state, ask, fill)
    }

    /// The entries of the session with the stable id `session`, oldest first.
    pub fn entries(&self, session: &str) -> Vec<Entry> {
        self.sessions
            .get(session)
            .map(|timeline| timeline.entries.clone())
            .unwrap_or_default()
    }

    /// When the session with the stable id `session` last changed status: the
    /// time of its latest lifecycle or ask entry ([`changes_status`]), a
    /// transcript entry or any other lifecycle word being no change. None when it has
    /// no such entry.
    pub fn state_changed_at(&self, session: &str) -> Option<Timestamp> {
        self.sessions
            .get(session)?
            .entries
            .iter()
            .rev()
            .find(|entry| changes_status(&entry.log))
            .map(|entry| entry.time)
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
