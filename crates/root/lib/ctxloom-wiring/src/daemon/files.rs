//! The user daemon's files under the ctxloom home (D-N23): the election lock,
//! the discovery file (0600), the stop request, and a launching client's
//! hand-off of a child's environment (0600, D-N45: taken by the daemon at the
//! child's start). Every one through the
//! file-system seam, so a test's temporary home is the only place touched.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_lock::{lock_within, Locked};
use file_system::{Fs, Perms};
use os::Mode;
use run_ports::{Discovery, DAEMON_DIR, DAEMON_LOCK_FILE, DISCOVERY_FILE};

/// The stop request's file: present, the daemon drains and exits.
const STOP_FILE: &str = "stop";

/// The daemon's log: what it says while nobody reads its stderr.
pub(crate) const LOG_FILE: &str = "daemon.log";

/// The directory of the launching clients' hand-offs, one file per child.
const LAUNCH_DIR: &str = "launch";

/// The first line of a hand-off.
const HAND_OFF_HEADER: &str = "ctxloom-launch-env 1";

/// The daemon's directory: the user's alone.
const DIR_MODE: u32 = 0o700;

/// The discovery file: the user's alone (it holds the user bearer; other
/// users are what the mode keeps out). On Windows the platform sets no mode:
/// the profile directory's inherited ACL stands (M8).
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
    /// waited for up to `wait`; none when another holds it still. It is the
    /// election ALONE (M11): whether a daemon serves is its health check
    /// (`daemon::live`), never this lock.
    pub(crate) fn elect(&self, wait: Duration) -> Option<Locked> {
        lock_within(self.fs.as_ref(), &self.path(DAEMON_LOCK_FILE), wait).ok()
    }

    /// The daemon's directory, made if missing and held to 0700 whoever made
    /// it (the election's lock may have, with the default mode; an older
    /// install's may be wider). Windows has no modes (M8).
    fn private_dir(&self) -> std::io::Result<()> {
        self.fs
            .create_dir_all(&self.dir, Mode::from_bits(DIR_MODE))?;
        self.fs
            .set_permissions(&self.dir, Mode::from_bits(DIR_MODE))
    }

    /// Advertise `discovery`, 0600, in a 0700 directory.
    pub(crate) fn publish(&self, discovery: &Discovery) -> std::io::Result<()> {
        self.private_dir()?;
        self.fs.write_atomic(
            &self.path(DISCOVERY_FILE),
            &discovery.encode(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// What the discovery file says, if it says it.
    pub(crate) fn discovery(&self) -> Option<Discovery> {
        let bytes = self.fs.read(&self.path(DISCOVERY_FILE)).ok()?;
        Discovery::decode(&bytes)
    }

    /// Withdraw the advertisement, whoever's it is.
    fn withdraw(&self) {
        let _ = self.fs.remove_file(&self.path(DISCOVERY_FILE));
    }

    /// Withdraw the advertisement only while it is still `own`: a daemon
    /// whose file another daemon's replaced leaves that one's alone.
    pub(crate) fn withdraw_own(&self, own: &Discovery) {
        if self.discovery().as_ref() == Some(own) {
            self.withdraw();
        }
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

    /// Where the hand-off of the child `session` is; none for a name that is
    /// not a UUID (it never names another path).
    fn hand_off_path(&self, session: &str) -> Option<PathBuf> {
        let id = uuid::Uuid::parse_str(session).ok()?;
        Some(self.dir.join(LAUNCH_DIR).join(id.hyphenated().to_string()))
    }

    /// Hand the daemon `env`, the environment the child `session` is to be
    /// started with (S6): 0600, in the 0700 daemon directory, written whole.
    pub(crate) fn hand_off(
        &self,
        session: &str,
        env: &BTreeMap<String, String>,
    ) -> std::io::Result<()> {
        let path = self
            .hand_off_path(session)
            .ok_or_else(|| std::io::Error::other("a child's id is a UUID"))?;
        let dir = self.dir.join(LAUNCH_DIR);
        self.fs.create_dir_all(&dir, Mode::from_bits(DIR_MODE))?;
        self.fs.write_atomic(
            &path,
            encode_env(env).as_bytes(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// Take the hand-off of `session`: read, then removed at once (nothing
    /// is left at rest once the daemon holds it). None when there is none,
    /// or it does not read.
    pub(crate) fn take_hand_off(&self, session: &str) -> Option<BTreeMap<String, String>> {
        let path = self.hand_off_path(session)?;
        let text = self.fs.read_to_string(&path).ok();
        let _ = self.fs.remove_file(&path);
        decode_env(&text?)
    }

    /// Withdraw the hand-off of `session` (a launch that was refused).
    pub(crate) fn withdraw_hand_off(&self, session: &str) {
        if let Some(path) = self.hand_off_path(session) {
            let _ = self.fs.remove_file(&path);
        }
    }

    /// The log file's path.
    pub(crate) fn log_path(&self) -> PathBuf {
        self.path(LOG_FILE)
    }
}

/// A hand-off's text: its header, then a `NAME=value` line per variable, the
/// value's backslashes, newlines and carriage returns escaped.
pub(crate) fn encode_env(env: &BTreeMap<String, String>) -> String {
    let mut text = format!("{HAND_OFF_HEADER}\n");
    for (name, value) in env {
        let escaped = value
            .replace('\\', "\\\\")
            .replace('\n', "\\n")
            .replace('\r', "\\r");
        text.push_str(&format!("{name}={escaped}\n"));
    }
    text
}

/// What [`encode_env`] wrote; none for anything else.
pub(crate) fn decode_env(text: &str) -> Option<BTreeMap<String, String>> {
    let mut lines = text.lines();
    if lines.next()? != HAND_OFF_HEADER {
        return None;
    }
    let mut env = BTreeMap::new();
    for line in lines {
        let (name, escaped) = line.split_once('=')?;
        let mut value = String::new();
        let mut chars = escaped.chars();
        while let Some(c) = chars.next() {
            match (c, c == '\\') {
                (_, true) => match chars.next()? {
                    'n' => value.push('\n'),
                    'r' => value.push('\r'),
                    '\\' => value.push('\\'),
                    _ => return None,
                },
                (c, false) => value.push(c),
            }
        }
        env.insert(name.to_string(), value);
    }
    Some(env)
}

#[cfg(test)]
#[path = "files.test.rs"]
mod tests;
