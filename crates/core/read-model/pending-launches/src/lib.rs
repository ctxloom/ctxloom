//! The pending launches: a read model a projector folds from the session and
//! run streams. It answers which runs are still owed a runner, in a stable
//! order, and which incarnations are owed a run.
//!
//! A run is owed a runner from its `RunEnqueued` until the launcher says it
//! made the runner (`RunnerStartedRecorded`) or its end (`RunEnded`, whatever
//! the cause). The runner's own signs of life (`RunnerAttachedRecorded`,
//! `TurnStartedRecorded`) are not the launch: a runner spawned and silent has
//! been started, and one never started has not, whatever else is recorded.
//!
//! An incarnation a session owes (`IncarnationOwed`) is listed until its run is enqueued: the policy
//! session to run does that, so the launcher acts on [`Pending::launches`]
//! only.
//!
//! It is HELD in memory ([`catch_up`] is a `CatchUp::new`), rebuilt from each
//! stream's start by a process's first step. No clock, no soft ceiling.

use std::collections::BTreeMap;
use std::sync::{Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, EventBook, Uuid};
use angzarr_embedded::{CatchUp, Payload, PayloadError, ProjectorHandler};
use runs::{RunEvent, RunMode, RUN_DOMAIN};
use sessions::{SessionEvent, SESSION_DOMAIN};

/// A run owed a runner.
#[derive(Clone, Debug, PartialEq)]
pub struct PendingLaunch {
    /// The run's stream root.
    pub run: Uuid,
    /// The session's id text, as the run names it.
    pub session: String,
    pub incarnation: u32,
    pub mode: RunMode,
    /// The run's own lock, held by its launcher until the runner is started
    /// (rulings 86, 92): a run owed a runner whose lock is free is an orphan.
    pub launcher_lock: String,
}

/// A run whose runner was started and that has not ended, with where its
/// session's lock is (ruling 93): a started run whose session's lock is free
/// has lost its owner.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StartedRun {
    /// The run's stream root.
    pub run: Uuid,
    /// The session's id text.
    pub session: String,
    pub incarnation: u32,
    /// The session's project directory and harp, as its stream says them now
    /// (a rename moves its lock with its name); empty until its mint is seen.
    pub project_dir: String,
    pub harp: String,
}

/// An incarnation a session owes a run for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Owed {
    pub session: String,
    pub incarnation: u32,
}

/// Where one run stands.
#[derive(Clone, Debug)]
enum Stage {
    /// Enqueued, no sign of a runner: its mode and its launcher's lock.
    Waiting(RunMode, String),
    /// Its runner was started, and it has not ended.
    Started,
    /// It ended.
    Past,
}

/// Where a session's lock is: its project directory and current harp.
#[derive(Clone, Debug, Default)]
struct Place {
    project_dir: String,
    harp: String,
}

/// (session text, incarnation): what a run and an owed incarnation share.
type Key = (String, u32);

/// The pending launches.
#[derive(Default)]
pub struct Pending {
    /// Each run, by its key.
    runs: BTreeMap<Key, Stage>,
    /// A run stream's root to its key, for the events that do not name it.
    keys: BTreeMap<Vec<u8>, Key>,
    /// The incarnations owed, by key, with the session stream that owes it.
    owed: BTreeMap<Key, Vec<u8>>,
    /// Each session's place, by its id text.
    places: BTreeMap<String, Place>,
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
                    self.forget_run(&root);
                }
                for event in events(book) {
                    self.fold_run(&root, RunEvent::from_any(event)?);
                }
            }
            SESSION_DOMAIN => {
                if fresh {
                    self.owed.retain(|_, owner| *owner != root);
                    self.places.remove(&text_of(&root));
                }
                for event in events(book) {
                    self.fold_session(&root, SessionEvent::from_any(event)?);
                }
            }
            _ => {}
        }
        Ok(())
    }

    fn forget_run(&mut self, root: &[u8]) {
        if let Some(key) = self.keys.remove(root) {
            self.runs.remove(&key);
        }
    }

    fn fold_run(&mut self, root: &[u8], event: RunEvent) {
        match event {
            RunEvent::RunEnqueued(enqueued) => {
                let key = (enqueued.session, enqueued.incarnation);
                self.owed.remove(&key);
                self.keys.insert(root.to_vec(), key.clone());
                self.runs
                    .insert(key, Stage::Waiting(enqueued.mode, enqueued.launcher_lock));
            }
            RunEvent::RunnerStartedRecorded(_) => {
                if let Some(key) = self.keys.get(root) {
                    if let Some(stage @ Stage::Waiting(..)) = self.runs.get_mut(key) {
                        *stage = Stage::Started;
                    }
                }
            }
            RunEvent::RunEnded(_) => {
                if let Some(key) = self.keys.get(root) {
                    self.runs.insert(key.clone(), Stage::Past);
                }
            }
            _ => {}
        }
    }

    fn fold_session(&mut self, root: &[u8], event: SessionEvent) {
        match event {
            SessionEvent::IncarnationOwed(owed) => {
                let key = (text_of(root), owed.incarnation);
                if !self.runs.contains_key(&key) {
                    self.owed.insert(key, root.to_vec());
                }
            }
            SessionEvent::SessionMinted(minted) => {
                self.places.insert(
                    text_of(root),
                    Place {
                        project_dir: minted.project_dir,
                        harp: minted.harp,
                    },
                );
            }
            SessionEvent::RenamedTo(renamed) => {
                self.places.entry(text_of(root)).or_default().harp = renamed.harp;
            }
            _ => {}
        }
    }

    /// The runs owed a runner, by session then incarnation.
    pub fn launches(&self) -> Vec<PendingLaunch> {
        self.runs
            .iter()
            .filter_map(|((session, incarnation), stage)| match stage {
                Stage::Waiting(mode, lock) => Some(PendingLaunch {
                    run: runs::run_root(session, *incarnation),
                    session: session.clone(),
                    incarnation: *incarnation,
                    mode: *mode,
                    launcher_lock: lock.clone(),
                }),
                Stage::Started | Stage::Past => None,
            })
            .collect()
    }

    /// The runs started and not ended, by session then incarnation (ruling
    /// 93).
    ///
    pub fn started(&self) -> Vec<StartedRun> {
        self.runs
            .iter()
            .filter(|(_, stage)| matches!(stage, Stage::Started))
            .map(|((session, incarnation), _)| {
                let place = self.places.get(session).cloned().unwrap_or_default();
                StartedRun {
                    run: runs::run_root(session, *incarnation),
                    session: session.clone(),
                    incarnation: *incarnation,
                    project_dir: place.project_dir,
                    harp: place.harp,
                }
            })
            .collect()
    }

    /// The incarnations owed a run that is not yet enqueued, by session then
    /// incarnation.
    pub fn owed(&self) -> Vec<Owed> {
        self.owed
            .keys()
            .map(|(session, incarnation)| Owed {
                session: session.clone(),
                incarnation: *incarnation,
            })
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
pub struct PendingModel(Mutex<Pending>);

impl PendingModel {
    /// The model, locked.
    pub fn pending(&self) -> MutexGuard<'_, Pending> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl ProjectorHandler for PendingModel {
    fn name(&self) -> &str {
        "pending-launches"
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
pub fn catch_up() -> CatchUp<PendingModel> {
    CatchUp::new(&[SESSION_DOMAIN, RUN_DOMAIN], PendingModel::default())
        .expect("the pending launches read the session and run domains")
}

/// `error`, an event the model cannot read, as the projector port's bug: its
/// code, and its text args (the type URL it names).
pub fn bug(error: &PayloadError) -> CodedError {
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
