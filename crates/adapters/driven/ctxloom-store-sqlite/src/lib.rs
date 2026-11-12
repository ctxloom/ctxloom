//! A project's ctxloom store (D26): angzarr-store-sqlite's [`SqliteStore`]
//! at `<home>/.ctxloom/projects/<project-id>/ctxloom.db`, beside taskloom's
//! `state.db` and never the same file. All file I/O goes through the
//! `file-system` seam, so a test under a temporary home touches nothing
//! else.
//!
//! - [`Stores::open`] opens the project's store to write, making its
//!   directory and the file (migrated if need be).
//! - The opening is `sqlite-store-opener`'s: this crate is the file name, the
//!   placement under the home and the [`ProjectStores`] port. A store from
//!   before the embedded store is refused ([`OpenError::PredatesEmbedded`]).
//! - ctxloom's home directory, `<home>/.ctxloom`, is its owner's alone
//!   (0700, owner ruling 2026-11-12), set on every open that finds a store,
//!   to write or to read, so one another writer made open to others is made
//!   so on the store's next open; the store itself (its directory 0700, the
//!   file, `-wal` and `-shm` 0600) is the opener's. On Windows the seam sets
//!   no mode (ruling M8: the profile's ACL holds it). A mode that cannot be
//!   set fails the open ([`OpenError::Restrict`]), the store closed.
//! - Errors are the opener's and the project id's, each under its own code.
//! - A store is closed, never dropped, where its file matters after (ruling
//!   131): [`SqliteStore::close`] waits for the connection's close, so when
//!   it returns the file holds every commit with no log beside it. A store
//!   dropped instead is closed on the driver's thread whenever that runs,
//!   which a process that exits first never lets it do. [`SqliteStores`]
//!   closes a store it gives up for another, and [`SqliteStores::close`] the
//!   one it holds; whoever [`Stores::open`]s one closes it.

mod error;

use std::path::PathBuf;
use std::sync::Arc;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::StoreError;
use file_system::Fs;
use os::Mode;
use ports::Project;
use session_ports::ProjectStores;
use sqlite_store_opener::Opener;

pub use error::StoreOpenError;
#[cfg(any(test, feature = "test-support"))]
pub use sqlite_store_opener::diagnostic_samples;
pub use sqlite_store_opener::{OpenError, EN_US};

/// The app's directory under a home.
const APP_DIR: &str = ".ctxloom";

/// ctxloom's home directory's mode: its owner's alone.
const PRIVATE_DIR: Mode = Mode::from_bits(0o700);

/// The store's file name: beside taskloom's `state.db`, never the same file.
const FILE_NAME: &str = "ctxloom.db";

/// The ctxloom stores under one home.
pub struct Stores {
    fs: Arc<dyn Fs>,
    opener: Opener,
    home: PathBuf,
}

impl Stores {
    /// The stores under `home`, on `fs`; none opened yet.
    pub fn new(fs: Arc<dyn Fs>, home: impl Into<PathBuf>) -> Stores {
        Stores {
            opener: Opener::new(Arc::clone(&fs), FILE_NAME),
            fs,
            home: home.into(),
        }
    }

    /// Where `project`'s database is: `<home>/.ctxloom/projects/<project>/ctxloom.db`.
    ///
    /// Refused where `project` is not one plain name (empty, absolute, with a
    /// path separator or NUL, or `.` or `..`): the shared `project-dir`
    /// crate's check, so an id cannot place the database outside the
    /// projects directory.
    pub fn path(&self, project: &str) -> Result<PathBuf, StoreOpenError> {
        project_dir::project_dir(&self.home, APP_DIR, project)
            .map(|dir| self.opener.file_in(&dir))
            .map_err(StoreOpenError::UnsafeProject)
    }

    /// `project`'s store, read-only: `None` where it has no database, and
    /// nothing is made. A store from before the embedded store is refused.
    pub async fn open_existing(&self, project: &str) -> Result<Option<SqliteStore>, StoreError> {
        let path = self
            .path(project)
            .map_err(StoreOpenError::into_store_error)?;
        let Some(store) = self
            .opener
            .open_existing(&path)
            .await
            .map_err(OpenError::into_store_error)?
        else {
            return Ok(None);
        };
        self.private(store)
            .await
            .map(Some)
            .map_err(OpenError::into_store_error)
    }

    /// `project`'s store, opened to write: its directory and file made and
    /// migrated if need be. A store from before the embedded store is
    /// refused.
    pub async fn open(&self, project: &str) -> Result<SqliteStore, StoreOpenError> {
        let path = self.path(project)?;
        let store = self
            .opener
            .open_for_write(&path)
            .await
            .map_err(StoreOpenError::Open)?;
        self.private(store).await.map_err(StoreOpenError::Open)
    }

    /// `store`, once ctxloom's home directory is 0700; closed, and the
    /// failure, if its mode cannot be set.
    async fn private(&self, store: SqliteStore) -> Result<SqliteStore, OpenError> {
        let app_dir = self.home.join(APP_DIR);
        match self.fs.set_permissions(&app_dir, PRIVATE_DIR) {
            Ok(()) => Ok(store),
            Err(error) => {
                // The failure to make it private is the one said; the
                // store is closed, never dropped, either way.
                let _closed = store.close().await;
                Err(OpenError::Restrict {
                    path: app_dir.display().to_string(),
                    detail: error.to_string(),
                })
            }
        }
    }
}

/// The SQLite store of the project last asked for, held until another is,
/// behind the [`ProjectStores`] port.
///
/// [`SqliteStores::open`] opens it to write, making its directory and file
/// (migrated if need be); [`SqliteStores::open_existing`] opens it read-only
/// and never creates it, finding no store where there is no file. A store
/// held to write serves a read. A store given up for another (another
/// project's, or a read's reopened to write) is closed before the next is
/// opened, and [`SqliteStores::close`] closes the one held (ruling 131).
pub struct SqliteStores {
    stores: Stores,
    held: Option<Held>,
}

/// A store held open, and for whom.
struct Held {
    project: String,
    /// Opened to write (created and migrated if need be), not read-only.
    writable: bool,
    store: SqliteStore,
}

impl SqliteStores {
    /// The stores under `home`, on `fs`; none opened yet.
    pub fn new(fs: Arc<dyn Fs>, home: impl Into<PathBuf>) -> SqliteStores {
        SqliteStores {
            stores: Stores::new(fs, home),
            held: None,
        }
    }

    /// Close the store held, if any, and wait for it (ruling 131): when this
    /// returns its file holds every commit, with no log beside it (a write's
    /// close is the last read-write one, which checkpoints the log and
    /// removes it; a read's connection is read-only, which SQLite never lets
    /// remove an empty log). Nothing is held after; a later open opens anew.
    pub async fn close(&mut self) -> Result<(), StoreError> {
        match self.held.take() {
            Some(held) => release(held).await,
            None => Ok(()),
        }
    }
}

/// `held`, closed and waited for: a store given up (for another, or by
/// [`SqliteStores::close`]) is done with its file before anything else is.
async fn release(held: Held) -> Result<(), StoreError> {
    held.store.close().await
}

impl ProjectStores for SqliteStores {
    type Store = SqliteStore;

    /// The project's store, opened to write: its directory and file made and
    /// migrated if need be. Held after; a store held for another project, or
    /// read-only, is closed first.
    async fn open(&mut self, project: &Project) -> Result<&SqliteStore, StoreError> {
        match self.held.take() {
            Some(held) if held.project == project.id && held.writable => {
                return Ok(&self.held.insert(held).store);
            }
            Some(other) => release(other).await?,
            None => {}
        }
        let store = self
            .stores
            .open(&project.id)
            .await
            .map_err(StoreOpenError::into_store_error)?;
        Ok(&self
            .held
            .insert(Held {
                project: project.id.clone(),
                writable: true,
                store,
            })
            .store)
    }

    /// The project's store, held or opened read-only; `None` if there is no
    /// file, and nothing is made. Another project's store held is closed
    /// first.
    async fn open_existing(
        &mut self,
        project: &Project,
    ) -> Result<Option<&SqliteStore>, StoreError> {
        match self.held.take() {
            Some(held) if held.project == project.id => {
                return Ok(Some(&self.held.insert(held).store));
            }
            Some(other) => release(other).await?,
            None => {}
        }
        let Some(store) = self.stores.open_existing(&project.id).await? else {
            return Ok(None);
        };
        Ok(Some(
            &self
                .held
                .insert(Held {
                    project: project.id.clone(),
                    writable: false,
                    store,
                })
                .store,
        ))
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
