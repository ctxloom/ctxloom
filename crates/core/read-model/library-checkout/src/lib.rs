//! The library checkouts (step 10 U0): a read model a projector folds from
//! the library streams, one row per tree (`projects/{p}/checkouts/{c}/
//! library`, `users/me/library`): the commit, the library's tree id and
//! the dirty paths last observed, the last cause, the pending intents, and
//! each resource's `{last sequence, oid}` with the baseline's sequence (G4
//! superseded). It is what a read reconciles against without folding the
//! stream on every read (U2), and what answers each resource's etag. It
//! folds with the domain's own fold ([`library_tree::Library::apply`]), so
//! its token is the one `decide` judges. It holds no content and writes no
//! file (87a).
//!
//! It is HELD in memory, like `trust-index`: registered in the daemon's
//! kernel ([`LibraryCheckouts`] on the projector port, S32) and caught up
//! from the store by the daemon's loop ([`caught_up`]), so a tree a daemon
//! before this one recorded is held too.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, Payload, PayloadError, ProjectorHandler};
use held_projection::{Fill, Held, Lock, Model};
use library_tree::{Library, LibraryEvent, LIBRARY_DOMAIN};

/// The checkouts.
#[derive(Default)]
pub struct LibraryCheckout {
    /// Each tree stream's state, by its root.
    rows: BTreeMap<Vec<u8>, Library>,
    /// Every intent recorded, by its request id (step 10b U3): what a
    /// write's retry finds, whether it is done or not.
    intents: BTreeMap<String, Known>,
    /// Every export recorded, by its request id (AIP-155), with its tree's
    /// root: what an export's retry is answered with.
    exports: BTreeMap<String, (Vec<u8>, library_tree::BundleExported)>,
    /// Who is told each change as it is recorded (step 10c U13 rework,
    /// `WatchLibrary`): no log is kept; a watcher is told what is recorded
    /// from when it watched, and woken by its own signal.
    watchers: std::sync::Mutex<Vec<Watcher>>,
    /// Each tree's last sequence told, by its root, so a stream folded
    /// afresh (a catch-up) tells nothing twice.
    logged: BTreeMap<Vec<u8>, u64>,
    /// The place of the last change told.
    last: u64,
}

/// What wakes a watcher once a change is told it: the catch-up's signal.
pub type Wake = Arc<dyn Fn() + Send + Sync>;

/// One watcher: where its changes go, and what wakes it.
struct Watcher {
    told: std::sync::mpsc::Sender<Recorded>,
    wake: Wake,
}

/// One recorded change of a tree (step 10c U13, G7; protos.md §4.1): a
/// `TreeObserved` or a `StepApplied`, as a thin notice. Never content.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Recorded {
    /// Its place in the log, from 1.
    pub at: u64,
    /// `projects/{p}/checkouts/{c}/library` or `users/me/library`.
    pub tree: String,
    /// The event's sequence in its tree's stream: each path's new token.
    pub sequence: u64,
    /// The cause the event records (an observation's external id, a
    /// step's fact's).
    pub cause: String,
    /// ctxloom's own write: its request id and verb; none for an outside
    /// change.
    pub intended: Option<(String, String)>,
    pub changes: Vec<Changed>,
    /// When it was recorded, seconds and nanos since the epoch.
    pub recorded_at: (i64, i32),
}

/// One path of a [`Recorded`] change.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Changed {
    pub path: String,
    /// `added`, `modified`, `deleted`, `renamed`, `type_changed`.
    pub status: String,
    /// A rename's old path.
    pub from_path: String,
}

/// A change's status as a notice says it.
fn status_text(status: library_tree::ChangeStatus) -> &'static str {
    match status {
        library_tree::ChangeStatus::Added => "added",
        library_tree::ChangeStatus::Modified => "modified",
        library_tree::ChangeStatus::Deleted => "deleted",
        library_tree::ChangeStatus::Renamed => "renamed",
        library_tree::ChangeStatus::TypeChanged => "type_changed",
    }
}

/// An intent as the record holds it, by its request id: its tree's root,
/// the intent as recorded, and how it ended.
#[derive(Clone, Debug, PartialEq)]
pub struct Known {
    pub root: Vec<u8>,
    pub intent: library_tree::Intent,
    pub end: End,
}

/// Where an intent is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum End {
    /// Steps owed: the next is `done`.
    Pending { done: u32 },
    /// Every step done.
    Done,
    /// A step failed (final).
    Failed,
    /// Superseded before it was done.
    Superseded,
}

/// The paths `event` changes, as a notice says them, judged against `row`
/// as it was before it: an observation's changes as git saw them, a step's
/// paths each added, modified or deleted by what the record held there.
/// None for an event that changes no path.
fn changes_of(row: &Library, event: &LibraryEvent) -> Option<Vec<Changed>> {
    match event {
        LibraryEvent::TreeObserved(observed) => Some(
            observed
                .changes
                .iter()
                .map(|change| Changed {
                    path: change.path.clone(),
                    status: status_text(change.status).to_string(),
                    from_path: change.from_path.clone(),
                })
                .collect(),
        ),
        LibraryEvent::StepApplied(applied) => Some(
            applied
                .observed
                .iter()
                .map(|path| {
                    let was = row
                        .resources
                        .get(&path.path)
                        .is_some_and(|resource| !resource.oid.is_empty())
                        || row.dirty.get(&path.path).is_some_and(|oid| !oid.is_empty());
                    let status = match (path.oid.is_empty(), was) {
                        (true, _) => "deleted",
                        (false, true) => "modified",
                        (false, false) => "added",
                    };
                    Changed {
                        path: path.path.clone(),
                        status: status.to_string(),
                        from_path: String::new(),
                    }
                })
                .collect(),
        ),
        _ => None,
    }
}

fn root_of(cover: Option<&Cover>) -> Vec<u8> {
    cover
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default()
}

impl LibraryCheckout {
    /// An empty model.
    pub fn new() -> LibraryCheckout {
        LibraryCheckout::default()
    }

    /// Fold `book`, one stream's events after the last projected of it. A
    /// tombstone (a purge's `StreamClosed`) closes the stream: it leaves the
    /// model. Another domain's book is none of its.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        if book.cover.as_ref().map(|c| c.domain.as_str()) != Some(LIBRARY_DOMAIN) {
            return Ok(());
        }
        let root = root_of(book.cover.as_ref());
        for page in &book.pages {
            if tombstone::is(page) {
                self.rows.remove(&root);
                self.intents.retain(|_, known| known.root != root);
                self.exports.retain(|_, (tree, _)| *tree != root);
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let event = LibraryEvent::from_any(event)?;
            let row = self.rows.entry(root.clone()).or_default();
            let changes = changes_of(row, &event);
            row.apply_page(&event, page.sequence, page.meta.as_ref());
            let tree = row.name.clone();
            if let Some(changes) = changes {
                self.log_change(
                    &root,
                    &event,
                    (page.sequence, page.meta.as_ref()),
                    tree,
                    changes,
                );
            }
            let row = self.rows.entry(root.clone()).or_default();
            let pending = row.pending.clone();
            self.track(&root, &event, &pending);
        }
        Ok(())
    }

    /// The intents `event` names, kept by their request ids; `pending` the
    /// tree's after it.
    fn track(
        &mut self,
        root: &[u8],
        event: &LibraryEvent,
        pending: &BTreeMap<String, library_tree::Intent>,
    ) {
        let (request, end) = match event {
            LibraryEvent::ChangeIntended(intended) => {
                let intent = intended.intent.clone();
                self.intents.insert(
                    intent.request_id.clone(),
                    Known {
                        root: root.to_vec(),
                        intent,
                        end: End::Pending { done: 0 },
                    },
                );
                return;
            }
            LibraryEvent::StepApplied(applied) => (&applied.request_id, None),
            LibraryEvent::TreeObserved(observed) => match &observed.intended {
                Some(request) => (request, None),
                None => return,
            },
            LibraryEvent::ChangeFailed(failed) => (&failed.request_id, Some(End::Failed)),
            LibraryEvent::IntentSuperseded(superseded) => {
                (&superseded.request_id, Some(End::Superseded))
            }
            // The tree's, naming no intent (G3).
            LibraryEvent::HomeRepositoryMade(_) => return,
            // Step 10c U4 makes it complete its pull's step, as a
            // `StepApplied` does; nothing records one yet.
            LibraryEvent::TreeInstalled(_) => return,
            LibraryEvent::BundleExported(exported) => {
                self.exports.insert(
                    exported.request_id.clone(),
                    (root.to_vec(), exported.clone()),
                );
                return;
            }
        };
        let Some(known) = self.intents.get_mut(request) else {
            return;
        };
        known.end = end.unwrap_or_else(|| match pending.get(request) {
            Some(intent) => End::Pending { done: intent.done },
            None => End::Done,
        });
    }

    /// `event`, recorded at `sequence` with `meta` on the stream rooted at
    /// `root` (the tree `tree`), logged as the change `changes`, unless the
    /// stream logged it before.
    fn log_change(
        &mut self,
        root: &[u8],
        event: &LibraryEvent,
        (sequence, meta): (u64, Option<&angzarr_embedded::v1::EventMeta>),
        tree: String,
        changes: Vec<Changed>,
    ) {
        if self.logged.get(root).is_some_and(|last| *last >= sequence) {
            return;
        }
        self.logged.insert(root.to_vec(), sequence);
        let request = match event {
            LibraryEvent::StepApplied(applied) => Some(applied.request_id.clone()),
            LibraryEvent::TreeObserved(observed) => observed.intended.clone(),
            _ => None,
        };
        let intended = request.map(|request| {
            let verb = self
                .intents
                .get(&request)
                .map(|known| known.intent.verb.clone())
                .unwrap_or_default();
            (request, verb)
        });
        let recorded_at = meta
            .and_then(|meta| meta.recorded_at.as_ref())
            .map_or((0, 0), |at| (at.seconds, at.nanos));
        self.last += 1;
        let recorded = Recorded {
            at: self.last,
            tree,
            sequence,
            cause: library_tree::cause_of(meta),
            intended,
            changes,
            recorded_at,
        };
        // A watcher gone is let go here; each other is told, then woken.
        let mut watchers = self
            .watchers
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        watchers.retain(|watcher| watcher.told.send(recorded.clone()).is_ok());
        for watcher in watchers.iter() {
            (watcher.wake)();
        }
    }

    /// A watcher of every change recorded from now on, oldest first: told
    /// each as it is folded and woken by `wake` (step 10c U13 rework: the
    /// catch-up's signal, no poll and no log).
    pub fn watch(&self, wake: Wake) -> std::sync::mpsc::Receiver<Recorded> {
        let (told, heard) = std::sync::mpsc::channel();
        self.watchers
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .push(Watcher { told, wake });
        heard
    }

    /// How many watchers are told.
    pub fn watching(&self) -> usize {
        self.watchers
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .len()
    }

    /// The export recorded under `request_id`, in any tree (AIP-155).
    pub fn export(&self, request_id: &str) -> Option<&library_tree::BundleExported> {
        self.exports.get(request_id).map(|(_, exported)| exported)
    }

    /// The intent recorded under `request_id`, in any tree.
    pub fn intent(&self, request_id: &str) -> Option<&Known> {
        self.intents.get(request_id)
    }

    /// Every tree folded, in no order a reader relies on.
    pub fn trees(&self) -> impl Iterator<Item = &Library> {
        self.rows.values()
    }

    /// The tree whose stream root is `root`, if any event of it was folded.
    pub fn tree(&self, root: &angzarr_embedded::v1::Uuid) -> Option<&Library> {
        self.rows.get(&root.value.to_vec())
    }

    /// The token (etag) of `path` in the tree rooted at `root`: its last
    /// sequence, a directory's the latest under it, else the baseline's;
    /// none for a tree never observed.
    pub fn token(&self, root: &angzarr_embedded::v1::Uuid, path: &str) -> Option<u64> {
        self.tree(root).map(|tree| tree.token(path))
    }
}

impl Model for LibraryCheckout {
    const NAME: &'static str = "library-checkout";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        LibraryCheckout::project(self, book)
    }

    /// The stream's tree, as the domain's state message.
    fn view(&self, cover: &Cover) -> Option<Any> {
        let tree = self.rows.get(&root_of(Some(cover)))?;
        Any::from_msg(&tree.wire()).ok()
    }
}

/// The model on angzarr-embedded's projector port: every clone is the same
/// fold ([`Held`]), so one instance can be both a catch-up's and the
/// kernel's.
#[derive(Clone, Default)]
pub struct LibraryCheckouts(Held<LibraryCheckout>);

impl LibraryCheckouts {
    /// An empty model whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> LibraryCheckouts {
        LibraryCheckouts(Held::new(Some(fill)))
    }

    /// The model, locked.
    pub fn checkouts(&self) -> Lock<'_, LibraryCheckout> {
        self.0.lock()
    }
}

impl ProjectorHandler for LibraryCheckouts {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// `model` caught up from a store: its catch-up, sharing its fold (S32).
pub fn caught_up(model: LibraryCheckouts) -> CatchUp<LibraryCheckouts> {
    CatchUp::new(&SOURCES, model).expect("the library checkouts read the library domain")
}

/// The domains the model folds.
pub const SOURCES: [&str; 1] = [LIBRARY_DOMAIN];

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
