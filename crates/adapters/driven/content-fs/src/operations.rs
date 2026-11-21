//! The atomic operations on ctxloom's trees (step 10b U8; S50, D3(a), D4,
//! L12), over the file-system seam. A tree is named by its directory (its
//! `.ctxloom`); every path is below it.
//!
//! - A file is written under `state/staging/<request id>/` and renamed over
//!   its target; nothing temporary is ever beside the target.
//! - A new directory is built whole there, then renamed in with NOREPLACE:
//!   one that appeared meanwhile is left as it was (`Exists`). The tree
//!   itself made whole (its root, `""`: a first init's `.ctxloom`, step 10c
//!   U6) cannot be staged inside itself: it alone is built BESIDE it, under
//!   `<tree>.staging/<request id>/`, then renamed in the same way; that
//!   staging is swept with the tree's.
//! - A removal is one rename into `state/trash/<request id>/<path>`.
//! - A move is one NOREPLACE rename, within the tree or into another
//!   (its target below that tree's directory; QUESTIONS 1(a)).
//! - Each rename's directories are synced after it (both, when it moves
//!   between two), and a purged slot's trash directory after the purge.
//!
//! A path that is not plain (absolute, climbing, empty) or that goes
//! through a link inside the tree is refused before anything is touched.
//! A filesystem with no NOREPLACE rename fails
//! the operation (D4), exit 10.

use std::io;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use ctxloom_ports::{
    is_plain_relative, Failure, Operated, Operation, Operations, Point, Put, STAGING_DIR, TRASH_DIR,
};
use file_system::{Fs, Kind, Perms};
use os::Mode;

use crate::error::ContentError;
use crate::join_rel;

/// Where the tree `tree` is staged when it is made whole (step 10c U6):
/// beside it, `<tree>.staging`, never inside the directory it creates.
fn beside(tree: &Path) -> PathBuf {
    let mut name = tree.file_name().unwrap_or_default().to_os_string();
    name.push(".staging");
    tree.with_file_name(name)
}

/// The atomic operations, on a filesystem.
pub struct TreeOperations {
    fs: Arc<dyn Fs>,
}

impl TreeOperations {
    pub fn new(fs: Arc<dyn Fs>) -> TreeOperations {
        TreeOperations { fs }
    }

    /// `rel` below `tree`, refused unless it is a plain path that goes
    /// through no link inside the tree (its own last step may be one).
    fn inside(&self, tree: &Path, rel: &str) -> Result<PathBuf, Failure> {
        let bad = || -> Failure {
            Box::new(ContentError::BadPath {
                path: rel.to_string(),
            })
        };
        if !is_plain_relative(rel) || rel.contains('\0') {
            return Err(bad());
        }
        let parts: Vec<&str> = rel.split('/').collect();
        let mut here = tree.to_path_buf();
        for part in &parts[..parts.len() - 1] {
            here.push(part);
            match self.fs.symlink_metadata(&here) {
                Ok(meta) if meta.kind == Kind::Symlink => return Err(bad()),
                Err(e) if e.kind() == io::ErrorKind::NotFound => break,
                Err(e) => return Err(failed_at(ContentError::list_dir, &here, &e)),
                Ok(_) => {}
            }
        }
        Ok(join_rel(tree, rel))
    }

    /// `rel` (a path below `tree`, `state/staging`) refused when `state/` or
    /// any step of it is a link (review 9): a cloned repository's
    /// `state -> /elsewhere` would make ctxloom create and remove files
    /// there. A step not there is fine; the last step is judged too.
    fn state_clear(&self, tree: &Path, rel: &str) -> Result<(), Failure> {
        let mut here = tree.to_path_buf();
        for part in rel.split('/') {
            here.push(part);
            match self.fs.symlink_metadata(&here) {
                Ok(meta) if meta.kind == Kind::Symlink => {
                    return Err(Box::new(ContentError::StateLink {
                        path: here.display().to_string(),
                    }))
                }
                Err(e) if e.kind() == io::ErrorKind::NotFound => break,
                Err(e) => return Err(failed_at(ContentError::list_dir, &here, &e)),
                Ok(_) => {}
            }
        }
        Ok(())
    }

    /// `<tree>.staging` refused when it is a link (a first init's tree is
    /// built there).
    fn beside_clear(&self, tree: &Path) -> Result<(), Failure> {
        let at = beside(tree);
        match self.fs.symlink_metadata(&at) {
            Ok(meta) if meta.kind == Kind::Symlink => Err(Box::new(ContentError::StateLink {
                path: at.display().to_string(),
            })),
            Err(e) if e.kind() != io::ErrorKind::NotFound => {
                Err(failed_at(ContentError::list_dir, &at, &e))
            }
            _ => Ok(()),
        }
    }

    /// The request's own directory below `under` (its staging, its trash
    /// slot), refused unless the id is one plain name and `under` goes
    /// through no link.
    fn of_request(&self, tree: &Path, under: &str, request_id: &str) -> Result<PathBuf, Failure> {
        if !is_plain_relative(request_id) || request_id.contains('/') {
            return Err(Box::new(ContentError::BadRequest {
                request_id: request_id.to_string(),
            }));
        }
        self.state_clear(tree, under)?;
        Ok(join_rel(tree, under).join(request_id))
    }

    fn exists(&self, path: &Path) -> Result<bool, Failure> {
        match self.fs.symlink_metadata(path) {
            Ok(_) => Ok(true),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(false),
            Err(e) => Err(failed_at(ContentError::list_dir, path, &e)),
        }
    }

    fn make_dirs(&self, dir: &Path) -> Result<(), Failure> {
        self.fs
            .create_dir_all(dir, Mode::from_bits(0o755))
            .map_err(|e| failed_at(ContentError::make_dir, dir, &e))
    }

    fn remove_all(&self, path: &Path) -> Result<(), Failure> {
        match self.fs.remove_dir_all(path) {
            Err(e) if e.kind() != io::ErrorKind::NotFound => {
                Err(failed_at(ContentError::remove_path, path, &e))
            }
            _ => Ok(()),
        }
    }

    fn write(&self, path: &Path, put: &Put) -> Result<(), Failure> {
        if let Some(parent) = path.parent() {
            self.make_dirs(parent)?;
        }
        let mode = Mode::from_bits(if put.executable { 0o755 } else { 0o644 });
        self.fs
            .write_atomic(path, &put.bytes, Perms::Exact(mode))
            .map_err(|e| failed_at(ContentError::write_file, path, &e))
    }

    /// A fresh staging for the request: an earlier try's dropped.
    fn staging(&self, tree: &Path, request_id: &str) -> Result<PathBuf, Failure> {
        self.fresh(&self.of_request(tree, STAGING_DIR, request_id)?)
    }

    /// `dir` removed when nothing is left in it (another request's staging
    /// is never touched); none there is done.
    fn drop_if_empty(&self, dir: &Path) -> Result<(), Failure> {
        match self.fs.read_dir(dir) {
            Ok(entries) if entries.is_empty() => self.remove_all(dir),
            Ok(_) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(failed_at(ContentError::list_dir, dir, &e)),
        }
    }

    /// `dir` made afresh: what an earlier try left there dropped.
    fn fresh(&self, dir: &Path) -> Result<PathBuf, Failure> {
        self.remove_all(dir)?;
        self.make_dirs(dir)?;
        Ok(dir.to_path_buf())
    }

    /// `dir`'s entries made durable (`Fs::sync_dir`: fsync on unix,
    /// nothing on Windows).
    fn sync(&self, dir: &Path) -> Result<(), Failure> {
        self.fs
            .sync_dir(dir)
            .map_err(|e| failed_at(ContentError::sync_dir, dir, &e))
    }

    /// The trash slot removed whole, and the trash directory synced, so a
    /// power loss cannot bring back a slot the record dropped. A slot not
    /// there (a retry, a tree with no trash) is done and syncs nothing.
    fn purge(&self, slot: &Path) -> Result<(), Failure> {
        match self.fs.remove_dir_all(slot) {
            Ok(()) => self.sync(slot.parent().unwrap_or(slot)),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(failed_at(ContentError::remove_path, slot, &e)),
        }
    }

    /// `to`'s directory made and `from` renamed there (NOREPLACE unless
    /// `replace`): whether it was renamed, false when something is at `to`.
    /// Once renamed, `to`'s directory is synced, then `from`'s when it is
    /// another, so a power loss cannot undo a rename the record holds done.
    fn rename(&self, from: &Path, to: &Path, replace: bool) -> Result<bool, Failure> {
        let dir = to.parent().unwrap_or(to);
        self.make_dirs(dir)?;
        let renamed = if replace {
            self.fs.rename(from, to)
        } else {
            self.fs.rename_noreplace(from, to)
        };
        match renamed {
            Ok(()) => {}
            Err(e) if !replace && e.kind() == io::ErrorKind::AlreadyExists => return Ok(false),
            Err(e) => {
                return Err(Box::new(ContentError::Rename {
                    from: from.display().to_string(),
                    to: to.display().to_string(),
                    detail: e.to_string(),
                }))
            }
        }
        self.sync(dir)?;
        let source = from.parent().unwrap_or(from);
        if source != dir {
            self.sync(source)?;
        }
        Ok(true)
    }

    fn replace(
        &self,
        tree: &Path,
        request_id: &str,
        put: &Put,
        at: &dyn Fn(Point),
    ) -> Result<Operated, Failure> {
        let target = self.inside(tree, &put.path)?;
        let staging = self.staging(tree, request_id)?;
        let staged = staging.join(leaf(&put.path));
        self.write(&staged, put)?;
        at(Point::Staged);
        self.rename(&staged, &target, true)?;
        self.remove_all(&staging)?;
        Ok(Operated::Done)
    }

    fn create_dir(
        &self,
        tree: &Path,
        request_id: &str,
        (path, files): (&str, &[Put]),
        at: &dyn Fn(Point),
    ) -> Result<Operated, Failure> {
        for put in files {
            if !is_plain_relative(&put.path) || put.path.contains('\0') {
                return Err(Box::new(ContentError::BadPath {
                    path: format!("{path}/{}", put.path),
                }));
            }
        }
        // The tree's root: staged beside it (step 10c U6).
        let (target, staging, staged) = if path.is_empty() {
            if !is_plain_relative(request_id) || request_id.contains('/') {
                return Err(Box::new(ContentError::BadRequest {
                    request_id: request_id.to_string(),
                }));
            }
            self.beside_clear(tree)?;
            let staging = self.fresh(&beside(tree).join(request_id))?;
            (tree.to_path_buf(), staging.clone(), staging)
        } else {
            let target = self.inside(tree, path)?;
            let staging = self.staging(tree, request_id)?;
            let staged = staging.join(leaf(path));
            (target, staging, staged)
        };
        self.make_dirs(&staged)?;
        for put in files {
            self.write(&join_rel(&staged, &put.path), put)?;
        }
        at(Point::Staged);
        let renamed = self.rename(&staged, &target, false)?;
        self.remove_all(&staging)?;
        if path.is_empty() {
            self.drop_if_empty(&beside(tree))?;
        }
        Ok(if renamed {
            Operated::Done
        } else {
            Operated::Exists {
                path: path.to_string(),
            }
        })
    }

    fn trash(
        &self,
        tree: &Path,
        request_id: &str,
        path: &str,
        at: &dyn Fn(Point),
    ) -> Result<Operated, Failure> {
        let source = self.inside(tree, path)?;
        let slot = join_rel(&self.of_request(tree, TRASH_DIR, request_id)?, path);
        if !self.exists(&source)? {
            // Retried after its rename: what it took is in its slot.
            return Ok(if self.exists(&slot)? {
                Operated::Done
            } else {
                Operated::Missing {
                    path: path.to_string(),
                }
            });
        }
        if !self.rename(&source, &slot, false)? {
            return Err(Box::new(ContentError::Rename {
                from: source.display().to_string(),
                to: slot.display().to_string(),
                detail: "the trash slot already holds this path".to_string(),
            }));
        }
        at(Point::Trashed);
        Ok(Operated::Done)
    }

    fn move_path(
        &self,
        tree: &Path,
        (from, to): (&str, &str),
        into: Option<&Path>,
    ) -> Result<Operated, Failure> {
        let source = self.inside(tree, from)?;
        let target = self.inside(into.unwrap_or(tree), to)?;
        if !self.exists(&source)? {
            // Retried after its rename: it is at its target.
            return Ok(if self.exists(&target)? {
                Operated::Done
            } else {
                Operated::Missing {
                    path: from.to_string(),
                }
            });
        }
        Ok(if self.rename(&source, &target, false)? {
            Operated::Done
        } else {
            Operated::Exists {
                path: to.to_string(),
            }
        })
    }
}

/// The last step of a `/`-separated path.
impl TreeOperations {
    /// Another tool's file (step 10c U10): written whole, atomically
    /// beside it, its directories made; or removed, with each directory
    /// that leaves empty below `keep`. Done already is done.
    fn outside(
        &self,
        path: &Path,
        bytes: Option<&[u8]>,
        executable: bool,
        keep: &Path,
    ) -> Result<Operated, Failure> {
        if !path.is_absolute() {
            return Err(Box::new(ContentError::BadPath {
                path: path.display().to_string(),
            }));
        }
        let Some(bytes) = bytes else {
            match self.fs.remove_file(path) {
                Err(e) if e.kind() != io::ErrorKind::NotFound => {
                    return Err(failed_at(ContentError::write_file, path, &e))
                }
                _ => {}
            }
            let mut dir = path.parent();
            while let Some(here) = dir.filter(|here| *here != keep && here.starts_with(keep)) {
                let empty = self
                    .fs
                    .read_dir(here)
                    .is_ok_and(|entries| entries.is_empty());
                if !empty || self.fs.remove_dir_all(here).is_err() {
                    break;
                }
                dir = here.parent();
            }
            return Ok(Operated::Done);
        };
        if let Some(dir) = path.parent() {
            self.make_dirs(dir)?;
        }
        let perms = if executable {
            Perms::Exact(Mode::from_bits(0o755))
        } else {
            Perms::KeepOr(Mode::FILE)
        };
        self.fs
            .write_atomic(path, bytes, perms)
            .map_err(|e| failed_at(ContentError::write_file, path, &e))?;
        Ok(Operated::Done)
    }
}

fn leaf(path: &str) -> &str {
    path.rsplit('/').next().unwrap_or(path)
}

fn failed_at(make: fn(String, String) -> ContentError, path: &Path, error: &io::Error) -> Failure {
    Box::new(make(path.display().to_string(), error.to_string()))
}

impl Operations for TreeOperations {
    fn operate(
        &self,
        tree: &str,
        request_id: &str,
        operation: &Operation,
        at: &dyn Fn(Point),
    ) -> Result<Operated, Failure> {
        let tree = Path::new(tree);
        match operation {
            Operation::Replace(put) => self.replace(tree, request_id, put, at),
            Operation::CreateDir { path, files } => {
                self.create_dir(tree, request_id, (path, files), at)
            }
            Operation::Trash { path } => self.trash(tree, request_id, path, at),
            Operation::Move { from, to, into } => {
                self.of_request(tree, TRASH_DIR, request_id)?;
                self.move_path(tree, (from, to), into.as_deref().map(Path::new))
            }
            Operation::Purge => {
                self.purge(&self.of_request(tree, TRASH_DIR, request_id)?)?;
                Ok(Operated::Done)
            }
            Operation::Outside {
                path,
                bytes,
                executable,
                keep,
            } => self.outside(
                Path::new(path),
                bytes.as_deref(),
                *executable,
                Path::new(keep),
            ),
        }
    }

    fn sweep_staging(&self, tree: &str) -> Result<(), Failure> {
        let tree = Path::new(tree);
        self.state_clear(tree, STAGING_DIR)?;
        self.beside_clear(tree)?;
        self.remove_all(&join_rel(tree, STAGING_DIR))?;
        self.remove_all(&beside(tree))
    }

    fn purge_slot(&self, tree: &str, request_id: &str) -> Result<(), Failure> {
        self.purge(&self.of_request(Path::new(tree), TRASH_DIR, request_id)?)
    }
}

#[cfg(test)]
#[path = "operations.test.rs"]
mod tests;
