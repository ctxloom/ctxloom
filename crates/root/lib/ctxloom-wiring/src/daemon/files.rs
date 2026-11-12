//! The user daemon's files (D-N23): at the OS account's place
//! ([`instance_lock_path`]), the single-instance lock and the discovery file
//! (0600) beside it; under the ctxloom home, the stop request, the log and a
//! launching client's hand-off of a child's environment (0600, D-N45: taken
//! by the daemon at the child's start). Every one through the file-system
//! seam, so a test's temporary home is the only place touched.

use std::collections::BTreeMap;
use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_lock::{take_current, take_once, until, LockError, Locked};
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

/// The test-support variable naming the directory the single-instance lock
/// and the discovery file are in, so each test's temporary home keeps its
/// own daemon (and two homes can be made to share one). Read only under
/// `test-support`.
pub(crate) const INSTANCE_DIR_VARIABLE: &str = "CTXLOOM_TEST_INSTANCE_DIR";

/// Where the single-instance lock is: ONE per OS account, the discovery file
/// beside it. It is keyed to the account, never to `CTXLOOM_HOME`; but the
/// platform does not yet resolve the account's home (angzarr-platform's
/// `os::user_home`, being added) and offers no per-user runtime place, so
/// until it does the place is under the ctxloom `home`
/// (`<home>/.ctxloom/daemon/daemon.lock`), and switching to the account's is
/// this function's one line. `overridden` (a test's, [`INSTANCE_DIR_VARIABLE`])
/// is the directory instead.
pub(crate) fn instance_lock_path(home: &Path, overridden: Option<&Path>) -> PathBuf {
    overridden
        .map_or_else(|| home.join(DAEMON_DIR), Path::to_path_buf)
        .join(DAEMON_LOCK_FILE)
}

/// The single-instance lock, held: released when dropped. It keeps a second
/// handle on the file it locked, to tell whether that is still the file at
/// the lock's path ([`DaemonFiles::still_holds`]).
pub(crate) struct Instance {
    _locked: Locked,
    held: File,
}

/// The daemon's files of one home.
pub(crate) struct DaemonFiles {
    fs: Arc<dyn Fs>,
    dir: PathBuf,
    lock: PathBuf,
}

impl DaemonFiles {
    /// The files of `home`, the single-instance lock where
    /// [`instance_lock_path`] puts it with no override.
    pub(crate) fn new(fs: Arc<dyn Fs>, home: &Path) -> DaemonFiles {
        DaemonFiles::with_instance(fs, home, instance_lock_path(home, None))
    }

    /// The files of `home`, the single-instance lock at `lock` and the
    /// discovery file beside it.
    pub(crate) fn with_instance(fs: Arc<dyn Fs>, home: &Path, lock: PathBuf) -> DaemonFiles {
        DaemonFiles {
            fs,
            dir: home.join(DAEMON_DIR),
            lock,
        }
    }

    /// The directory of the single-instance lock and the discovery file.
    fn instance_dir(&self) -> &Path {
        self.lock.parent().unwrap_or(&self.dir)
    }

    fn discovery_path(&self) -> PathBuf {
        self.instance_dir().join(DISCOVERY_FILE)
    }

    #[cfg(test)]
    pub(crate) fn dir(&self) -> &Path {
        &self.dir
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.join(name)
    }

    /// The single-instance lock (D18, S-e): one daemon per OS account, the
    /// lock whoever serves holds for its life,
    /// waited for up to `wait`; none when another holds it still. It says
    /// only that one daemon serves (M11): whether a daemon serves is its health check
    /// (`daemon::live`), never this lock.
    ///
    /// The lock file is made once and never removed or replaced by ctxloom.
    pub(crate) fn hold_instance(&self, wait: Duration) -> Option<Instance> {
        let fs = self.fs.as_ref();
        let path = self.lock.as_path();
        fs.create_dir_all(self.instance_dir(), Mode::from_bits(DIR_MODE))
            .ok()?;
        let mut pause = until(path, wait);
        let mut held = None;
        let locked = take_current(fs, path, || {
            let file = take_once(fs, path, &mut pause)?;
            held = Some(file.try_clone().map_err(|e| LockError::Io {
                path: path.display().to_string(),
                detail: e.to_string(),
            })?);
            Ok(file)
        })
        .ok()?;
        Some(Instance {
            _locked: locked,
            held: held?,
        })
    }

    /// Whether `instance` is still the lock at the lock's path: the file it
    /// locked is the file there now (the platform's identity: device and
    /// inode, volume and file index). A lock file removed or replaced under
    /// a serving daemon excludes nobody, so the daemon exits once it is not.
    /// What cannot be told is held.
    pub(crate) fn still_holds(&self, instance: &Instance) -> bool {
        match self.fs.same_file(&instance.held, &self.lock) {
            Ok(same) => same,
            Err(e) => e.kind() != std::io::ErrorKind::NotFound,
        }
    }

    /// The daemon's directory, made if missing and held to 0700 whoever made
    /// it (the single-instance lock may have, with the default mode; an older
    /// install's may be wider). Windows has no modes (M8).
    fn private_dir(&self, dir: &Path) -> std::io::Result<()> {
        self.fs.create_dir_all(dir, Mode::from_bits(DIR_MODE))?;
        self.fs.set_permissions(dir, Mode::from_bits(DIR_MODE))
    }

    /// Advertise `discovery`, 0600, in a 0700 directory.
    pub(crate) fn publish(&self, discovery: &Discovery) -> std::io::Result<()> {
        self.private_dir(self.instance_dir())?;
        self.fs.write_atomic(
            &self.discovery_path(),
            &discovery.encode(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// What the discovery file says, if it says it.
    pub(crate) fn discovery(&self) -> Option<Discovery> {
        let bytes = self.fs.read(&self.discovery_path()).ok()?;
        Discovery::decode(&bytes)
    }

    /// Withdraw the advertisement, whoever's it is.
    fn withdraw(&self) {
        let _ = self.fs.remove_file(&self.discovery_path());
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
