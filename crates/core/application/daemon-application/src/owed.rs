//! What each live session is owed, told as the events arrive (step 10c U13
//! rework; owner rulings 2026-11-18 and 2026-11-27).
//!
//! [`OwedContexts`] is the daemon's RUNNER for it, run by the loop
//! ([`crate::Daemon::tick`]) under angzarr's [`CatchUp::new`] over the
//! library, sessions and projects domains. It decides nothing and holds no
//! read model: the session aggregate decides what a library change owes it,
//! on its own folded loadout and handed positions
//! (`sessions::RecordLibraryChanged`), and the runner only routes. It folds,
//! for itself, from the events it is handed: each library tree's paths (each
//! at the sequence and instant of the last event that named it), each
//! session as the domain folds it (`sessions::apply`: its loadout, what its
//! context covers, whether it ended) and each project's harp. That fold is
//! rebuilt whole when a daemon starts, as the read models are, so nothing
//! of it is kept in the store; every fact it sends is keyed
//! ([`sessions::owed_key`]), so a book handed again, or the whole store
//! replayed, records nothing twice.
//!
//! - A library change (a `TreeObserved` or a `StepApplied` at its stream's
//!   sequence) is told to each live session drawing on that tree whose
//!   loadout it touches and whose context does not cover it.
//! - A session's loadout (`LoadoutRecorded`) is told the changes its trees
//!   hold past what it covers, so a change recorded between its plan and its
//!   loadout is not lost, and whichever of a change and a loadout the runner
//!   meets last, the pair is judged.
//!
//! The session's own index folds the owed changes, its loadout and the
//! positions each re-assembly covered; its next hook only COLLECTS what is
//! owed (`RedeliverContext`). Nothing here observes a tree.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage};
use angzarr_embedded::{CatchUp, Payload, ProjectorHandler, Timestamp};
use library_tree::{LibraryEvent, LIBRARY_DOMAIN};
use run_ports::Edge;
use sessions::{
    ProjectEvent, ProjectState, SessionEvent, SessionState, PROJECTS_DOMAIN, SESSION_DOMAIN,
};

/// The runner's name.
pub const NAME: &str = "owed-contexts";

/// The domains it handles.
pub const SOURCES: [&str; 3] = [LIBRARY_DOMAIN, SESSION_DOMAIN, PROJECTS_DOMAIN];

/// The library trees a session draws on: its checkout's, when its library
/// has been named (under its project's harp), and the home's.
pub trait SessionTrees: Send + Sync {
    fn trees(&self, session: &SessionState, project_harp: Option<&str>) -> Vec<String>;
}

/// One library change as the runner tells it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Change {
    pub tree: String,
    pub sequence: u64,
    /// Every path it changed (a rename's both).
    pub paths: Vec<String>,
    pub recorded: Timestamp,
}

/// The change `event`, at `sequence` of the tree `tree`'s stream recorded
/// at `recorded`; none for an event that changes no path.
pub fn change_of(
    tree: &str,
    event: &LibraryEvent,
    sequence: u64,
    recorded: Timestamp,
) -> Option<Change> {
    let paths: Vec<String> = match event {
        LibraryEvent::TreeObserved(observed) => observed
            .changes
            .iter()
            .flat_map(|change| [change.path.clone(), change.from_path.clone()])
            .collect(),
        LibraryEvent::StepApplied(applied) => applied
            .observed
            .iter()
            .map(|path| path.path.clone())
            .collect(),
        _ => return None,
    };
    let paths: Vec<String> = paths.into_iter().filter(|path| !path.is_empty()).collect();
    (!paths.is_empty()).then(|| Change {
        tree: tree.to_string(),
        sequence,
        paths,
        recorded,
    })
}

/// A library tree as the runner folds it: each path at the sequence and
/// instant of the last event that named it.
#[derive(Default)]
struct Tree {
    paths: BTreeMap<String, (u64, Timestamp)>,
}

/// A change told to a session, with the key it is recorded under.
#[derive(Clone)]
struct Told {
    /// The session's stream root.
    session: Vec<u8>,
    fact: sessions::RecordLibraryChanged,
    key: String,
}

/// What the runner folds from the events it is handed: the library trees,
/// the sessions as the domain folds them, and the projects' harps. Its own,
/// rebuilt when a daemon starts, and no read model's.
#[derive(Default)]
struct Fold {
    /// Each library stream's tree name, by root.
    names: BTreeMap<Vec<u8>, String>,
    trees: BTreeMap<String, Tree>,
    sessions: BTreeMap<Vec<u8>, SessionState>,
    projects: BTreeMap<Vec<u8>, ProjectState>,
    /// Where each stream's fold is: the next page it needs (a page handed
    /// again is folded once).
    next: BTreeMap<(String, Vec<u8>), u64>,
    /// Told and not yet recorded: sent first when a book is handed again.
    pending: Vec<Told>,
}

/// A book's stream root and the pages of it not folded yet.
fn unfolded<'a>(
    fold: &mut Fold,
    domain: &str,
    book: &'a EventBook,
) -> (Vec<u8>, Vec<&'a EventPage>) {
    let root = book
        .cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default();
    let next = fold
        .next
        .entry((domain.to_string(), root.clone()))
        .or_default();
    let pages: Vec<&EventPage> = book
        .pages
        .iter()
        .filter(|page| tombstone::is(page) || page.sequence >= *next)
        .collect();
    if let Some(last) = pages.last() {
        *next = (*next).max(last.sequence + 1);
    }
    (root, pages)
}

/// The instant `page` was recorded.
fn recorded_of(page: &EventPage) -> Timestamp {
    page.meta
        .as_ref()
        .and_then(|meta| meta.recorded_at)
        .unwrap_or_default()
}

impl Fold {
    /// The harp of the project `project_id`, when its stream named it.
    fn harp_of(&self, project_id: &str) -> Option<&str> {
        self.projects
            .values()
            .find(|project| project.project_id == project_id && !project.harp.is_empty())
            .map(|project| project.harp.as_str())
    }

    /// Each live session with a loadout, with the trees it draws on.
    fn live(&self, trees: &dyn SessionTrees) -> Vec<(Vec<u8>, Vec<String>)> {
        self.sessions
            .iter()
            .filter(|(_, state)| state.ended_at.is_none() && state.context.is_some())
            .map(|(root, state)| {
                (
                    root.clone(),
                    trees.trees(state, self.harp_of(&state.project_id)),
                )
            })
            .collect()
    }

    /// A library stream's book folded: each change it holds, told to the
    /// live sessions that draw on its tree.
    fn library(&mut self, book: &EventBook, trees: &dyn SessionTrees) -> Vec<Told> {
        let (root, pages) = unfolded(self, LIBRARY_DOMAIN, book);
        let mut changes = Vec::new();
        for page in pages {
            if tombstone::is(page) {
                if let Some(name) = self.names.remove(&root) {
                    self.trees.remove(&name);
                }
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let Ok(event) = LibraryEvent::from_any(event) else {
                continue;
            };
            let recorded = recorded_of(page);
            if let LibraryEvent::TreeObserved(observed) = &event {
                self.names.insert(root.clone(), observed.name.clone());
                if observed.baseline {
                    self.trees.insert(observed.name.clone(), Tree::default());
                }
            }
            let Some(name) = self.names.get(&root).cloned() else {
                continue;
            };
            let Some(change) = change_of(&name, &event, page.sequence, recorded) else {
                continue;
            };
            let tree = self.trees.entry(name).or_default();
            for path in &change.paths {
                tree.paths
                    .insert(path.clone(), (change.sequence, change.recorded));
            }
            changes.push(change);
        }
        if changes.is_empty() {
            return Vec::new();
        }
        let live = self.live(trees);
        let mut told = Vec::new();
        for change in &changes {
            for (session, drawn) in &live {
                let Some(context) = self.sessions.get(session).and_then(|s| s.context.as_ref())
                else {
                    continue;
                };
                // A first judgement of the pair, cheap: the session decides.
                if drawn.contains(&change.tree)
                    && context.owes(&change.tree, change.sequence, change.recorded)
                    && !sessions::touched(&context.bundles, &change.paths).is_empty()
                {
                    told.push(told_of(session, change));
                }
            }
        }
        told
    }

    /// A session stream's book folded: a loadout it holds told the changes
    /// its trees hold past what it covers.
    fn session(&mut self, book: &EventBook, trees: &dyn SessionTrees) -> Vec<Told> {
        let (root, pages) = unfolded(self, SESSION_DOMAIN, book);
        let mut loaded = false;
        for page in pages {
            if tombstone::is(page) {
                self.sessions.remove(&root);
                loaded = false;
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let Ok(event) = SessionEvent::from_any(event) else {
                continue;
            };
            let state = self.sessions.entry(root.clone()).or_default();
            sessions::apply(state, &event, recorded_of(page));
            loaded |= matches!(event, SessionEvent::LoadoutRecorded(_));
        }
        if loaded {
            self.at_load(&root, trees)
        } else {
            Vec::new()
        }
    }

    /// What the session `root`'s trees hold past its context's positions
    /// that its loadout touches, by sequence.
    fn at_load(&self, root: &[u8], trees: &dyn SessionTrees) -> Vec<Told> {
        let Some(state) = self.sessions.get(root) else {
            return Vec::new();
        };
        let Some(context) = state.context.as_ref().filter(|_| state.ended_at.is_none()) else {
            return Vec::new();
        };
        let mut told = Vec::new();
        for name in trees.trees(state, self.harp_of(&state.project_id)) {
            let Some(tree) = self.trees.get(&name) else {
                continue;
            };
            let mut past: BTreeMap<u64, (Timestamp, Vec<String>)> = BTreeMap::new();
            for (path, (sequence, recorded)) in &tree.paths {
                if context.owes(&name, *sequence, *recorded) {
                    past.entry(*sequence)
                        .or_insert_with(|| (*recorded, Vec::new()))
                        .1
                        .push(path.clone());
                }
            }
            for (sequence, (recorded, paths)) in past {
                if sessions::touched(&context.bundles, &paths).is_empty() {
                    continue;
                }
                told.push(told_of(
                    root,
                    &Change {
                        tree: name.clone(),
                        sequence,
                        paths,
                        recorded,
                    },
                ));
            }
        }
        told
    }

    /// A projects stream's book folded: the harps its projects were named.
    fn project(&mut self, book: &EventBook) {
        let (root, pages) = unfolded(self, PROJECTS_DOMAIN, book);
        for page in pages {
            if tombstone::is(page) {
                self.projects.remove(&root);
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            if let Ok(event) = ProjectEvent::from_any(event) {
                sessions::fold_project(self.projects.entry(root.clone()).or_default(), event);
            }
        }
    }
}

/// `change` told to the session `root`.
fn told_of(session: &[u8], change: &Change) -> Told {
    let id = uuid::Uuid::from_slice(session)
        .map(|id| id.to_string())
        .unwrap_or_default();
    Told {
        session: session.to_vec(),
        key: sessions::owed_key(&id, &change.tree, change.sequence),
        fact: sessions::RecordLibraryChanged {
            tree: change.tree.clone(),
            sequence: change.sequence,
            paths: change.paths.clone(),
            recorded: change.recorded,
        },
    }
}

/// The runner: it folds what it is handed and tells each live session the
/// library changes on the trees it draws on, through `edge`; the session
/// decides what it is owed.
pub struct OwedContexts<E> {
    edge: Arc<E>,
    trees: Arc<dyn SessionTrees>,
    fold: Mutex<Fold>,
}

impl<E: Edge> OwedContexts<E> {
    pub fn new(edge: Arc<E>, trees: Arc<dyn SessionTrees>) -> OwedContexts<E> {
        OwedContexts {
            edge,
            trees,
            fold: Mutex::new(Fold::default()),
        }
    }

    fn fold(&self) -> MutexGuard<'_, Fold> {
        self.fold.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// `told` recorded on its session, keyed.
    async fn tell(&self, told: &Told) -> Result<(), CodedError> {
        let cover = Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(
                told.session.as_slice().try_into().unwrap_or([0; 16]),
            )),
            edition: None,
        };
        self.edge
            .handle(sessions::fact_book(cover, &told.fact, &told.key))
            .await
            .map(|_| ())
            .map_err(|failure| {
                CodedError::bug(
                    "daemon-application-owed-not-recorded",
                    [("ext_err_txt".to_string(), format!("{failure:?}"))],
                )
            })
    }
}

impl<E: Edge> ProjectorHandler for OwedContexts<E> {
    fn name(&self) -> &str {
        NAME
    }

    /// A book of any of its domains folded, and what it owes told; a fact
    /// not recorded fails the book, so the catch-up hands it again and the
    /// facts not yet recorded are told first.
    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let book = request.events.clone().unwrap_or_default();
        {
            let mut fold = self.fold();
            let told = match book.cover.as_ref().map(|cover| cover.domain.as_str()) {
                Some(LIBRARY_DOMAIN) => fold.library(&book, self.trees.as_ref()),
                Some(SESSION_DOMAIN) => fold.session(&book, self.trees.as_ref()),
                Some(PROJECTS_DOMAIN) => {
                    fold.project(&book);
                    Vec::new()
                }
                _ => Vec::new(),
            };
            fold.pending.extend(told);
        }
        loop {
            let next = self.fold().pending.first().cloned();
            let Some(told) = next else { break };
            self.tell(&told).await?;
            self.fold().pending.remove(0);
        }
        Ok(ProjectResponse { projection: None })
    }
}

/// `owed` run by a catch-up that replays the stores when a daemon starts,
/// as the read models are.
pub fn caught_up<E: Edge>(owed: OwedContexts<E>) -> CatchUp<OwedContexts<E>> {
    CatchUp::new(&SOURCES, owed)
        .expect("the owed contexts read the library, sessions and projects domains")
}

#[cfg(test)]
#[path = "owed.test.rs"]
mod tests;
