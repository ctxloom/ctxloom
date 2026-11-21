//! What each live session is owed, set as the projection updates (step 10c
//! U13 rework, owner 2026-11-18: "we read from the projection, and the
//! projection updates the worker agents").
//!
//! [`OwedContexts`] is an event handler the daemon's loop runs
//! ([`crate::Daemon::tick`]) under angzarr's [`CatchUp::stored`], over the
//! library and sessions domains: its positions are kept in the store under
//! its name, so a daemon after this one resumes where it left off, and a
//! book handed again (delivery is at least once) records nothing twice.
//!
//! - A library change (a `TreeObserved` or a `StepApplied` at its stream's
//!   sequence) is held against each live session drawing on that tree: one
//!   whose loadout it [`touched`] and whose context does not cover it is
//!   recorded owed it (`RecordContextOwed`, keyed
//!   [`sessions::owed_key`]). One step's changes are one event, so one
//!   batch.
//! - A session's loadout (`LoadoutRecorded`) is held against its trees as
//!   the checkouts projection has them: each path changed past the
//!   positions the loadout covers (read before its plan) that it touched is
//!   owed at once (keyed [`sessions::owed_at_load_key`]), so a change
//!   recorded between the plan and the loadout is not lost.
//!
//! The session's own index folds the owed changes, its loadout and the
//! positions each re-assembly covered; its next hook only COLLECTS what is
//! owed (`RedeliverContext`). Nothing here observes a tree.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{CatchUp, Payload, ProjectorHandler, Timestamp};
use library_checkout::LibraryCheckouts;
use library_tree::{LibraryEvent, LIBRARY_DOMAIN};
use run_ports::Edge;
use session_index::{SessionIndexes, SessionRecord};
use sessions::{SessionEvent, SessionLoadout, SESSION_DOMAIN};

use crate::reactions::touched;

/// The projector's name, under which the store keeps its positions.
pub const NAME: &str = "owed-contexts";

/// The domains it handles.
pub const SOURCES: [&str; 2] = [LIBRARY_DOMAIN, SESSION_DOMAIN];

/// The library trees a session draws on: its checkout's, when its library
/// has been named, and the home's.
pub trait SessionTrees: Send + Sync {
    fn trees(&self, record: &SessionRecord) -> Vec<String>;
}

/// One library change as the handler judges it.
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

/// Of `paths`, those `loadout` draws on, sorted, each once.
pub fn drawn_on(loadout: &SessionLoadout, paths: &[String]) -> Vec<String> {
    let kept: std::collections::BTreeSet<&String> = paths
        .iter()
        .filter(|path| !touched(loadout, std::slice::from_ref(*path)).is_empty())
        .collect();
    kept.into_iter().cloned().collect()
}

/// What `change` owes each of `sessions` (each with the trees it draws
/// on): the live ones on its tree whose context does not cover it, with the
/// bundles of their loadout it touched and the paths of it they draw on;
/// by session id, in their order.
pub fn owed_by(
    change: &Change,
    sessions: &[(SessionRecord, Vec<String>)],
) -> Vec<(String, Vec<String>, Vec<String>)> {
    sessions
        .iter()
        .filter(|(record, trees)| record.ended_at.is_none() && trees.contains(&change.tree))
        .filter_map(|(record, _)| {
            let (loadout, handed) = (record.loadout.as_ref()?, record.context.as_ref()?);
            if !handed.owes(&change.tree, change.sequence, change.recorded) {
                return None;
            }
            let named = touched(loadout, &change.paths);
            (!named.is_empty())
                .then(|| (record.id.clone(), named, drawn_on(loadout, &change.paths)))
        })
        .collect()
}

/// What a loadout covering `covered` is owed by its trees as the
/// checkouts hold them (`resources` gives a tree's paths, each at the
/// sequence of the last event that named it): per tree it covers, the
/// latest sequence past its position of a path the loadout touched, with
/// every bundle so touched. A tree it covers no position of is the
/// changes' to judge, as they are handled.
pub fn owed_at_load(
    loadout: &SessionLoadout,
    covered: &BTreeMap<String, u64>,
    resources: impl Fn(&str) -> Vec<(String, u64)>,
) -> Vec<(String, u64, Vec<String>, Vec<String>)> {
    let mut owed = Vec::new();
    for (tree, at) in covered {
        let past: Vec<(String, u64)> = resources(tree)
            .into_iter()
            .filter(|(_, sequence)| sequence > at)
            .collect();
        let paths: Vec<String> = past.iter().map(|(path, _)| path.clone()).collect();
        let named = touched(loadout, &paths);
        if named.is_empty() {
            continue;
        }
        let drawn = drawn_on(loadout, &paths);
        let latest = past
            .iter()
            .filter(|(path, _)| drawn.contains(path))
            .map(|(_, sequence)| *sequence)
            .max()
            .unwrap_or_default();
        owed.push((tree.clone(), latest, named, drawn_on(loadout, &paths)));
    }
    owed
}

/// Where each tree `checkouts` holds that `wanted` names is: its latest
/// sequence that changed a path (its baseline's when none did since),
/// sorted by tree.
pub fn positions(
    checkouts: &library_checkout::LibraryCheckout,
    wanted: impl Fn(&str) -> bool,
) -> Vec<sessions::TreePosition> {
    let mut positions: Vec<sessions::TreePosition> = checkouts
        .trees()
        .filter(|tree| wanted(&tree.name))
        .map(|tree| sessions::TreePosition {
            tree: tree.name.clone(),
            sequence: tree
                .resources
                .values()
                .map(|resource| resource.sequence)
                .chain([tree.baseline_sequence])
                .max()
                .unwrap_or_default(),
        })
        .collect();
    positions.sort_by(|a, b| a.tree.cmp(&b.tree));
    positions
}

/// The handler: it reads the session index and the checkouts (both held,
/// caught up by the same loop before it), and records what is owed through
/// `edge`.
pub struct OwedContexts<E> {
    edge: Arc<E>,
    index: SessionIndexes,
    checkouts: LibraryCheckouts,
    trees: Arc<dyn SessionTrees>,
}

impl<E: Edge> OwedContexts<E> {
    pub fn new(
        edge: Arc<E>,
        index: SessionIndexes,
        checkouts: LibraryCheckouts,
        trees: Arc<dyn SessionTrees>,
    ) -> OwedContexts<E> {
        OwedContexts {
            edge,
            index,
            checkouts,
            trees,
        }
    }

    /// Every session the index holds, with the trees each draws on.
    fn sessions(&self) -> Vec<(SessionRecord, Vec<String>)> {
        let records = self.index.index().records();
        records
            .into_iter()
            .filter(|record| record.ended_at.is_none() && record.loadout.is_some())
            .map(|record| {
                let trees = self.trees.trees(&record);
                (record, trees)
            })
            .collect()
    }

    /// `fact` recorded on the session `session`, keyed `key`.
    async fn owe(
        &self,
        session: &str,
        fact: sessions::RecordContextOwed,
        key: &str,
    ) -> Result<(), CodedError> {
        let Ok(root) = uuid::Uuid::parse_str(session) else {
            return Ok(());
        };
        let cover = Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        self.edge
            .handle(sessions::fact_book(cover, &fact, key))
            .await
            .map(|_| ())
            .map_err(|failure| {
                CodedError::bug(
                    "daemon-application-owed-not-recorded",
                    [("ext_err_txt".to_string(), format!("{failure:?}"))],
                )
            })
    }

    /// A library stream's book: each change it holds owed to the sessions
    /// it touches.
    async fn library(&self, book: &EventBook) -> Result<(), CodedError> {
        let root = book
            .cover
            .as_ref()
            .and_then(|cover| cover.root.clone())
            .unwrap_or_default();
        // The checkouts are caught up before it in the same round; a tree
        // they do not hold yet is handed again at the next.
        let Some(tree) = self
            .checkouts
            .checkouts()
            .tree(&root)
            .map(|tree| tree.name.clone())
        else {
            return Err(CodedError::bug(
                "daemon-application-owed-tree-not-held",
                [(
                    "tree".to_string(),
                    uuid::Uuid::from_slice(&root.value)
                        .map(|id| id.to_string())
                        .unwrap_or_default(),
                )],
            ));
        };
        let mut sessions = None;
        for page in &book.pages {
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let Ok(event) = LibraryEvent::from_any(event) else {
                continue;
            };
            let recorded = page
                .meta
                .as_ref()
                .and_then(|meta| meta.recorded_at)
                .unwrap_or_default();
            let Some(change) = change_of(&tree, &event, page.sequence, recorded) else {
                continue;
            };
            let sessions = sessions.get_or_insert_with(|| self.sessions());
            for (session, bundles, paths) in owed_by(&change, sessions) {
                let key = sessions::owed_key(&session, &change.tree, change.sequence);
                let fact = sessions::RecordContextOwed {
                    tree: change.tree.clone(),
                    sequence: change.sequence,
                    bundles,
                    paths,
                };
                self.owe(&session, fact, &key).await?;
            }
        }
        Ok(())
    }

    /// A session stream's book: a loadout it holds judged against the
    /// trees it covers, as the checkouts hold them now.
    async fn session(&self, book: &EventBook) -> Result<(), CodedError> {
        let Some(root) = book.cover.as_ref().and_then(|cover| cover.root.as_ref()) else {
            return Ok(());
        };
        let session = uuid::Uuid::from_slice(&root.value)
            .map(|id| id.to_string())
            .unwrap_or_default();
        for page in &book.pages {
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let Ok(SessionEvent::LoadoutRecorded(recorded)) = SessionEvent::from_any(event) else {
                continue;
            };
            let covered: BTreeMap<String, u64> = recorded
                .covered
                .iter()
                .map(|position| (position.tree.clone(), position.sequence))
                .collect();
            let owed = {
                let checkouts = self.checkouts.checkouts();
                owed_at_load(&recorded.loadout, &covered, |tree| {
                    checkouts
                        .tree(&library_tree::tree_root(tree))
                        .map(|row| {
                            row.resources
                                .iter()
                                .map(|(path, resource)| (path.clone(), resource.sequence))
                                .collect()
                        })
                        .unwrap_or_default()
                })
            };
            for (tree, sequence, bundles, paths) in owed {
                let key = sessions::owed_at_load_key(&session, &tree, sequence);
                let fact = sessions::RecordContextOwed {
                    tree,
                    sequence,
                    bundles,
                    paths,
                };
                self.owe(&session, fact, &key).await?;
            }
        }
        Ok(())
    }
}

impl<E: Edge> ProjectorHandler for OwedContexts<E> {
    fn name(&self) -> &str {
        NAME
    }

    /// A book of either domain handled; an owed context not recorded fails
    /// the book, so the catch-up hands it again.
    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let book = request.events.clone().unwrap_or_default();
        match book.cover.as_ref().map(|cover| cover.domain.as_str()) {
            Some(LIBRARY_DOMAIN) => self.library(&book).await?,
            Some(SESSION_DOMAIN) => self.session(&book).await?,
            _ => {}
        }
        Ok(ProjectResponse { projection: None })
    }
}

/// `owed` run by a catch-up whose positions the store keeps.
pub fn caught_up<E: Edge>(owed: OwedContexts<E>) -> CatchUp<OwedContexts<E>> {
    CatchUp::stored(&SOURCES, owed)
        .expect("the owed contexts read the library and sessions domains")
}

#[cfg(test)]
#[path = "owed.test.rs"]
mod tests;
