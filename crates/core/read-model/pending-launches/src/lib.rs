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
//! A run is STARTED from its `RunnerStartedRecorded` until its `RunEnded`
//! ([`Pending::started`], ruling 93), listed with its session's project and
//! current harp (where its session's lock is): a launcher that finds one
//! whose session's lock is free knows its owner is gone. A run owed a runner
//! carries its launcher's lock (`launcher_lock`, rulings 86, 92) for the same
//! judgement, and its session's place too, once its mint is seen.
//!
//! An incarnation a session owes (`IncarnationOwed`) is listed until its run is enqueued: the policy
//! session to run does that, so the launcher acts on [`Pending::launches`]
//! only.
//!
//! It is HELD in memory ([`catch_up`] is a `CatchUp::new`), rebuilt from each
//! stream's start by a process's first step. No clock, no soft ceiling.

use std::collections::BTreeMap;
use std::sync::{Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_ports::tombstone;
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
    /// The session's project directory and harp, as its stream says them now
    /// (its mint's); empty until its mint is seen. A run adopted while owed a
    /// runner lets its checkout go in this project once it ends, even when
    /// it was started and ended before it was ever listed as started.
    pub project_dir: String,
    /// The session's project, by id (its mint's): whose seal key opens the
    /// run's sealed values.
    pub project_id: String,
    pub harp: String,
    /// What the run's plan decided, as its `RunEnqueued` recorded it: the
    /// launcher acts on this alone (P3: nothing is handed to the launcher).
    /// None for a run enqueued before the record existed.
    pub spec: Option<runs::LaunchSpec>,
    /// What the run was given, as its enqueue recorded it (the permissions
    /// document its engine is handed among it).
    pub grant: Option<runs::Grant>,
    /// What the session's own events say of the launch: its engine and
    /// workspace axis (the mint's), the agent its incarnation's loadout
    /// named, and the engine's own session it resumes (the one bound when the
    /// incarnation is a later one). Empty until recorded.
    pub engine: String,
    pub workspace: Option<String>,
    pub agent: String,
    pub resume: Option<String>,
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
    /// (a harp never changes, S55); empty until its mint is seen.
    pub project_dir: String,
    pub harp: String,
    /// The run's own lock, as its enqueue journaled it: a host that holds it
    /// for the run's life (the daemon, for a child it hosts, rulings 86, 92)
    /// is lost when it is free (T3, T18).
    pub launcher_lock: String,
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
    Waiting(Box<Enqueued>),
    /// Its runner was started, and it has not ended: its launcher's lock.
    Started(String),
    /// It ended.
    Past,
}

/// A run as its enqueue recorded it.
#[derive(Clone, Debug)]
struct Enqueued {
    mode: RunMode,
    lock: String,
    spec: Option<runs::LaunchSpec>,
    grant: Option<runs::Grant>,
}

/// Where a session's lock is: its project directory and current harp.
#[derive(Clone, Debug, Default)]
struct Place {
    project_dir: String,
    project_id: String,
    harp: String,
    /// Whether the session's mint is folded.
    minted: bool,
    /// The agent each incarnation's loadout named.
    loadouts: BTreeMap<u32, String>,
    engine: String,
    workspace: Option<String>,
    /// The engine's own session last bound.
    native_id: String,
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
    /// projector port hands it. A tombstone (a purge's `StreamClosed`) closes
    /// the stream: what the model held of it leaves, and what follows starts
    /// afresh.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let domain = book.cover.as_ref().map_or("", |c| c.domain.as_str());
        let root = root_of(book);
        for step in steps(book) {
            match (domain, step) {
                (RUN_DOMAIN, Step::Closed) => self.forget_run(&root),
                (RUN_DOMAIN, Step::Event(event)) => {
                    self.fold_run(&root, RunEvent::from_any(event)?);
                }
                (SESSION_DOMAIN, Step::Closed) => {
                    self.owed.retain(|_, owner| *owner != root);
                    self.places.remove(&text_of(&root));
                }
                (SESSION_DOMAIN, Step::Event(event)) => {
                    self.fold_session(&root, SessionEvent::from_any(event)?);
                }
                _ => {}
            }
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
                self.runs.insert(
                    key,
                    Stage::Waiting(Box::new(Enqueued {
                        mode: enqueued.mode,
                        lock: enqueued.launcher_lock,
                        spec: enqueued.spec,
                        grant: enqueued.grant,
                    })),
                );
            }
            RunEvent::RunnerStartedRecorded(_) => {
                if let Some(key) = self.keys.get(root) {
                    if let Some(stage @ Stage::Waiting(..)) = self.runs.get_mut(key) {
                        let lock = match stage {
                            Stage::Waiting(owed) => std::mem::take(&mut owed.lock),
                            _ => String::new(),
                        };
                        *stage = Stage::Started(lock);
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
                let place = self.places.entry(text_of(root)).or_default();
                place.project_dir = minted.project_dir;
                place.project_id = minted.project_id;
                place.harp = minted.harp;
                place.engine = minted.engine;
                place.workspace = minted.workspace;
                place.minted = true;
            }
            SessionEvent::LoadoutRecorded(recorded) => {
                self.places
                    .entry(text_of(root))
                    .or_default()
                    .loadouts
                    .insert(recorded.incarnation, recorded.loadout.agent);
            }
            SessionEvent::EngineSessionBound(bound) => {
                self.places.entry(text_of(root)).or_default().native_id = bound.native_id;
            }
            _ => {}
        }
    }

    /// The runs owed a runner, by session then incarnation.
    pub fn launches(&self) -> Vec<PendingLaunch> {
        self.runs
            .iter()
            .filter_map(|((session, incarnation), stage)| match stage {
                Stage::Waiting(owed) => {
                    let place = self.places.get(session).cloned().unwrap_or_default();
                    let resume = (!place.native_id.is_empty()).then(|| place.native_id.clone());
                    Some(PendingLaunch {
                        run: runs::run_root(session, *incarnation),
                        session: session.clone(),
                        incarnation: *incarnation,
                        mode: owed.mode,
                        launcher_lock: owed.lock.clone(),
                        project_dir: place.project_dir,
                        project_id: place.project_id,
                        harp: place.harp,
                        spec: owed.spec.clone(),
                        grant: owed.grant.clone(),
                        engine: place.engine,
                        workspace: place.workspace,
                        agent: place.loadouts.get(incarnation).cloned().unwrap_or_default(),
                        resume,
                    })
                }
                Stage::Started(_) | Stage::Past => None,
            })
            .collect()
    }

    /// The runs a launcher may act on now: owed a runner AND their session
    /// has its mint and its loadout for the run's incarnation recorded
    /// (P3, owner ruling 2026-11-25). The condition is on the FOLDED STATE of
    /// the session's stream, never on delivery order: angzarr orders events
    /// within a stream only, so the run's enqueue may be seen before, after
    /// or between the session's mint and loadout.
    pub fn launchable(&self) -> Vec<PendingLaunch> {
        self.launches()
            .into_iter()
            .filter(|launch| {
                self.places.get(&launch.session).is_some_and(|place| {
                    place.minted && place.loadouts.contains_key(&launch.incarnation)
                })
            })
            .collect()
    }

    /// The runs started and not ended, by session then incarnation (ruling
    /// 93).
    ///
    pub fn started(&self) -> Vec<StartedRun> {
        self.runs
            .iter()
            .filter_map(|((session, incarnation), stage)| {
                let Stage::Started(lock) = stage else {
                    return None;
                };
                let place = self.places.get(session).cloned().unwrap_or_default();
                Some(StartedRun {
                    run: runs::run_root(session, *incarnation),
                    session: session.clone(),
                    incarnation: *incarnation,
                    project_dir: place.project_dir,
                    harp: place.harp,
                    launcher_lock: lock.clone(),
                })
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

/// One page of a book as a model folds it.
enum Step<'a> {
    /// A purge's tombstone: the stream is closed.
    Closed,
    /// A domain's event.
    Event(&'a angzarr_embedded::Any),
}

/// The steps of `book`; a framework meta page is no domain's event.
fn steps(book: &EventBook) -> impl Iterator<Item = Step<'_>> {
    book.pages.iter().filter_map(|page| {
        if tombstone::is(page) {
            return Some(Step::Closed);
        }
        match &page.body {
            Some(event_page::Body::Event(event)) => Some(Step::Event(event)),
            _ => None,
        }
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
    use angzarr_embedded::coded_diagnostic::{Arg, Diagnostic as _};
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
