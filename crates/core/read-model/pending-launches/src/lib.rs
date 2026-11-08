//! The pending launches: a read model a projector folds from the session and
//! run streams. It answers which runs are still owed a runner, in a stable
//! order, and which incarnations are owed a run.
//!
//! A run is owed a runner from its `RunEnqueued` until a sign that a runner
//! exists (`RunnerAttachedRecorded`, `TurnStartedRecorded`) or its end
//! (`RunEnded`, whatever the cause). An incarnation a session owes
//! (`IncarnationOwed`) is listed until its run is enqueued: the policy
//! session to run does that, so the launcher acts on [`Pending::launches`]
//! only.
//!
//! Known gap: the plan names a `RunnerStarted` fact of the launcher as the
//! sign that a launch was made. The runs protos do not have it yet, so the
//! first sign of life from the runner stands for it; a launcher that has
//! started a turn's command and not yet heard from it would launch again.
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
    /// Enqueued, no sign of a runner.
    Waiting(RunMode),
    /// A runner exists, or the run ended.
    Past,
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
                self.runs.insert(key, Stage::Waiting(enqueued.mode));
            }
            RunEvent::RunnerAttachedRecorded(_)
            | RunEvent::TurnStartedRecorded(_)
            | RunEvent::RunEnded(_) => {
                if let Some(key) = self.keys.get(root) {
                    self.runs.insert(key.clone(), Stage::Past);
                }
            }
            _ => {}
        }
    }

    fn fold_session(&mut self, root: &[u8], event: SessionEvent) {
        if let SessionEvent::IncarnationOwed(owed) = event {
            let key = (text_of(root), owed.incarnation);
            if !self.runs.contains_key(&key) {
                self.owed.insert(key, root.to_vec());
            }
        }
    }

    /// The runs owed a runner, by session then incarnation.
    pub fn launches(&self) -> Vec<PendingLaunch> {
        self.runs
            .iter()
            .filter_map(|((session, incarnation), stage)| match stage {
                Stage::Waiting(mode) => Some(PendingLaunch {
                    run: runs::run_root(session, *incarnation),
                    session: session.clone(),
                    incarnation: *incarnation,
                    mode: *mode,
                }),
                Stage::Past => None,
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
