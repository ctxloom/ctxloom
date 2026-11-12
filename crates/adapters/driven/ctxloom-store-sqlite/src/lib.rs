//! The user's ctxloom store (T14): angzarr-store-sqlite's [`SqliteStore`] at
//! `<home>/.ctxloom/ctxloom.db`, ONE file for every project's streams (each
//! stream is rooted in its project's namespace, so nothing routes by file),
//! beside taskloom's per-project `state.db` and never the same file. All
//! file I/O goes through the `file-system` seam, so a test under a temporary
//! home touches nothing else.
//!
//! - [`Stores::open`] opens the store to write, making the app dir and the
//!   file (migrated if need be).
//! - The opening is `sqlite-store-opener`'s: this crate is the file name, the
//!   placement under the home and the [`ProjectStores`] port. A store from
//!   before the embedded store is refused ([`OpenError::PredatesEmbedded`]).
//! - A project that still has a store of its OWN, from before the one store
//!   (`<home>/.ctxloom/projects/<id>/ctxloom.db`), is refused
//!   ([`StoreOpenError::PerProjectStore`], owner T17 and S48): nothing is
//!   imported from it, and its fix names the file to remove.
//! - A store is closed, never dropped, where its file matters after (ruling
//!   131): [`SqliteStore::close`] waits for the connection's close, so when
//!   it returns the file holds every commit with no log beside it. A store
//!   dropped instead is closed on the driver's thread whenever that runs,
//!   which a process that exits first never lets it do. [`SqliteStores`]
//!   closes a read's store it reopens to write, and [`SqliteStores::close`]
//!   the one it holds; whoever [`Stores::open`]s one closes it.

mod error;

use std::path::PathBuf;
use std::sync::Arc;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::StoreError;
use file_system::Fs;
use ports::Project;
use session_ports::ProjectStores;
use sqlite_store_opener::Opener;

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{StoreOpenError, EN_US};
pub use sqlite_store_opener::OpenError;
pub use sqlite_store_opener::EN_US as OPENER_EN_US;

/// The app's directory under a home.
const APP_DIR: &str = ".ctxloom";

/// The store's file name: beside taskloom's `state.db`, never the same file.
const FILE_NAME: &str = "ctxloom.db";

/// The ctxloom store under one home.
pub struct Stores {
    fs: Arc<dyn Fs>,
    opener: Opener,
    home: PathBuf,
}

impl Stores {
    /// The store under `home`, on `fs`; not opened yet.
    pub fn new(fs: Arc<dyn Fs>, home: impl Into<PathBuf>) -> Stores {
        Stores {
            opener: Opener::new(Arc::clone(&fs), FILE_NAME),
            fs,
            home: home.into(),
        }
    }

    /// Where the one database is: `<home>/.ctxloom/ctxloom.db`.
    pub fn path(&self) -> PathBuf {
        self.opener.file_in(&self.home.join(APP_DIR))
    }

    /// Where `project`'s own store was, before the one store:
    /// `<home>/.ctxloom/projects/<project>/ctxloom.db`. Refused where
    /// `project` is not one plain name (the shared `project-dir` crate's
    /// check), so an id names no path outside the projects directory.
    pub fn per_project(&self, project: &str) -> Result<PathBuf, StoreOpenError> {
        project_dir::project_dir(&self.home, APP_DIR, project)
            .map(|dir| self.opener.file_in(&dir))
            .map_err(StoreOpenError::UnsafeProject)
    }

    /// Refused ([`StoreOpenError::PerProjectStore`]) where `project` still
    /// has a store of its own from before the one store (T17): nothing is
    /// read from it or imported, and it is left as it is. An unsafe id is
    /// refused as [`Stores::per_project`] refuses it.
    pub fn refuse_per_project(&self, project: &str) -> Result<(), StoreOpenError> {
        let path = self.per_project(project)?;
        match self.fs.exists(&path) {
            Ok(true) => Err(StoreOpenError::PerProjectStore {
                path: path.display().to_string(),
            }),
            // Unreadable is no proof of a store: the open says what is wrong.
            Ok(false) | Err(_) => Ok(()),
        }
    }

    /// The store, read-only: `None` where there is no database, and nothing
    /// is made. A store from before the embedded store is refused.
    pub async fn open_existing(&self) -> Result<Option<SqliteStore>, StoreError> {
        self.opener
            .open_existing(&self.path())
            .await
            .map_err(OpenError::into_store_error)
    }

    /// `project`'s directory under the home's projects, made if need be: the
    /// directory a project is listed by (taskloom's registry finds every
    /// project there, and keeps its `state.db` in it). ctxloom keeps nothing
    /// in it; a write for a project makes it, as the project's own store
    /// once did.
    pub fn list_project(&self, project: &str) -> Result<(), StoreOpenError> {
        let dir = project_dir::project_dir(&self.home, APP_DIR, project)
            .map_err(StoreOpenError::UnsafeProject)?;
        self.fs
            .create_dir_all(&dir, os::Mode::from_bits(0o755))
            .map_err(|error| StoreOpenError::ProjectDirUnmade {
                path: dir.display().to_string(),
                detail: error.to_string(),
            })
    }

    /// The store, opened to write for `project`: refused where the project
    /// has a store of its own ([`Stores::refuse_per_project`]), the project
    /// listed ([`Stores::list_project`]), then [`Stores::open`].
    pub async fn open_for(&self, project: &str) -> Result<SqliteStore, StoreOpenError> {
        self.refuse_per_project(project)?;
        self.list_project(project)?;
        self.open().await
    }

    /// The store, opened to write: the app dir and file made and migrated if
    /// need be. A store from before the embedded store is refused.
    pub async fn open(&self) -> Result<SqliteStore, StoreOpenError> {
        self.opener
            .open_for_write(&self.path())
            .await
            .map_err(StoreOpenError::Open)
    }
}

/// The one SQLite store, held once opened, behind the [`ProjectStores`]
/// port: every project's streams are in it.
///
/// [`SqliteStores::open`] opens it to write, making the app dir and file
/// (migrated if need be); [`SqliteStores::open_existing`] opens it read-only
/// and never creates it, finding no store where there is no file. A store
/// held to write serves a read. A store held read-only is closed before it is
/// reopened to write, and [`SqliteStores::close`] closes the one held (ruling
/// 131). Either refuses a project that has a store of its own (T17).
pub struct SqliteStores {
    stores: Stores,
    held: Option<Held>,
}

/// The store held open.
struct Held {
    /// Opened to write (created and migrated if need be), not read-only.
    writable: bool,
    store: SqliteStore,
}

impl SqliteStores {
    /// The store under `home`, on `fs`; not opened yet.
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
            Some(held) => held.store.close().await,
            None => Ok(()),
        }
    }

    /// `project` refused if it has a store of its own (T17), as the port's
    /// error.
    fn admit(&self, project: &Project) -> Result<(), StoreError> {
        self.stores
            .refuse_per_project(&project.id)
            .map_err(StoreOpenError::into_store_error)
    }
}

impl ProjectStores for SqliteStores {
    type Store = SqliteStore;

    /// The one store, opened to write: the app dir and file made and
    /// migrated if need be. Held after; one held read-only is closed first.
    async fn open(&mut self, project: &Project) -> Result<&SqliteStore, StoreError> {
        self.admit(project)?;
        self.stores
            .list_project(&project.id)
            .map_err(StoreOpenError::into_store_error)?;
        match self.held.take() {
            Some(held) if held.writable => return Ok(&self.held.insert(held).store),
            Some(read) => read.store.close().await?,
            None => {}
        }
        let store = self
            .stores
            .open()
            .await
            .map_err(StoreOpenError::into_store_error)?;
        Ok(&self
            .held
            .insert(Held {
                writable: true,
                store,
            })
            .store)
    }

    /// The one store, held or opened read-only; `None` if there is no file,
    /// and nothing is made.
    async fn open_existing(
        &mut self,
        project: &Project,
    ) -> Result<Option<&SqliteStore>, StoreError> {
        self.admit(project)?;
        if let Some(held) = self.held.take() {
            return Ok(Some(&self.held.insert(held).store));
        }
        let Some(store) = self.stores.open_existing().await? else {
            return Ok(None);
        };
        Ok(Some(
            &self
                .held
                .insert(Held {
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
