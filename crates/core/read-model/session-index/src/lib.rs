//! The session index: a read model a projector folds from a project's
//! session streams. It is purely derived, so it is no aggregate: it projects
//! every session stream in a project's store (the store is the collection),
//! holding every session there (a destroyed one's stream is gone, and so is
//! it).
//!
//! - [`SessionIndex`] is the read model, folded one stream's book at a time
//!   ([`SessionIndex::project`]); [`SessionIndexes`] is it on
//!   angzarr-embedded's projector port, which a `CatchUp` catches up from
//!   the store ([`SessionIndex::catch_up`]), each stream handed what it is
//!   owed, from where the index is on it. The index keeps no cursor across
//!   streams: nothing numbers across streams.
//! - [`SessionRecord`] is what the store records of a session; [`View`]
//!   joins it with what only the outside knows (its state from its lock) and
//!   its title from its transcript, and is what `session list`, `search` and
//!   `show` render from.
//! - On the projector port it is a [`Held`] (S32): the same instance may be
//!   registered in the kernel, where a `HandleSync` naming `session-index` is
//!   answered the command's session as its resource message,
//!   `ctxloom.daemon.v1.Session`, named by its project's harp and its own
//!   (S42, S54: the index folds the projects' streams for their harps).
//! - A session's transcript (D27's lines) is folded from its capture events
//!   too ([`SessionIndex::transcript`], [`Projection`]): no file holds it
//!   (owner ruling 87a), so a read, live or not, sees what was delivered.
//! - [`title_of`] is a session's title, [`Retention`] the rule that keeps its
//!   transcript, and [`stamp`] the instants as the rows show them.

mod projection;
mod retention;
pub mod stamp;
mod title;
mod transcript;
mod view;

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{
    Any, CatchUp, Domain, PageContext, Payload, PayloadError, ProjectorHandler, Timestamp,
};
use held_projection::{Fill, Held, Lock, Model};
use sessions::{
    Child, Origin, ProjectEvent, Rotation, SessionAggregate, SessionEvent, SessionRef,
    PROJECTS_DOMAIN, SESSION_DOMAIN,
};

pub use pending_messages::PendingMessage;
pub use projection::Projection;
pub use retention::{ended, Retained, Retention};
pub use title::{first_line_summary, title_of, TITLE_BYTES};
pub use transcript::{entry_line, rfc3339, Speaker};
pub use view::{only_state, Captured, State, View, NO_SUMMARY, STATES};

/// What the store records of one session, as its events fold.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SessionRecord {
    /// The stable id: the stream's root, as a UUID.
    pub id: String,
    pub harp: String,
    pub project_id: String,
    pub project_dir: String,
    pub output_dir: String,
    pub engine: String,
    pub origin: Option<Origin>,
    /// The engine's own session key and transcript for the current binding.
    pub native_id: String,
    pub transcript_path: String,
    pub engine_version: String,
    /// Every binding displaced, oldest first.
    pub rotations: Vec<Rotation>,
    pub kept: bool,
    pub started_at: Timestamp,
    pub ended_at: Option<Timestamp>,
    /// The highest incarnation the session owed: its runs are
    /// UUIDv5(session, 1..=incarnation) (a destroy purges each, ruling 94b).
    pub incarnation: u32,
    /// When its hooks last delivered something to its transcript: the newest
    /// capture event's instant (owner ruling 87a: its last activity).
    pub last_captured: Option<Timestamp>,
    /// The owner a child was minted for (D26); `None` for a top-level
    /// session.
    pub owner: Option<SessionRef>,
    /// Whether it is closed (K1).
    pub closed: bool,
    /// The children it holds as an owner (K2): the owner-to-children view,
    /// per owner stream.
    pub children: Vec<Child>,
    /// The title `session rename` set (S55); empty when none is.
    pub title: String,
    /// Its resource name, `projects/{project}/sessions/{session}` (slice 2
    /// B3); empty while its project has no harp.
    pub name: String,
}

/// A project as its stream says it (S54, and its registration, owner
/// 2026-11-12): its id, the harp it was named with, and the root and VCS
/// identity a daemon client registered it from (empty until registered).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ProjectRow {
    pub project_id: String,
    pub harp: String,
    pub root: String,
    pub git_common_dir: String,
    pub root_commit: String,
}

/// One index of a project's sessions.
#[derive(Default)]
pub struct SessionIndex {
    /// Every session stream projected, by root: the session as its events
    /// leave it.
    streams: BTreeMap<Vec<u8>, SessionAggregate>,
    /// Each session's transcript (owner ruling 87a), by root: its folding
    /// and the text so far.
    transcripts: BTreeMap<Vec<u8>, Folded>,
    /// Each project's harp (S54), by its project stream's root: its id and
    /// the harp it was named with.
    projects: BTreeMap<Vec<u8>, ProjectRow>,
}

/// A session's transcript as folded so far.
struct Folded {
    projection: Projection,
    text: String,
    /// The instant of the newest line.
    last: Option<Timestamp>,
}

impl SessionIndex {
    /// An empty index.
    pub fn new() -> SessionIndex {
        SessionIndex::default()
    }

    /// An empty index on the projector port, to catch up from a store.
    pub fn catch_up() -> CatchUp<SessionIndexes> {
        SessionIndex::caught_up(SessionIndexes::default())
    }

    /// `index` caught up from a store: its catch-up, sharing its fold (S32).
    pub fn caught_up(index: SessionIndexes) -> CatchUp<SessionIndexes> {
        CatchUp::new(&SOURCES, index).expect("a session index reads the sessions and projects")
    }

    /// The harp the project `project_id` was named with (S54), if it was.
    pub fn project_harp(&self, project_id: &str) -> Option<&str> {
        self.projects
            .values()
            .find(|row| row.project_id == project_id && !row.harp.is_empty())
            .map(|row| row.harp.as_str())
    }

    /// The project named `harp`, as its stream says it.
    pub fn project_named_by(&self, harp: &str) -> Option<ProjectRow> {
        self.projects.values().find(|row| row.harp == harp).cloned()
    }

    /// Fold a project stream's `book`: the harp it names its project with,
    /// and its registration.
    fn project_named(&mut self, root: Vec<u8>, book: &EventBook) -> Result<(), PayloadError> {
        if book.pages.is_empty() && book.next_sequence == 0 {
            self.projects.remove(&root);
        }
        for page in &book.pages {
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let row = self.projects.entry(root.clone()).or_default();
            match ProjectEvent::from_any(event)? {
                ProjectEvent::ProjectNamed(named) if row.harp.is_empty() => {
                    row.project_id = named.project_id;
                    row.harp = named.harp;
                }
                ProjectEvent::ProjectNamed(_) => {}
                ProjectEvent::ProjectRegistered(registered) if row.root.is_empty() => {
                    row.project_id = registered.project_id;
                    row.root = registered.root;
                    row.git_common_dir = registered.git_common_dir;
                    row.root_commit = registered.root_commit;
                }
                ProjectEvent::ProjectRegistered(_) => {}
                ProjectEvent::ProjectIdentityAdopted(_) => {}
            }
        }
        Ok(())
    }

    /// Fold `book`, one session stream's events after the last projected of
    /// it, as the projector port hands it: a book from sequence 0 starts the
    /// stream afresh; a book with no event at 0 says the stream is gone
    /// (purged whole), and its session leaves the index. An event the sessions
    /// domain cannot read folds nothing and is the fold's error.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = root(book.cover.as_ref());
        if book
            .cover
            .as_ref()
            .is_some_and(|c| c.domain == PROJECTS_DOMAIN)
        {
            return self.project_named(root, book);
        }
        let Some(first) = book.pages.first() else {
            if book.next_sequence == 0 {
                self.streams.remove(&root);
                self.transcripts.remove(&root);
            }
            return Ok(());
        };
        let mut session = match first.sequence {
            0 => {
                self.transcripts.remove(&root);
                SessionAggregate::default()
            }
            _ => self.streams.remove(&root).unwrap_or_default(),
        };
        for page in &book.pages {
            // A framework meta-event is no session event: only a domain's
            // event is folded.
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let at = PageContext {
                cover: book.cover.as_ref(),
                sequence: page.sequence,
                meta: page.meta.as_ref(),
            };
            let event = SessionEvent::from_any(event)?;
            SessionAggregate::apply(&mut session, &event, &at);
            let recorded = page
                .meta
                .as_ref()
                .and_then(|meta| meta.recorded_at)
                .unwrap_or_default();
            self.transcribe(&root, &session, page.sequence, recorded, &event);
        }
        self.streams.insert(root, session);
        Ok(())
    }

    /// Fold `event`, page `sequence` of the stream rooted at `root` recorded
    /// at `recorded`, into the session's transcript.
    fn transcribe(
        &mut self,
        root: &[u8],
        session: &SessionAggregate,
        sequence: u64,
        recorded: Timestamp,
        event: &SessionEvent,
    ) {
        let state = session.state();
        let folded = self
            .transcripts
            .entry(root.to_vec())
            .or_insert_with(|| Folded {
                projection: Projection::new(&state.harp, &uuid_text(root), &state.engine),
                text: String::new(),
                last: None,
            });
        let lines = folded.projection.fold(sequence, recorded.seconds, event);
        if !lines.is_empty() {
            folded.last = Some(recorded);
            folded.text.extend(lines);
        }
    }

    /// The record of the session folded from the stream rooted at `root`.
    fn record_of(&self, root: &[u8], session: &SessionAggregate) -> SessionRecord {
        let record = record_of(root, session);
        SessionRecord {
            last_captured: self.transcripts.get(root).and_then(|t| t.last),
            name: self.name_of(&record),
            ..record
        }
    }

    /// Every session, most recently started first, a tie by harp.
    pub fn records(&self) -> Vec<SessionRecord> {
        let mut records: Vec<SessionRecord> = self
            .streams
            .iter()
            .map(|(root, session)| self.record_of(root, session))
            .collect();
        records.sort_by(|a, b| {
            let started = |r: &SessionRecord| (r.started_at.seconds, r.started_at.nanos);
            started(b)
                .cmp(&started(a))
                .then_with(|| a.harp.cmp(&b.harp))
        });
        records
    }

    /// The session named `harp`.
    pub fn find(&self, harp: &str) -> Option<SessionRecord> {
        self.records().into_iter().find(|r| r.harp == harp)
    }

    /// The session whose current binding is `native_id`, or whose lineage
    /// holds it: a key an old vendor transcript still names resolves to the
    /// session it was once bound to (Go's `FindBySessionID`).
    pub fn find_by_native_id(&self, native_id: &str) -> Option<SessionRecord> {
        if native_id.is_empty() {
            return None;
        }
        self.records().into_iter().find(|r| {
            r.native_id == native_id || r.rotations.iter().any(|x| x.native_id == native_id)
        })
    }

    /// The transcript of the session with the stable id `id` (owner ruling
    /// 87a: no transcript file; the session's events in the store are the
    /// record): D27's lines, one per capture event, `seq` counting them, as
    /// far as the store held when this index caught up. `None` when the
    /// session has no capture event (or no such session).
    ///
    pub fn transcript(&self, id: &str) -> Option<String> {
        self.transcripts
            .iter()
            .find(|(root, _)| uuid_text(root) == id)
            .map(|(_, folded)| folded.text.clone())
            .filter(|text| !text.is_empty())
    }

    /// The session with the stable id `id`.
    pub fn find_by_id(&self, id: &str) -> Option<SessionRecord> {
        self.streams
            .iter()
            .map(|(root, session)| self.record_of(root, session))
            .find(|r| r.id == id)
    }
}

/// The domains the index folds: the sessions, and the projects for their
/// harps (S54).
pub const SOURCES: [&str; 2] = [SESSION_DOMAIN, PROJECTS_DOMAIN];

impl SessionIndex {
    /// The resource name of `record` (S42): its project's harp and its own,
    /// `projects/{project}/sessions/{session}`; empty while its project has
    /// no harp.
    pub fn name_of(&self, record: &SessionRecord) -> String {
        self.project_harp(&record.project_id)
            .and_then(|project| sessions::SessionName::new(project, &record.harp).ok())
            .map(|name| name.to_string())
            .unwrap_or_default()
    }

    /// The resource name of the session `session` refers to, where the index
    /// holds it.
    fn name_of_ref(&self, session: &SessionRef) -> String {
        self.find_by_id(&session.session)
            .map(|record| self.name_of(&record))
            .unwrap_or_default()
    }

    /// The session `record` as its resource message (S32): what a `Get` of
    /// it returns, as far as the store says (its state is its lock's, which
    /// no read model knows, so it is left unspecified), its title the one
    /// `session rename` set (S55) beside the first-prompt summary.
    pub fn resource(&self, record: &SessionRecord) -> daemon_proto::v1::Session {
        let summary = self
            .transcript(&record.id)
            .as_deref()
            .and_then(title_of)
            .unwrap_or_default();
        daemon_proto::v1::Session {
            name: self.name_of(record),
            uid: record.id.clone(),
            engine: record.engine.clone(),
            summary,
            start_time: Some(record.started_at),
            end_time: record.ended_at,
            kept: record.kept,
            owner: record
                .owner
                .as_ref()
                .map(|owner| self.name_of_ref(owner))
                .unwrap_or_default(),
            closed: record.closed,
            children: record
                .children
                .iter()
                .filter_map(|child| child.session.as_ref())
                .map(|child| self.name_of_ref(child))
                .collect(),
            incarnation: record.incarnation,
            title: record.title.clone(),
            ..daemon_proto::v1::Session::default()
        }
    }
}

impl Model for SessionIndex {
    const NAME: &'static str = "session-index";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        SessionIndex::project(self, book)
    }

    /// A session stream's session, as its resource message.
    fn view(&self, cover: &Cover) -> Option<Any> {
        if cover.domain != SESSION_DOMAIN {
            return None;
        }
        let record = self.find_by_id(&uuid_text(&root(Some(cover))))?;
        Any::from_msg(&self.resource(&record)).ok()
    }
}

/// A [`SessionIndex`] on angzarr-embedded's projector port: what a
/// `CatchUp` hands each session stream's book, and, registered in a kernel,
/// what it hands inline. Every clone is the same fold ([`Held`]).
#[derive(Clone, Default)]
pub struct SessionIndexes(Held<SessionIndex>);

impl SessionIndexes {
    /// An empty index whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> SessionIndexes {
        SessionIndexes(Held::new(Some(fill)))
    }

    /// The index as projected so far.
    pub fn index(&self) -> Lock<'_, SessionIndex> {
        self.0.lock()
    }
}

impl ProjectorHandler for SessionIndexes {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// The record of the session folded from the stream rooted at `root`.
fn record_of(root: &[u8], session: &SessionAggregate) -> SessionRecord {
    let state = session.state();
    SessionRecord {
        id: uuid_text(root),
        harp: state.harp.clone(),
        project_id: state.project_id.clone(),
        project_dir: state.project_dir.clone(),
        output_dir: state.output_dir.clone(),
        engine: state.engine.clone(),
        origin: state.origin,
        native_id: state.native_id.clone(),
        transcript_path: state.transcript_path.clone(),
        engine_version: state.engine_version.clone(),
        rotations: state.rotations.clone(),
        kept: state.kept,
        started_at: state.started_at,
        ended_at: state.ended_at,
        incarnation: state.incarnation,
        last_captured: None,
        owner: state.owner.clone(),
        closed: state.closed,
        children: state.children.clone(),
        title: state.title.clone(),
        name: String::new(),
    }
}

/// A stream root as a UUID's hyphenated text; its hex where it is not 16
/// bytes.
fn uuid_text(root: &[u8]) -> String {
    let hex: String = root.iter().map(|b| format!("{b:02x}")).collect();
    if hex.len() != 32 {
        return hex;
    }
    format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    )
}

/// A session stream's root, as the index tells streams apart by.
fn root(cover: Option<&Cover>) -> Vec<u8> {
    cover
        .and_then(|cover| cover.root.as_ref())
        .map(|r| r.value.to_vec())
        .unwrap_or_default()
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
