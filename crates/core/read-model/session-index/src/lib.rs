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
use std::sync::{Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{
    CatchUp, Domain, PageContext, Payload, PayloadError, ProjectorHandler, Timestamp,
};
use sessions::{Origin, Rotation, SessionAggregate, SessionEvent, SESSION_DOMAIN};

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
    /// The title `session rename` set (S55); empty when none is.
    pub title: String,
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
        CatchUp::new(&[SESSION_DOMAIN], SessionIndexes::default())
            .expect("a session index reads the sessions domain")
    }

    /// Fold `book`, one session stream's events after the last projected of
    /// it, as the projector port hands it: a book from sequence 0 starts the
    /// stream afresh; a book with no event at 0 says the stream is gone
    /// (purged whole), and its session leaves the index. An event the sessions
    /// domain cannot read folds nothing and is the fold's error.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = root(book.cover.as_ref());
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
        SessionRecord {
            last_captured: self.transcripts.get(root).and_then(|t| t.last),
            ..record_of(root, session)
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

/// A [`SessionIndex`] on angzarr-embedded's projector port: what a
/// `CatchUp` hands each session stream's book.
#[derive(Default)]
pub struct SessionIndexes(Mutex<SessionIndex>);

impl SessionIndexes {
    /// The index as projected so far.
    pub fn index(&self) -> MutexGuard<'_, SessionIndex> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl ProjectorHandler for SessionIndexes {
    fn name(&self) -> &str {
        "session-index"
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let events = request.events.clone().unwrap_or_default();
        self.index().project(&events).map_err(|error| bug(&error))?;
        Ok(ProjectResponse { projection: None })
    }
}

/// `error`, an event the index cannot read, as the projector port's bug: its
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
        title: state.title.clone(),
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
