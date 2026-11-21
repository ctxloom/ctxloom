//! The user daemon's files (D-N23): at the OS account's place
//! ([`instance_lock_path`]), the single-instance lock and the discovery file
//! (0600) beside it; under the ctxloom home, the stop request, the log and
//! each child's hand-off of its environment (0600; T6, S10: written by the
//! daemon's `CreateHandOff`, read at each of the child's starts, deleted at
//! its close, swept when no open session holds it). Every one through the
//! file-system seam, so a test's temporary home is the only place touched.

use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use daemon_application::HandOff;
use file_lock::{take_current, take_once, until, LockError, Locked};
use file_system::{Fs, Perms};
use os::Mode;
use run_ports::{Discovery, DAEMON_DIR, DAEMON_LOCK_FILE, DISCOVERY_FILE};

/// The daemon's log: what it says while nobody reads its stderr.
pub(crate) const LOG_FILE: &str = "daemon.log";

/// The directory of the launching clients' hand-offs, one file per child.
const LAUNCH_DIR: &str = "launch";

/// The first line of a hand-off.
const HAND_OFF_HEADER: &str = "ctxloom-launch-env 3";

/// The first line of a hand-off an older daemon wrote (no agent line): read
/// as one for the project's default agent.
const HAND_OFF_HEADER_2: &str = "ctxloom-launch-env 2";

/// What marks a line of a hand-off as its launcher's place (no variable
/// name starts with it).
const LAUNCHER_MARK: char = '@';

/// What marks a hand-off's line naming the agent its child is planned for.
const AGENT_LINE: &str = "!agent";

/// The extension of a hand-off's mark in launch (`<id>.launching`).
const LAUNCHING: &str = "launching";

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
/// beside it, keyed to the account, never to `CTXLOOM_HOME` or `HOME`:
/// `account` is the ACCOUNT's home (`Os::user_home`, the daemon module's
/// `account_home`) and the lock `<account>/.ctxloom/daemon/daemon.lock`, the
/// daemon directory of the account's default ctxloom home. So with no
/// `CTXLOOM_HOME` the lock, the discovery file and the daemon's own files are
/// one directory; two ctxloom homes on one account share the lock, and a
/// client on the other is refused. It is no runtime directory: those are
/// variables (`XDG_RUNTIME_DIR`), absent on macOS and Windows, or shared
/// temp space. `overridden` (a test's, [`INSTANCE_DIR_VARIABLE`]) is the
/// directory instead.
pub(crate) fn instance_lock_path(account: &Path, overridden: Option<&Path>) -> PathBuf {
    overridden
        .map_or_else(|| account.join(DAEMON_DIR), Path::to_path_buf)
        .join(DAEMON_LOCK_FILE)
}

/// Why the single-instance lock was not taken.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Unheld {
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
pub(crate) struct Instance {
    _locked: Locked,
    held: File,
}

/// The daemon's files of one home.
#[derive(Clone)]
pub(crate) struct DaemonFiles {
    fs: Arc<dyn Fs>,
    /// ctxloom's home, whose run locks a launch's lock is among.
    home: PathBuf,
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
    pub(crate) fn dir(&self) -> &Path {
        &self.dir
    }

    fn path(&self, name: &str) -> PathBuf {
        self.dir.join(name)
    }

    /// [`Self::take_instance`], none when it was not taken.
    #[cfg(test)]
    pub(crate) fn hold_instance(&self, wait: Duration) -> Option<Instance> {
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
    pub(crate) fn take_instance(&self, wait: Duration) -> Result<Instance, Unheld> {
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
    pub(crate) fn still_holds(&self, instance: &Instance) -> bool {
        match self.fs.same_file(&instance.held, &self.lock) {
            Ok(same) => same,
            Err(e) => e.kind() != std::io::ErrorKind::NotFound,
        }
    }

    /// Whether the daemon's ctxloom directory (`<home>/.ctxloom`, its
    /// store's) is gone: its home deleted (j000100's exit, a test's
    /// temporary home), which the single-instance lock does not always say,
    /// as it may be elsewhere (the account's, while `CTXLOOM_HOME` names
    /// another home; a test's own place). What cannot be told is there.
    pub(crate) fn home_gone(&self) -> bool {
        matches!(self.fs.exists(self.ctxloom_dir()), Ok(false))
    }

    /// The daemon's ctxloom directory as a person reads it, once it is gone
    /// ([`home_gone`](Self::home_gone)); none while it is there.
    pub(crate) fn gone_home(&self) -> Option<String> {
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
    pub(crate) fn publish(&self, discovery: &Discovery) -> std::io::Result<()> {
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

    /// Where the hand-off of the child `session` is; none for a name that is
    /// not a UUID (it never names another path).
    fn hand_off_path(&self, session: &str) -> Option<PathBuf> {
        let id = uuid::Uuid::parse_str(session).ok()?;
        Some(self.dir.join(LAUNCH_DIR).join(id.hyphenated().to_string()))
    }

    /// Keep `handed`, the environment the child `session` is started with
    /// and where its launcher finds its user's settings (S6, S7, T6): 0600,
    /// in the 0700 daemon directory, written whole.
    pub(crate) fn hand_off(&self, session: &str, handed: &HandOff) -> std::io::Result<()> {
        let path = self
            .hand_off_path(session)
            .ok_or_else(|| std::io::Error::other("a child's id is a UUID"))?;
        let dir = self.dir.join(LAUNCH_DIR);
        self.private_dir(&self.dir)?;
        self.private_dir(&dir)?;
        self.fs.write_atomic(
            &path,
            encode_hand_off(handed).as_bytes(),
            Perms::Exact(Mode::from_bits(FILE_MODE)),
        )
    }

    /// Where the mark of `session`'s hand-off is: beside it, `<id>.launching`.
    fn mark_path(&self, session: &str) -> Option<PathBuf> {
        let path = self.hand_off_path(session)?;
        Some(path.with_extension(LAUNCHING))
    }

    /// Hand the daemon `handed` for the child `session`'s launch (T6, the
    /// owner's ruling on U3): its mark first, then the hand-off, so no
    /// hand-off of a launch is ever there unmarked.
    pub(crate) fn launching(&self, session: &str, handed: &HandOff) -> std::io::Result<()> {
        let mark = self
            .mark_path(session)
            .ok_or_else(|| std::io::Error::other("a child's id is a UUID"))?;
        self.private_dir(&self.dir)?;
        self.private_dir(&self.dir.join(LAUNCH_DIR))?;
        self.fs
            .write_atomic(&mark, b"", Perms::Exact(Mode::from_bits(FILE_MODE)))?;
        self.hand_off(session, handed)
    }

    /// The hand-off of `session` claimed by its `LaunchChild`: its mark
    /// cleared, the hand-off left.
    pub(crate) fn claim_hand_off(&self, session: &str) {
        if let Some(mark) = self.mark_path(session) {
            let _ = self.fs.remove_file(&mark);
        }
    }

    /// The hand-off of `session`, read and left where it is (T6: it lives as
    /// long as the child is open). None when there is none, or it does not
    /// read.
    pub(crate) fn read_hand_off(&self, session: &str) -> Option<HandOff> {
        let path = self.hand_off_path(session)?;
        decode_hand_off(&self.fs.read_to_string(&path).ok()?)
    }

    /// Delete the hand-off of `session` (its child closed, its launch
    /// refused, or swept).
    pub(crate) fn delete_hand_off(&self, session: &str) {
        if let Some(path) = self.hand_off_path(session) {
            let _ = self.fs.remove_file(&path);
        }
        self.claim_hand_off(session);
    }

    /// The run locks a launch's lock is among (`daemon_api::launch_lock_name`).
    pub(crate) fn launch_locks(&self) -> liveness::RunLockFiles {
        liveness::RunLockFiles::new(Arc::clone(&self.fs), self.home.clone())
    }

    /// Whether the creator of `session`'s launch still holds its launch
    /// lock: its file there and not provably free (a probe never reads an
    /// unreadable lock as free). None there: its creator let it go, or never
    /// took one.
    fn creator_holds(&self, session: &str) -> bool {
        let locks = self.launch_locks();
        let path = locks.path(&daemon_api::launch_lock_name(session));
        self.fs.exists(&path).unwrap_or(true)
            && run_ports::RunLocks::probe(&locks, &path.display().to_string())
                != run_ports::LockProbe::Free
    }

    /// Every hand-off there is, by its child's stable id: whether it is
    /// marked in launch, and whether its launch's creator still holds its
    /// launch lock. A file not named by a UUID is no hand-off.
    pub(crate) fn hand_offs(&self) -> Vec<daemon_application::Kept> {
        let dir = self.dir.join(LAUNCH_DIR);
        let Ok(entries) = self.fs.read_dir(&dir) else {
            return Vec::new();
        };
        entries
            .into_iter()
            .filter_map(|entry| {
                let name = entry.name.to_str()?.to_string();
                let id = uuid::Uuid::parse_str(&name).ok()?.hyphenated().to_string();
                (id == name).then_some(())?;
                let marked = self
                    .mark_path(&name)
                    .is_some_and(|mark| self.fs.exists(&mark).unwrap_or(false));
                Some(daemon_application::Kept {
                    marked,
                    creator_holds: marked && self.creator_holds(&name),
                    session: name,
                })
            })
            .collect()
    }

    /// The log file's path.
    pub(crate) fn log_path(&self) -> PathBuf {
        self.path(LOG_FILE)
    }

    /// The test-support switch that kills the daemon once a client's write
    /// is applied, before it is answered (slice 3 step 4), taken by the
    /// daemon that finds it: whether it was there.
    #[cfg(feature = "test-support")]
    pub(crate) fn take_die_after_apply(&self) -> bool {
        self.fs.remove_file(&self.path(DIE_AFTER_APPLY)).is_ok()
    }

    /// The test-support switch that kills the daemon outright at one step
    /// (slice 4 U6, `test-die-at`, its one line the step's word: `enqueued`,
    /// `minted`, `started`, `turn`, `hook`, `discovery`): whether it names
    /// `step`, taken (removed) when it does, so the daemon started after
    /// finds none.
    #[cfg(feature = "test-support")]
    pub(crate) fn take_die_at(&self, step: &str) -> bool {
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
    pub(crate) fn hold_at(&self, point: &str) {
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
    pub(crate) fn alarms_held_at(&self) -> Option<i64> {
        let bytes = self.fs.read(&self.path(HOLD_ALARMS)).ok()?;
        String::from_utf8_lossy(&bytes).trim().parse().ok()
    }

    /// The test-support switch that makes the daemon deaf to a stop and a
    /// drain (`test-deaf-to-stop`): while it is there, each is taken and
    /// ignored, so `ctxloom shutdown` finds a daemon that will not exit.
    #[cfg(feature = "test-support")]
    pub(crate) fn deaf_to_stop(&self) -> bool {
        self.fs.exists(&self.path(DEAF_TO_STOP)).unwrap_or(false)
    }

    /// The test-support wait `ctxloom shutdown` gives a daemon it asked the
    /// plain stop (`test-shutdown-stop-wait`, its one line whole seconds);
    /// none, or one that does not read, is none.
    #[cfg(feature = "test-support")]
    pub(crate) fn shutdown_stop_wait(&self) -> Option<std::time::Duration> {
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
    pub(crate) fn start_up_fails(&self) -> Option<String> {
        let bytes = self.fs.read(&self.path(START_UP_FAILS)).ok()?;
        Some(String::from_utf8_lossy(&bytes).trim().to_string())
    }
}

/// The daemon's hand-offs, as its API makes them (`CreateHandOff`, S10):
/// each child planned by its project's dry run in the environment its
/// launch sent, judged on the evidence the plan gives (by name only), and
/// its hand-off written through [`DaemonFiles`]. What a child may be given
/// of that environment is its engine's allowlist, its engine block's `env`,
/// its credential source's variables and the permits
/// (`engine_claude::launcher_environment`); where its launcher finds its
/// user's settings is the launch's [`super::SETTINGS_PLACES`].
pub(crate) struct ApiHandOffs {
    files: DaemonFiles,
    /// Each launch's client program, kept beside its hand-off.
    programs: HandedPrograms,
    home: PathBuf,
    temp: PathBuf,
    /// The engines a child is hosted on: their names and credentials.
    engines: Vec<daemon_application::HostedEngine>,
}

/// The daemon's hand-offs (slice 4 U3: a child's hand-off planned, judged
/// and kept here), over `home`'s daemon files, planning with `temp` and the
/// `engines` children are hosted on.
pub(crate) fn api_hand_offs(
    fs: &Arc<dyn Fs>,
    (home, temp): (&Path, &Path),
    engines: &[daemon_application::HostedEngine],
    programs: &HandedPrograms,
) -> Arc<dyn daemon_api::HandOffs> {
    Arc::new(ApiHandOffs {
        files: DaemonFiles::new(Arc::clone(fs), home),
        programs: programs.clone(),
        home: home.to_path_buf(),
        temp: temp.to_path_buf(),
        engines: engines.to_vec(),
    })
}

impl daemon_api::HandOffs for ApiHandOffs {
    fn plan(&self, request: &daemon_api::HandingOff) -> Result<daemon_api::Planned, String> {
        // Keyed as the platform compares names (uppercase on Windows), as a
        // run's environment is (`os::Flavor::env_key`).
        let flavor = os::host().flavor();
        let caller: std::collections::BTreeMap<String, String> = request
            .env
            .iter()
            .map(|(name, value)| (flavor.env_key(name), value.clone()))
            .collect();
        let previewed = super::child_plan(
            &self.home,
            &self.temp,
            &request.engine,
            &request.agent,
            &request.root,
            &caller,
            request.trusted,
        )?;
        // None asked: the engine the plan resolved for the agent (its own,
        // else the config's default engine).
        let engine = match request.engine.is_empty() {
            true => previewed.plan.engine.clone(),
            false => request.engine.clone(),
        };
        let drivable: Vec<String> = self
            .engines
            .iter()
            .map(|engine| engine.name().to_string())
            .collect();
        let base = self
            .engines
            .iter()
            .find(|hosted| hosted.name() == engine)
            .map_or(run_ports::Credentials::NONE, |hosted| {
                hosted.engine.credentials()
            });
        let child = run_application::hosted_child(&previewed, &engine, &drivable, base, &caller);
        let credential = child.evidence.credential.clone().unwrap_or_default();
        Ok(daemon_api::Planned {
            engine,
            drivable_engines: child.evidence.drivable_engines.clone(),
            credential: runs::CredentialEvidence {
                file_only: credential.file_only,
                builtin_variables: credential.builtin_variables,
                accepted_variables: credential.accepted_variables,
                accepted_set: credential.accepted_set,
            },
            fatal_findings: child.evidence.fatal_findings,
            untrusted_settings: child.evidence.untrusted_settings.clone(),
            content: daemon_api::HandOffContent {
                env: engine_claude::launcher_environment(&child.settings, &caller),
                launcher: super::settings_places(&caller),
                agent: request.agent.clone(),
                // The API's to say: the client that asked.
                program: None,
            },
        })
    }

    fn write(&self, child: &str, content: &daemon_api::HandOffContent) -> Result<(), String> {
        let handed = HandOff {
            env: content.env.clone(),
            launcher: content.launcher.clone(),
            agent: content.agent.clone(),
            program: None,
        };
        self.programs.keep(child, content.program.clone());
        self.files
            .launching(child, &handed)
            .map_err(|e| e.to_string())
    }

    fn delete(&self, child: &str) -> Result<(), String> {
        self.programs.forget(child);
        self.files.delete_hand_off(child);
        Ok(())
    }
}

/// Each hosted launch's client program (owner, 2026-11-18: a launch runs
/// the CLIENT's program for its hooks and leads its engine's `PATH` with its
/// directory), by its child's id, from its `CreateHandOff` to its close: in
/// the daemon's memory, never in the hand-off file (whose format is
/// unchanged). A daemon started again between the two has none of them, and
/// the child runs the daemon's own program, which the version rule makes the
/// same version.
#[derive(Clone, Default)]
pub(crate) struct HandedPrograms(
    Arc<std::sync::Mutex<std::collections::BTreeMap<String, daemon_application::Program>>>,
);

impl HandedPrograms {
    /// Keep `program` for the child `child` (none: forget any kept).
    pub(crate) fn keep(&self, child: &str, program: Option<daemon_application::Program>) {
        let mut kept = self
            .0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        match program {
            Some(program) => kept.insert(child.to_string(), program),
            None => kept.remove(child),
        };
    }

    /// The program kept for the child `child`.
    pub(crate) fn of(&self, child: &str) -> Option<daemon_application::Program> {
        let kept = self
            .0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        kept.get(child).cloned()
    }

    /// Forget the child `child`'s program.
    pub(crate) fn forget(&self, child: &str) {
        self.keep(child, None);
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

/// A hand-off's text: its header, then a `NAME=value` line per variable of
/// the child's environment, a `@NAME=value` line per place of its
/// launcher's and, for an agent named, a `!agent=<agent>` line, each value's
/// backslashes, newlines and carriage returns escaped.
pub(crate) fn encode_hand_off(handed: &HandOff) -> String {
    let mut text = format!("{HAND_OFF_HEADER}\n");
    let mut lines: Vec<(String, &str)> = handed
        .env
        .iter()
        .map(|(name, value)| (name.clone(), value.as_str()))
        .collect();
    lines.extend(
        handed
            .launcher
            .iter()
            .map(|(name, value)| (format!("{LAUNCHER_MARK}{name}"), value.as_str())),
    );
    if !handed.agent.is_empty() {
        lines.push((AGENT_LINE.to_string(), handed.agent.as_str()));
    }
    for (name, value) in lines {
        let escaped = value
            .replace('\\', "\\\\")
            .replace('\n', "\\n")
            .replace('\r', "\\r");
        text.push_str(&format!("{name}={escaped}\n"));
    }
    text
}

/// What [`encode_hand_off`] wrote; none for anything else.
pub(crate) fn decode_hand_off(text: &str) -> Option<HandOff> {
    let mut lines = text.lines();
    if !matches!(lines.next()?, HAND_OFF_HEADER | HAND_OFF_HEADER_2) {
        return None;
    }
    let mut handed = HandOff::default();
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
        if name == AGENT_LINE {
            handed.agent = value;
            continue;
        }
        match name.strip_prefix(LAUNCHER_MARK) {
            Some(place) => handed.launcher.insert(place.to_string(), value),
            None => handed.env.insert(name.to_string(), value),
        };
    }
    Some(handed)
}

#[cfg(test)]
#[path = "files.test.rs"]
mod tests;
