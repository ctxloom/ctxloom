//! The user daemon's files under the ctxloom home (D-N23): the election lock,
//! the discovery file (0600) and the stop request. Every one through the
//! file-system seam, so a test's temporary home is the only place touched.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_lock::{lock_within, LockError, Locked};
use file_system::{Fs, Perms};
use os::Mode;
use run_ports::{Discovery, DAEMON_DIR, DAEMON_LOCK_FILE, DISCOVERY_FILE};

/// The stop request's file: present, the daemon drains and exits.
const STOP_FILE: &str = "stop";

/// The daemon's log: what it says while nobody reads its stderr.
pub(crate) const LOG_FILE: &str = "daemon.log";

/// The daemon's directory: the user's alone.
const DIR_MODE: u32 = 0o700;

/// The discovery file: the user's alone (it names no secret, but nobody else
/// has reason to read it).
const FILE_MODE: u32 = 0o600;

/// The daemon's files of one home.
pub(crate) struct DaemonFiles {
    fs: Arc<dyn Fs>,
    dir: PathBuf,
}

impl DaemonFiles {
    pub(crate) fn new(fs: Arc<dyn Fs>, home: &Path) -> DaemonFiles {
        DaemonFiles {
            fs,
            dir: home.join(DAEMON_DIR),
        }
    }

    #[cfg(test)]
    pub(crate) fn dir(&self) -> &Path {
        &self.dir
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.join(name)
    }

    /// The election (D18, S-e): the lock whoever serves holds for its life,
    /// waited for up to `wait`; none when another holds it still.
    pub(crate) fn elect(&self, wait: Duration) -> Option<Locked> {
        lock_within(self.fs.as_ref(), &self.path(DAEMON_LOCK_FILE), wait).ok()
    }

    /// Whether a daemon holds the election now. A lock that cannot be asked
    /// (the directory cannot be made) is no daemon.
    pub(crate) fn serving(&self) -> bool {
        matches!(
            lock_within(
                self.fs.as_ref(),
                &self.path(DAEMON_LOCK_FILE),
                Duration::ZERO
            ),
            Err(LockError::Timeout { .. })
        )
    }

    /// Advertise `discovery`, 0600, in a 0700 directory.
    pub(crate) fn publish(&self, discovery: &Discovery) -> std::io::Result<()> {
        self.fs
            .create_dir_all(&self.dir, Mode::from_bits(DIR_MODE))?;
        self.fs.write_atomic(
            &self.path(DISCOVERY_FILE),
            discovery.encode().as_bytes(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// What the discovery file says, if it says it.
    pub(crate) fn discovery(&self) -> Option<Discovery> {
        let text = self.fs.read_to_string(&self.path(DISCOVERY_FILE)).ok()?;
        Discovery::decode(&text)
    }

    /// Withdraw the advertisement (a daemon that exits).
    pub(crate) fn withdraw(&self) {
        let _ = self.fs.remove_file(&self.path(DISCOVERY_FILE));
    }

    /// Ask the daemon to drain and exit.
    pub(crate) fn request_stop(&self) -> std::io::Result<()> {
        self.fs
            .create_dir_all(&self.dir, Mode::from_bits(DIR_MODE))?;
        self.fs.write_atomic(
            &self.path(STOP_FILE),
            b"stop\n",
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// Whether a stop was asked.
    pub(crate) fn stop_requested(&self) -> bool {
        self.fs.exists(&self.path(STOP_FILE)).unwrap_or(false)
    }

    /// Forget a stop request (a daemon that obeyed it, or one starting).
    pub(crate) fn clear_stop(&self) {
        let _ = self.fs.remove_file(&self.path(STOP_FILE));
    }

    /// The log file's path.
    pub(crate) fn log_path(&self) -> PathBuf {
        self.path(LOG_FILE)
    }
}

#[cfg(test)]
#[path = "files.test.rs"]
mod tests;
