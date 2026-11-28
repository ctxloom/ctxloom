//! The daemon's composition of per-project seal keys (owner ruling
//! 2026-11-27): what the application needs of a project ([`Registered`]: its
//! registered root, where its live `.env` is, and whether the user trusts
//! it), a cache of each project's key set keyed by its `.env`'s stat
//! ([`Cached`]: edge state like the scanner's memo, never projector state),
//! and the telling of a key set's ids to the project's stream
//! ([`Announcing`]).

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::SystemTime;

use file_system::Fs;
use run_ports::{
    Edge, KeySet, KeysSeen, ProjectEvidence, ProjectFacts, ProjectSecrets, SealingError,
};
use session_index::SessionIndexes;
use trust_index::TrustIndexes;

/// What the daemon's projections say of a project, by its harp or its id.
pub(crate) struct Registered {
    pub(crate) index: SessionIndexes,
    pub(crate) trusts: TrustIndexes,
}

/// The registered row of `project`, by harp else by id.
pub(crate) fn row_of(index: &SessionIndexes, project: &str) -> Option<session_index::ProjectRow> {
    let index = index.index();
    index
        .project_named_by(project)
        .or_else(|| index.project_identified_by(project))
}

impl ProjectFacts for Registered {
    fn evidence(&self, project: &str) -> Option<ProjectEvidence> {
        let row = row_of(&self.index, project)?;
        Some(ProjectEvidence {
            trusted: self.trusts.index().trusts(&row.project_id),
            root: row.root,
        })
    }
}

/// What a `.env` was when its key set was read: its size and its time, and
/// the trust it was read under (a revoked trust reads again).
#[derive(Clone, Debug, PartialEq, Eq)]
struct Stamp {
    len: u64,
    modified: Option<SystemTime>,
    root: String,
    trusted: bool,
}

/// Each project's key set, read again when its `.env`'s stat or its trust
/// moves.
pub(crate) struct Cached {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) inner: Arc<dyn ProjectSecrets>,
    sets: Mutex<BTreeMap<String, (Stamp, KeySet)>>,
}

impl Cached {
    pub(crate) fn new(fs: Arc<dyn Fs>, inner: Arc<dyn ProjectSecrets>) -> Cached {
        Cached {
            fs,
            inner,
            sets: Mutex::new(BTreeMap::new()),
        }
    }
}

impl ProjectSecrets for Cached {
    fn key_set(&self, project: &str, evidence: &ProjectEvidence) -> Result<KeySet, SealingError> {
        let meta = self.fs.metadata(&Path::new(&evidence.root).join(".env"));
        let stamp = Stamp {
            len: meta.as_ref().map_or(0, |meta| meta.len),
            modified: meta.as_ref().ok().map(|meta| meta.modified),
            root: evidence.root.clone(),
            trusted: evidence.trusted,
        };
        let mut sets = self.sets.lock().unwrap_or_else(PoisonError::into_inner);
        if let Some((held, keys)) = sets.get(project) {
            if *held == stamp {
                return Ok(keys.clone());
            }
        }
        let keys = self.inner.key_set(project, evidence);
        match &keys {
            Ok(keys) => {
                sets.insert(project.to_string(), (stamp, keys.clone()));
            }
            Err(_) => {
                sets.remove(project);
            }
        }
        keys
    }
}

/// Tells a key set's ids to its project's stream: `RecordSealKeySeen`, one
/// per key, through the core's own edge, off the calling thread (the sealing
/// port is synchronous). A resend is a DUPLICATE and a lost one is told
/// again by the next set.
pub(crate) struct Announcing<E> {
    pub(crate) edge: Arc<E>,
    pub(crate) index: SessionIndexes,
    pub(crate) runtime: tokio::runtime::Handle,
}

impl<E: Edge + 'static> KeysSeen for Announcing<E> {
    fn seen(&self, project: &str, keys: &KeySet) {
        let Some(row) = row_of(&self.index, project) else {
            return;
        };
        let cover = angzarr_embedded::v1::Cover {
            domain: sessions::PROJECTS_DOMAIN.to_string(),
            root: Some(sessions::project_id_root(&row.project_id)),
            edition: None,
        };
        let told = std::iter::once((keys.current(), true))
            .chain(keys.earlier().iter().map(|key| (key, false)))
            .map(|(key, current)| {
                let fact = sessions::RecordSealKeySeen {
                    key_id: key.id.clone(),
                    fingerprint: key.fingerprint.clone(),
                    current,
                };
                let id = format!("sealkey:{}:{}:{current}", row.project_id, key.id);
                sessions::fact_book(cover.clone(), &fact, &id)
            })
            .collect::<Vec<_>>();
        let edge = Arc::clone(&self.edge);
        self.runtime.spawn(async move {
            for book in told {
                let _ = edge.handle(book).await;
            }
        });
    }
}

#[cfg(test)]
#[path = "project_secrets.test.rs"]
mod tests;
