//! The user daemon's files (D-N23): at the OS account's place
//! ([`instance_lock_path`]), the single-instance lock and the discovery file
//! (0600) beside it; under the ctxloom home, the stop request and the log.
//! A hosted child's launch inputs are no file: they ride the event that
//! records its launch (owner ruling 2026-11-25). Every file through the
//! file-system seam, so a test's temporary home is the only place touched.

use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_lock::{take_current, take_once, until, LockError, Locked};
use file_system::{Fs, Perms};
use os::Mode;
use run_ports::{Discovery, DAEMON_DIR, DAEMON_LOCK_FILE, DISCOVERY_FILE};

/// The daemon's log: what it says while nobody reads its stderr.
pub const LOG_FILE: &str = "daemon.log";

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
pub const INSTANCE_DIR_VARIABLE: &str = "CTXLOOM_TEST_INSTANCE_DIR";

/// Where the single-instance lock is: ONE per OS account, the discovery file
/// beside it, keyed to the account, never to `CTXLOOM_DAEMON_ROOT` or `HOME`:
/// `account` is the ACCOUNT's home (`Os::user_home`, the daemon module's
/// `account_home`) and the lock `<account>/.ctxloom/daemon/daemon.lock`, the
/// daemon directory of the account's default ctxloom home. So with no
/// `CTXLOOM_DAEMON_ROOT` the lock, the discovery file and the daemon's own files are
/// one directory; two ctxloom homes on one account share the lock, and a
/// client on the other is refused. It is no runtime directory: those are
/// variables (`XDG_RUNTIME_DIR`), absent on macOS and Windows, or shared
/// temp space. `overridden` (a test's, [`INSTANCE_DIR_VARIABLE`]) is the
/// directory instead.
pub fn instance_lock_path(account: &Path, overridden: Option<&Path>) -> PathBuf {
    overridden
        .map_or_else(|| account.join(DAEMON_DIR), Path::to_path_buf)
        .join(DAEMON_LOCK_FILE)
}

/// Why the single-instance lock was not taken.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Unheld {
    /// Another process held it for the whole wait: another daemon serves,
    /// or is starting, or is stopping.
    Held { lock: String },
    /// It could not be taken at all (its directory or its file cannot be
    /// made or locked): no daemon can start here until that changes.
    Failed(String),
}

/// The single-instance lock, held: released when dropped. It keeps a second
/// handle on the file it locked, to tell whether that is still the file at
/// the lock's path ([`DaemonFiles::still_holds`]).
pub struct Instance {
    _locked: Locked,
    held: File,
}

/// The daemon's files of one home.
#[derive(Clone)]
pub struct DaemonFiles {
    fs: Arc<dyn Fs>,
    /// ctxloom's home, whose run locks a launch's lock is among.
    home: PathBuf,
    dir: PathBuf,
    lock: PathBuf,
}

impl DaemonFiles {
    /// The files of `home`, the single-instance lock where
    /// [`instance_lock_path`] puts it with no override.
    pub fn new(fs: Arc<dyn Fs>, home: &Path) -> DaemonFiles {
        DaemonFiles::with_instance(fs, home, instance_lock_path(home, None))
    }

    /// The files of `home`, the single-instance lock at `lock` and the
    /// discovery file beside it.
    pub fn with_instance(fs: Arc<dyn Fs>, home: &Path, lock: PathBuf) -> DaemonFiles {
        DaemonFiles {
            fs,
            home: home.to_path_buf(),
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
    pub fn dir(&self) -> &Path {
        &self.dir
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.join(name)
    }

    /// [`Self::take_instance`], none when it was not taken.
    #[cfg(any(test, feature = "test-support"))]
    pub fn hold_instance(&self, wait: Duration) -> Option<Instance> {
        self.take_instance(wait).ok()
    }

    /// The single-instance lock (D18, S-e): one daemon per OS account, the
    /// lock whoever serves holds for its life, waited for up to `wait`, or
    /// why it was not taken: another held it for the whole wait
    /// ([`Unheld::Held`]), or it could not be taken at all
    /// ([`Unheld::Failed`]). It says only that one daemon serves (M11):
    /// whether a daemon serves is its health check (`daemon::live`), never
    /// this lock.
    ///
    /// The lock file is made once and never removed or replaced by ctxloom.
    pub fn take_instance(&self, wait: Duration) -> Result<Instance, Unheld> {
        let fs = self.fs.as_ref();
        let path = self.lock.as_path();
        fs.create_dir_all(self.instance_dir(), Mode::from_bits(DIR_MODE))
            .map_err(|e| Unheld::Failed(e.to_string()))?;
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
        .map_err(|e| match e {
            LockError::Timeout { .. } => Unheld::Held {
                lock: path.display().to_string(),
            },
            other => Unheld::Failed(other.to_string()),
        })?;
        let held = held.ok_or_else(|| Unheld::Failed("the lock was not kept".to_string()))?;
        Ok(Instance {
            _locked: locked,
            held,
        })
    }

    /// Whether `instance` is still the lock at the lock's path: the file it
    /// locked is the file there now (the platform's identity: device and
    /// inode, volume and file index). A lock file removed or replaced under
    /// a serving daemon excludes nobody, so the daemon exits once it is not.
    /// What cannot be told is held.
    pub fn still_holds(&self, instance: &Instance) -> bool {
        match self.fs.same_file(&instance.held, &self.lock) {
            Ok(same) => same,
            Err(e) => e.kind() != std::io::ErrorKind::NotFound,
        }
    }

    /// Whether the daemon's ctxloom directory (`<home>/.ctxloom`, its
    /// store's) is gone: its home deleted (j000100's exit, a test's
    /// temporary home), which the single-instance lock does not always say,
    /// as it may be elsewhere (the account's, while `CTXLOOM_DAEMON_ROOT` names
    /// another home; a test's own place). What cannot be told is there.
    pub fn home_gone(&self) -> bool {
        matches!(self.fs.exists(self.ctxloom_dir()), Ok(false))
    }

    /// The daemon's ctxloom directory as a person reads it, once it is gone
    /// ([`home_gone`](Self::home_gone)); none while it is there.
    pub fn gone_home(&self) -> Option<String> {
        self.home_gone()
            .then(|| self.ctxloom_dir().display().to_string())
    }

    /// The daemon's ctxloom directory, `<home>/.ctxloom` (its store's).
    fn ctxloom_dir(&self) -> &Path {
        self.dir.parent().unwrap_or(&self.dir)
    }

    /// The daemon's directory, made if missing and held to 0700 whoever made
    /// it (the single-instance lock may have, with the default mode; an older
    /// install's may be wider). Windows has no modes (M8).
    fn private_dir(&self, dir: &Path) -> std::io::Result<()> {
        self.fs.create_dir_all(dir, Mode::from_bits(DIR_MODE))?;
        self.fs.set_permissions(dir, Mode::from_bits(DIR_MODE))
    }

    /// Advertise `discovery`, 0600, in a 0700 directory.
    pub fn publish(&self, discovery: &Discovery) -> std::io::Result<()> {
        self.private_dir(self.instance_dir())?;
        #[cfg(feature = "test-support")]
        self.die_while_publishing(discovery);
        self.fs.write_atomic(
            &self.discovery_path(),
            &discovery.encode(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// Under test-support, killed outright while the discovery file is
    /// written, when the switch says so (U6): half its bytes are in the
    /// atomic write's own temporary beside it, and the file it replaces is
    /// left as it was (an atomic write, T25).
    #[cfg(feature = "test-support")]
    fn die_while_publishing(&self, discovery: &Discovery) {
        if !self.take_die_at("discovery") {
            return;
        }
        let bytes = discovery.encode();
        let partial = self.discovery_path().with_extension("partial");
        let _ = self.fs.write_atomic(
            &partial,
            &bytes[..bytes.len() / 2],
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        );
        std::process::abort();
    }

    /// What the discovery file says, if it says it.
    pub fn discovery(&self) -> Option<Discovery> {
        let bytes = self.fs.read(&self.discovery_path()).ok()?;
        Discovery::decode(&bytes)
    }

    /// The daemon that holds the single-instance lock, advertises itself
    /// and does not answer its health check (`answers`): the discovery file
    /// names it, the lock is held by another, and `answers` says no. None
    /// for no file, a lock that is free (a file a killed daemon left), or a
    /// daemon that answers. The lock is taken and let go at once, to see
    /// whether it can be.
    pub fn silent_holder(&self, answers: impl Fn(&Discovery) -> bool) -> Option<Discovery> {
        let found = self.discovery()?;
        if answers(&found) {
            return None;
        }
        match self.take_instance(Duration::ZERO) {
            Err(Unheld::Held { .. }) => Some(found),
            _ => None,
        }
    }

    /// Withdraw the advertisement, whoever's it is.
    fn withdraw(&self) {
        let _ = self.fs.remove_file(&self.discovery_path());
    }

    /// Withdraw the advertisement only while it is still `own`: a daemon
    /// whose file another daemon's replaced leaves that one's alone.
    pub fn withdraw_own(&self, own: &Discovery) {
        if self.discovery().as_ref() == Some(own) {
            self.withdraw();
        }
    }

    /// The run locks a launch's lock is among (`daemon_api_client::launch_lock_name`).
    pub fn launch_locks(&self) -> liveness::RunLockFiles {
        liveness::RunLockFiles::new(Arc::clone(&self.fs), self.home.clone())
    }

    /// The log file's path.
    pub fn log_path(&self) -> PathBuf {
        self.path(LOG_FILE)
    }

    /// The test-support switch that kills the daemon once a client's write
    /// is applied, before it is answered (slice 3 step 4), taken by the
    /// daemon that finds it: whether it was there.
    #[cfg(feature = "test-support")]
    pub fn take_die_after_apply(&self) -> bool {
        self.fs.remove_file(&self.path(DIE_AFTER_APPLY)).is_ok()
    }

    /// The test-support switch that kills the daemon outright at one step
    /// (slice 4 U6, `test-die-at`, its one line the step's word: `enqueued`,
    /// `minted`, `started`, `turn`, `hook`, `discovery`): whether it names
    /// `step`, taken (removed) when it does, so the daemon started after
    /// finds none.
    #[cfg(feature = "test-support")]
    pub fn take_die_at(&self, step: &str) -> bool {
        let named = self
            .fs
            .read(&self.path(DIE_AT))
            .is_ok_and(|bytes| String::from_utf8_lossy(&bytes).trim() == step);
        named && self.fs.remove_file(&self.path(DIE_AT)).is_ok()
    }

    /// The test-support switch that holds a library write at one point
    /// (`test-hold-at`, its one line the point's word, as `test-die-at`'s):
    /// while the file names `point`, the write waits there, so a test
    /// standing for another process acts between two of its steps and then
    /// removes the file (the race rule: the interleaving is forced, never
    /// waited for). Bounded by `HOLD_MOST`, so a test that never lets go
    /// cannot hang the daemon.
    #[cfg(feature = "test-support")]
    pub fn hold_at(&self, point: &str) {
        let named = || {
            self.fs
                .read(&self.path(HOLD_AT))
                .is_ok_and(|bytes| String::from_utf8_lossy(&bytes).trim() == point)
        };
        let deadline = std::time::Instant::now() + HOLD_MOST;
        while named() && std::time::Instant::now() < deadline {
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
    }

    /// The instant the test-support switch that holds the scheduler's
    /// alarms (`test-hold-alarms`) holds them at, while it is there: its one
    /// line, seconds since the epoch, written by the test before what it
    /// holds was armed. While it is, the scheduler's clock stands there, so
    /// no alarm armed since comes due; once it is gone the clock is the
    /// system's again (a test that stands for the passing of an alarm's
    /// time lets go of it, so the alarm fires when the test says, never
    /// when a loaded machine is slow: the race rule). A line that is no
    /// number holds them at none (the system's clock).
    #[cfg(feature = "test-support")]
    pub fn alarms_held_at(&self) -> Option<i64> {
        let bytes = self.fs.read(&self.path(HOLD_ALARMS)).ok()?;
        String::from_utf8_lossy(&bytes).trim().parse().ok()
    }

    /// The test-support switch that makes the daemon deaf to a stop and a
    /// drain (`test-deaf-to-stop`): while it is there, each is taken and
    /// ignored, so `ctxloom shutdown` finds a daemon that will not exit.
    #[cfg(feature = "test-support")]
    pub fn deaf_to_stop(&self) -> bool {
        self.fs.exists(&self.path(DEAF_TO_STOP)).unwrap_or(false)
    }

    /// The test-support wait `ctxloom shutdown` gives a daemon it asked the
    /// plain stop (`test-shutdown-stop-wait`, its one line whole seconds);
    /// none, or one that does not read, is none.
    #[cfg(feature = "test-support")]
    pub fn shutdown_stop_wait(&self) -> Option<std::time::Duration> {
        let bytes = self.fs.read(&self.path(SHUTDOWN_STOP_WAIT)).ok()?;
        let seconds = String::from_utf8_lossy(&bytes).trim().parse().ok()?;
        Some(std::time::Duration::from_secs(seconds))
    }

    /// The test-support switch that fails the daemon's start-up catch-up
    /// (OD-52, `test-start-up-fails`): while it is there, every start-up
    /// attempt (the start's, and each round's again) fails, its why the
    /// file's one line. Never taken: a test removes it to see a later
    /// catch-up succeed.
    #[cfg(feature = "test-support")]
    pub fn start_up_fails(&self) -> Option<String> {
        let bytes = self.fs.read(&self.path(START_UP_FAILS)).ok()?;
        Some(String::from_utf8_lossy(&bytes).trim().to_string())
    }
}

/// The kill switch's file, in the daemon's directory.
#[cfg(feature = "test-support")]
const DIE_AFTER_APPLY: &str = "test-die-after-apply";

/// The test-support switch that kills a daemon at a step (slice 4 U6).
#[cfg(feature = "test-support")]
const DIE_AT: &str = "test-die-at";

/// The test-support switch that holds a write at a point (`test-hold-at`).
#[cfg(feature = "test-support")]
const HOLD_AT: &str = "test-hold-at";

/// The test-support switch that holds the scheduler's alarms.
#[cfg(feature = "test-support")]
const HOLD_ALARMS: &str = "test-hold-alarms";

/// The longest a write is held at a point.
#[cfg(feature = "test-support")]
const HOLD_MOST: std::time::Duration = std::time::Duration::from_secs(30);

/// The test-support switch that fails a daemon's start-up catch-up (OD-52).
#[cfg(feature = "test-support")]
const START_UP_FAILS: &str = "test-start-up-fails";

/// The test-support switch that makes the daemon ignore a stop and a drain.
#[cfg(feature = "test-support")]
const DEAF_TO_STOP: &str = "test-deaf-to-stop";

/// The test-support wait of `ctxloom shutdown` for a daemon it stopped.
#[cfg(feature = "test-support")]
const SHUTDOWN_STOP_WAIT: &str = "test-shutdown-stop-wait";

#[cfg(test)]
#[path = "files.test.rs"]
mod tests;
