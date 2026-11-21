//! The deps files over a RECORDING store (step 10c U4): every write a use
//! case makes is kept, never made, and the reads after it see it. The daemon
//! plans a pull, an upgrade or a remote's write over it where the caller
//! stands, then makes what it recorded as one retryable sequence of its own.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, Mutex, MutexGuard};

use deps_ports::{BundleSlot, DepsFiles, RepoTree, Why};

use crate::{ProjectFiles, IDENTITY_MARKER};

/// A tree a use case installed, as an install would leave it: its directory
/// below the app directory, and each file below that (the identity marker
/// last), with whether it is written executable.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Install {
    pub slot: BundleSlot,
    pub sha: String,
    /// `cache/bundles/<segments>/<bundle>/<sha>`, `/`-separated.
    pub path: String,
    pub files: Vec<(String, Vec<u8>, bool)>,
}

/// What a use case would have written, in what it is of.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Recorded {
    /// `remotes.yaml` as it was last written.
    pub registry: Option<String>,
    /// `lock.yaml` as it was last written.
    pub lock: Option<String>,
    /// Each tree installed, in order (one per identity and commit).
    pub installs: Vec<Install>,
    /// Each installed tree taken away (one another commit's tree replaced,
    /// or a prune), below the app directory, in order, once each.
    pub removed: Vec<String>,
}

/// [`ProjectFiles`], its writes recorded instead of made.
pub struct RecordingDepsFiles {
    inner: Arc<ProjectFiles>,
    recorded: Mutex<Recorded>,
}

impl RecordingDepsFiles {
    pub fn over(inner: Arc<ProjectFiles>) -> RecordingDepsFiles {
        RecordingDepsFiles {
            inner,
            recorded: Mutex::new(Recorded::default()),
        }
    }

    /// What was recorded, taken.
    pub fn take(&self) -> Recorded {
        std::mem::take(&mut *self.held())
    }

    fn held(&self) -> MutexGuard<'_, Recorded> {
        self.recorded.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// The tree recorded for `slot` at `sha`, as its files.
    fn recorded_tree(&self, slot: &BundleSlot, sha: &str) -> Option<RepoTree> {
        self.held()
            .installs
            .iter()
            .find(|install| install.slot.identity == slot.identity && install.sha == sha)
            .map(|install| {
                install
                    .files
                    .iter()
                    .filter(|(path, _, _)| path != IDENTITY_MARKER)
                    .map(|(path, bytes, _)| (path.clone(), bytes.clone()))
                    .collect()
            })
    }

    /// `path` recorded removed, once.
    fn remove(recorded: &mut Recorded, path: String) {
        if !recorded.removed.contains(&path) {
            recorded.removed.push(path);
        }
    }
}

impl DepsFiles for RecordingDepsFiles {
    fn registry_path(&self) -> String {
        self.inner.registry_path()
    }

    fn read_registry(&self) -> Result<Option<String>, Why> {
        if let Some(text) = self.held().registry.clone() {
            return Ok(Some(text));
        }
        self.inner.read_registry()
    }

    fn write_registry(&self, text: &str) -> Result<(), Why> {
        self.held().registry = Some(text.to_string());
        Ok(())
    }

    fn lock_path(&self) -> String {
        self.inner.lock_path()
    }

    fn read_lock(&self) -> Result<Option<String>, Why> {
        if let Some(text) = self.held().lock.clone() {
            return Ok(Some(text));
        }
        self.inner.read_lock()
    }

    fn write_lock(&self, text: &str) -> Result<(), Why> {
        self.held().lock = Some(text.to_string());
        Ok(())
    }

    fn is_installed(&self, slot: &BundleSlot, sha: &str) -> bool {
        self.recorded_tree(slot, sha).is_some() || self.inner.is_installed(slot, sha)
    }

    /// The tree as an install writes it, recorded; the identity's trees at
    /// any other commit recorded taken away.
    fn install(
        &self,
        slot: &BundleSlot,
        sha: &str,
        tree: &RepoTree,
        executable: &BTreeSet<String>,
    ) -> Result<(), Why> {
        let mut files: Vec<(String, Vec<u8>, bool)> = tree
            .iter()
            .map(|(path, bytes)| (path.clone(), bytes.clone(), executable.contains(path)))
            .collect();
        files.push((
            IDENTITY_MARKER.to_string(),
            slot.identity.as_bytes().to_vec(),
            false,
        ));
        let path = self.inner.install_path(slot, sha);
        let replaced = self.inner.replaced_by(slot, sha);
        let mut recorded = self.held();
        recorded
            .installs
            .retain(|install| !(install.slot.identity == slot.identity && install.sha == sha));
        recorded.installs.push(Install {
            slot: slot.clone(),
            sha: sha.to_string(),
            path,
            files,
        });
        for gone in replaced {
            Self::remove(&mut recorded, gone);
        }
        Ok(())
    }

    fn installed_path(&self, slot: &BundleSlot, sha: &str) -> String {
        self.inner.installed_path(slot, sha)
    }

    fn read_installed(&self, slot: &BundleSlot, sha: &str) -> Result<Option<RepoTree>, Why> {
        if let Some(tree) = self.recorded_tree(slot, sha) {
            return Ok(Some(tree));
        }
        self.inner.read_installed(slot, sha)
    }

    /// What a prune would take, recorded: the trees on disk not kept.
    fn prune(&self, keep: &[(String, String)]) -> Result<Vec<String>, Why> {
        let (dirs, gone) = self.inner.prunable(keep)?;
        let mut recorded = self.held();
        for dir in dirs {
            Self::remove(&mut recorded, dir);
        }
        Ok(gone)
    }

    fn local_bundle(&self, name: &str) -> Result<Option<String>, Why> {
        self.inner.local_bundle(name)
    }

    fn read_local_bundle(&self, name: &str) -> Result<BTreeMap<String, Vec<u8>>, Why> {
        self.inner.read_local_bundle(name)
    }

    /// A recording store removes nothing: no planned write takes an
    /// authored bundle away through it.
    fn remove_local_bundle(&self, name: &str) -> Result<(), Why> {
        Err(format!("the bundle {name} is not removed by a plan"))
    }

    fn is_directory(&self, path: &str) -> bool {
        self.inner.is_directory(path)
    }

    fn exists(&self, path: &str) -> bool {
        self.inner.exists(path)
    }
}

#[cfg(test)]
#[path = "recording.test.rs"]
mod tests;
