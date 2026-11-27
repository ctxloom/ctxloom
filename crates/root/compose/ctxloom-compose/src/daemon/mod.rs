//! The user daemon (D18), as the hidden `ctxloom daemon` noun:
//!
//! - `serve`: be the daemon ([`serve`]); a client spawns it ([`ensure`]);
//! - `status`, `stop`: whether one serves, and ask it to stop or drain;
//! - `resolve`: register this project with it, or recheck it (tests and
//!   diagnosis).
//!
//! The user's child verbs are `session launch|send|clear|close` (slice 4
//! U3, S2, S19), clients of the daemon ([`child_caller`]); the hidden
//! `daemon launch|send|close|clear` are gone (owner ruling s4-Q13, hard
//! migrate).

pub mod drain;
pub mod files;
pub mod flush;
pub mod heard;
pub mod runtime;

use files::{instance_lock_path, DaemonFiles};

use std::ffi::{OsStr, OsString};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, Instant};

use cli_support::Failure;
use cli_support::{Exit, ExitClass};
use daemon_ports::{Daemons, Unreached};
use file_system::Open;
use os::Mode;
use process_env::EnvSnapshot;
use sessions::ProjectName;

use crate::{LibError, Wiring};

/// How fast a daemon runs: its idle limit, how often it looks at its stores,
/// and how long a drain waits for a busy child before it hands it over.
#[derive(Clone, Copy, Debug)]
pub struct Pace {
    pub idle: Duration,
    pub poll: Duration,
    pub hand_over: Duration,
}

/// What `ctxloom daemon serve` runs: the daemon's composition
/// (`ctxloom-daemon`'s `serve`), handed to the command line by the binary,
/// which is the one composition root of both programs. The CLI's own wiring
/// opens no store and starts no kernel: it only reaches the daemon.
pub type Serve = fn(&Arc<EnvSnapshot>, &str, Pace) -> Result<Exit, Failure>;

/// What the daemon's composition gives the command line, handed in by the
/// binary (the composition root of both programs): `ctxloom-wiring` itself
/// opens no store and hosts no kernel, so it cannot serve a daemon, name the
/// store's legacy files or run the tests' store commands.
#[derive(Clone, Copy)]
pub struct DaemonEntry {
    pub serve: Serve,
    /// The hidden `test` commands that read or write the store.
    #[cfg(feature = "test-support")]
    pub store_tests: crate::store_verb::StoreTest,
}

impl DaemonEntry {
    /// No daemon composition: `daemon serve` fails and the store's test
    /// commands fail. What a command line built
    /// without the daemon crate (a wiring test) has.
    pub const NONE: DaemonEntry = DaemonEntry {
        serve: no_daemon,
        #[cfg(feature = "test-support")]
        store_tests: |_, _, _, _| {
            Err(failed(LibError::NoRuntime {
                detail: NO_DAEMON.to_string(),
            }))
        },
    };
}

const NO_DAEMON: &str = "this command line was built without the daemon's composition";

pub fn no_daemon(_: &Arc<EnvSnapshot>, _: &str, _: Pace) -> Result<Exit, Failure> {
    Err(failed(LibError::NoRuntime {
        detail: NO_DAEMON.to_string(),
    }))
}

/// The stack the daemon serves on (a hosted session's is the same size).
pub const DAEMON_STACK: usize = 16 * 1024 * 1024;

/// The noun.
pub const NOUN: &str = "daemon";

/// The variables a spawned daemon is given of its spawner's (D-N45), as
/// the platform compares names: how to find programs, temp space and locale,
/// and on Windows what a program needs to start
/// (`run_ports::WINDOWS_STARTUP_VARIABLES`, kept beside these). Never the
/// spawner's home or its confinement (`HOME`, `USERPROFILE`, `APPDATA`,
/// `LOCALAPPDATA`, the XDG bases): the daemon's home is the ACCOUNT's (M4,
/// one daemon per OS user), whoever spawned it.
const DAEMON_ENV: [&str; 4] = ["PATH", "TMPDIR", "LANG", "LC_ALL"];

/// S16: how long a hook spends, in all, reaching a daemon (rediscovering
/// it, or spawning one and waiting) and delivering, before it fails open.
pub const HOOK_BOUND: Duration = Duration::from_secs(10);

/// What a delivery is left of [`HOOK_BOUND`] after the longest spawn wait.
pub const DELIVERY_MARGIN: Duration = Duration::from_secs(2);

/// How long a client waits for a daemon it spawned to answer its health
/// check: what [`HOOK_BOUND`] leaves once a delivery has its margin.
pub const SPAWN_WAIT: Duration = HOOK_BOUND.saturating_sub(DELIVERY_MARGIN);

/// How long one health check of a daemon may take (a loopback TLS
/// handshake and one call; a stale port refuses at once).
pub const HEALTH_WAIT: Duration = Duration::from_secs(1);

/// The environment a spawned daemon is given of its spawner's `vars`
/// (D-N45): what it needs to run, named as `flavor` compares names (so
/// Windows' `Path` is `PATH`), and ctxloom's `home` when the spawner set
/// one (its `CTXLOOM_DAEMON_ROOT`; with none the daemon serves the account's home,
/// [`daemon_home`]). Nothing else of the spawner's crosses: a child's
/// environment is its launcher's (S6).
pub fn daemon_env<N: AsRef<OsStr>, V: AsRef<OsStr>>(
    flavor: os::Flavor,
    vars: impl IntoIterator<Item = (N, V)>,
    home: Option<&Path>,
) -> Vec<(OsString, OsString)> {
    let kept: Vec<String> = DAEMON_ENV
        .iter()
        .chain(run_ports::WINDOWS_STARTUP_VARIABLES)
        .map(|name| flavor.env_key(name))
        .collect();
    let mut env: Vec<(OsString, OsString)> = vars
        .into_iter()
        .filter(|(name, _)| {
            name.as_ref()
                .to_str()
                .is_some_and(|name| kept.contains(&flavor.env_key(name)))
        })
        .map(|(name, value)| (name.as_ref().to_os_string(), value.as_ref().to_os_string()))
        .collect();
    if let Some(home) = home {
        env.push((
            run_ports::DAEMON_ROOT_VARIABLE.into(),
            home.as_os_str().to_os_string(),
        ));
    }
    env
}

/// The test-support variable moving every ctxloom clock on by a span of the
/// duration grammar (`3h`, `2d`, `90m`, `0s`): the daemon's (a kept trash
/// slot's age, G12), and the screen of `top`. Passed on to a daemon a
/// command starts. A bare integer or a negative span is refused, so a clock
/// only ever moves on ([`clock_offset`]).
#[cfg(feature = "test-support")]
pub const CLOCK_OFFSET_VARIABLE: &str = "CTXLOOM_TEST_CLOCK_OFFSET";

/// How far `env` moves the clocks on: nothing when unset, else the span it
/// names. The ONE reader of [`CLOCK_OFFSET_VARIABLE`]; the shipped binary
/// has no offset and never reads it.
#[cfg(feature = "test-support")]
pub fn clock_offset(
    env: &process_env::EnvSnapshot,
) -> Result<std::time::Duration, duration_text::DurationError> {
    match env.non_empty(CLOCK_OFFSET_VARIABLE) {
        None => Ok(std::time::Duration::ZERO),
        Some(text) => duration_text::parse(&text.to_string_lossy()),
    }
}

/// The shipped binary's clocks are the system's.
#[cfg(not(feature = "test-support"))]
pub fn clock_offset(
    _: &process_env::EnvSnapshot,
) -> Result<std::time::Duration, duration_text::DurationError> {
    Ok(std::time::Duration::ZERO)
}

/// The test-support variable naming a test's LIFELINE: a lock file the
/// test process holds for its life (the harness's `tests::lifeline`). A
/// daemon given it that can take that lock has outlived its test (one
/// killed by nextest's timeout drops nothing, so nothing else stops it):
/// it ends what it hosts and exits (`serve::Lifeline`). Passed on to a
/// daemon a command starts. Read only under `test-support`.
#[cfg(feature = "test-support")]
pub const LIFELINE_VARIABLE: &str = "CTXLOOM_TEST_LIFELINE";

/// The test-support variable naming the OS account's home, so a test's
/// temporary home is the account's for the daemon (and a test can tell the
/// account's home from a moved HOME). Read only under `test-support`.
#[cfg(feature = "test-support")]
pub const ACCOUNT_HOME_VARIABLE: &str = "CTXLOOM_TEST_ACCOUNT_HOME";

/// The OS account's home, as the system keeps it (rust-support's
/// `Os::user_home`: the account database's, never `HOME` or `USERPROFILE`,
/// so a process whose HOME was moved still finds the account's); none when
/// the account has none. One daemon per OS USER: the single-instance lock
/// and the daemon's own home are derived from it.
#[cfg(not(feature = "test-support"))]
pub fn account_home(_: &EnvSnapshot) -> Option<PathBuf> {
    os::host().user_home().ok()
}

/// Under `test-support` the account's home is the test's
/// ([`ACCOUNT_HOME_VARIABLE`]), else the process's ctxloom home
/// (`CTXLOOM_DAEMON_ROOT`), else its HOME (each test's temporary home), never the
/// real account's: no test reaches the real account's lock or files. The
/// ctxloom home stands in before HOME because it is what an engine's hook
/// processes carry (their HOME is the session home): as the account's real
/// home does in a shipped binary, it finds them the daemon that started
/// their engine (slice 4 U7: no run tells its hooks an endpoint of its own).
#[cfg(feature = "test-support")]
pub fn account_home(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(ACCOUNT_HOME_VARIABLE)
        .or_else(|| env.non_empty(run_ports::DAEMON_ROOT_VARIABLE))
        .or_else(|| env.home())
        .map(PathBuf::from)
}

/// The daemon's ctxloom home (M4): `CTXLOOM_DAEMON_ROOT` when it was given one
/// (its spawner's, [`daemon_env`]), else the ACCOUNT's home, never its HOME:
/// the daemon serves the account, whoever spawned it.
pub fn daemon_home(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(run_ports::DAEMON_ROOT_VARIABLE)
        .map(PathBuf::from)
        .or_else(|| account_home(env))
}

/// The test-support variable that makes the daemon's start die or deepen
/// (CI on Windows, 2026-11-19): `panic` panics on the daemon's thread before
/// it serves; `stack:<MiB>` uses that much of its stack first (more than
/// the thread has overflows it). Passed on to a daemon a command starts.
#[cfg(feature = "test-support")]
pub const START_VARIABLE: &str = "CTXLOOM_TEST_DAEMON_START";

/// The variable a client passes on as the spawned daemon's idle time, in
/// seconds (the tests' short one).
const IDLE_VARIABLE: &str = "CTXLOOM_DAEMON_IDLE_SECONDS";

pub fn failed(cause: impl coded_diagnostic::Diagnostic + 'static) -> Failure {
    Failure::new(cause, Exit::FAILED)
}

/// How long one call to the daemon may take.
const CALL_WAIT: Duration = Duration::from_secs(5);

/// How many git invocations one scratch worktree's removal may take
/// (resolve, inspect, the merge checks, `worktree remove`, `branch -d`).
const GIT_CALLS_PER_WORKTREE: u64 = 6;

/// How many scratch worktrees a destroy's deadline is sized for, every git
/// invocation of each taking its whole timeout.
const WORKTREES_BOUNDED: u64 = 10;

/// How long one destroy call (`DeleteSession`, `SweepSessions`) may take:
/// its worktrees' git, each invocation bounded by worktree-git's own
/// timeout, plus a read's wait as headroom (half an hour and five seconds).
const DESTROY_WAIT: Duration = Duration::from_secs(
    worktree_git::DEFAULT_TIMEOUT.as_secs() * GIT_CALLS_PER_WORKTREE * WORKTREES_BOUNDED
        + CALL_WAIT.as_secs(),
);

/// How long a client waits for a daemon of another version it stopped to
/// exit (M3).
const STOPPED_WAIT: Duration = Duration::from_secs(8);

/// The daemon `found` names, as a client calls it: each request made on
/// the user's behalf filled with `caller` (owner, 2026-11-18).
pub fn client_of(
    found: &run_ports::Discovery,
    caller: daemon_proto::v1::Caller,
) -> daemon_client::DaemonClient {
    daemon_client::DaemonClient::new(found, CALL_WAIT)
        .destroying(DESTROY_WAIT)
        .filling(caller)
}

/// The daemon `found` names, as a command calls it: the warnings each of
/// its answers carries heard, to be said as the command ends (OD-55).
pub fn hearing_client_of(
    found: &run_ports::Discovery,
    caller: daemon_proto::v1::Caller,
) -> daemon_client::DaemonClient {
    client_of(found, caller).hearing(std::sync::Arc::new(heard::hear))
}

/// `future`, on a runtime of its own.
pub fn block_on<T>(future: impl std::future::Future<Output = T>) -> Result<T, Failure> {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map(|runtime| runtime.block_on(future))
        .map_err(|e| {
            failed(LibError::NoRuntime {
                detail: e.to_string(),
            })
        })
}

/// A client's call that came to nothing, as the command's failure.
pub fn called(error: daemon_api_client::CallError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit)
}

/// The stamp this client compares with the daemon's (M3): the binary's,
/// or, under test-support, the one a test names.
#[cfg(feature = "test-support")]
pub fn own_stamp<'a>(env: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
    env.utf8(STAMP_VARIABLE)
        .filter(|s| !s.is_empty())
        .unwrap_or(stamp)
}

#[cfg(not(feature = "test-support"))]
pub fn own_stamp<'a>(_: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
    stamp
}

/// The variable a test names a client's stamp by (M3's skew, on demand).
#[cfg(feature = "test-support")]
const STAMP_VARIABLE: &str = "CTXLOOM_TEST_STAMP";

/// M3: the serving daemon `found`, reconciled with this client's stamp
/// `own`: of another version and idle, it is stopped and waited out (the
/// next `ensure` starts this one's); busy, the command is refused.
pub fn reconciled(
    files: &DaemonFiles,
    found: Option<&run_ports::Discovery>,
    own: &str,
) -> Result<(), Failure> {
    let Some(found) = found else {
        return Ok(());
    };
    // Its calls (`GetDaemon`, `StopDaemon`) carry no caller.
    let outcome = block_on(daemon_api_client::reconcile(
        &hearing_client_of(found, daemon_proto::v1::Caller::default()),
        own,
        &found.stamp,
    ))?;
    match outcome {
        Ok(daemon_api_client::Reconciled::Same) => Ok(()),
        Ok(daemon_api_client::Reconciled::Stopped) => {
            let deadline = Instant::now() + STOPPED_WAIT;
            while Instant::now() < deadline && files.discovery().is_some_and(|d| live(&d)) {
                std::thread::sleep(Duration::from_millis(20));
            }
            Ok(())
        }
        Err(skew) => {
            let exit = skew.exit();
            Err(Failure::new(skew, exit))
        }
    }
}

/// Whether a client waits on for a daemon to advertise itself once the one
/// it spawned has exited with the status `code` (`None`: a signal's): only
/// when that one lost the single-instance lock to another daemon, which is
/// the one to wait for. Any other exit means no daemon is coming of this
/// spawn, so the client stops waiting (the daemon cluster's cap, owner
/// ruling 2026-11-18: it waited out all of [`SPAWN_WAIT`] for a daemon that
/// had died at once).
pub fn spawn_waits_on(code: Option<i32>) -> bool {
    let held = LibError::DaemonInstanceHeld {
        lock: String::new(),
    };
    code == Some(i32::from(held.exit().code(false)))
}

/// The single-instance lock of `wiring`'s ACCOUNT ([`instance_lock_path`]
/// under [`account_home`], its ctxloom home where the account has none), a
/// test's override first.
pub fn instance_lock(wiring: &Wiring) -> PathBuf {
    let account = account_home(wiring.env());
    let base = account.as_deref().unwrap_or(wiring.home());
    instance_lock_path(base, instance_override(wiring.env()).as_deref())
}

/// The directory a test names for the single-instance lock
/// ([`files::INSTANCE_DIR_VARIABLE`]); compiled out of the shipped binary.
#[cfg(feature = "test-support")]
pub fn instance_override(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(files::INSTANCE_DIR_VARIABLE)
        .map(PathBuf::from)
}

/// The shipped binary has no override: the account's place alone.
#[cfg(not(feature = "test-support"))]
pub fn instance_override(_: &EnvSnapshot) -> Option<PathBuf> {
    None
}

/// The daemon files of `wiring`'s home, its account's single-instance lock.
pub fn files_of(wiring: &Wiring) -> DaemonFiles {
    DaemonFiles::with_instance(wiring.fs(), wiring.home(), instance_lock(wiring))
}

/// The live daemon `files` advertise, if one serves; refused when it serves
/// another ctxloom home than `wiring`'s (one daemon per OS user, so this
/// command is never given a second).
pub fn serving(
    wiring: &Wiring,
    files: &DaemonFiles,
) -> Result<Option<run_ports::Discovery>, Failure> {
    serving_here(wiring, files).map_err(|refused| Failure::new(refused, Exit::REFUSED))
}

/// [`serving`]'s answer, its refusal the wiring's own.
pub fn serving_here(
    wiring: &Wiring,
    files: &DaemonFiles,
) -> Result<Option<run_ports::Discovery>, LibError> {
    let Some(found) = files.discovery().filter(live) else {
        return Ok(None);
    };
    let home = wiring.home().display().to_string();
    if found.home == home {
        Ok(Some(found))
    } else {
        Err(LibError::DaemonOtherHome {
            home,
            daemon_home: found.home,
        })
    }
}

/// The user's daemon as a command reaches it (daemon-ports' `Daemons`,
/// slice 2 step 6): the discovery file's, if it is live (health) and this
/// home's; else, for a call, one spawned and waited for (S16's bound), a
/// daemon of another version stopped first when it is idle (M3).
pub struct Reach<'a> {
    wiring: &'a Wiring,
    stamp: String,
    /// Whether this reach never ends what a daemon hosts (OD-3: a session's
    /// MCP server's calls): an idle daemon of an older version is asked to
    /// stop, and any other of another stamp refuses the call.
    never_stops: bool,
    /// Whether reaching the daemon delivers the home's holds first, once a
    /// process (s4-Q14, H3): every command's but the `daemon` noun's own,
    /// which look at the daemon and change nothing.
    flushes: bool,
    /// Whether the daemon's warnings its answers carry are heard, to be
    /// said on stderr as the command ends (OD-55): every command's but
    /// `daemon status`'s, which shows them in its answer.
    hears: bool,
}

impl<'a> Reach<'a> {
    /// The daemon of `wiring`'s home, as this client of stamp `stamp` (the
    /// binary's; a test's own under test-support) reaches it.
    pub fn new(wiring: &'a Wiring, stamp: &str) -> Reach<'a> {
        Reach {
            wiring,
            stamp: own_stamp(wiring.env(), stamp).to_string(),
            never_stops: false,
            flushes: true,
            hears: true,
        }
    }

    /// The stamp this client compares with the daemon's (M3).
    pub fn stamp(&self) -> &str {
        &self.stamp
    }

    /// This reach as the `daemon` noun's own verbs make it (`status`,
    /// `stop`, `resolve`): it delivers no hold on its way in.
    pub fn observing(self) -> Reach<'a> {
        Reach {
            flushes: false,
            ..self
        }
    }

    /// This reach as `daemon status` makes it: the daemon's warnings are
    /// its answer, not heard to be said on stderr.
    pub fn showing_warnings(self) -> Reach<'a> {
        Reach {
            hears: false,
            ..self
        }
    }

    /// The daemon `found` names, as this reach calls it: hearing the
    /// warnings its answers carry (OD-55), unless it shows them.
    fn client(&self, found: &run_ports::Discovery) -> daemon_client::DaemonClient {
        // A reach that never stops a daemon is a session's MCP server's.
        let kind = match self.never_stops {
            true => daemon_proto::v1::client::Kind::Mcp,
            false => daemon_proto::v1::client::Kind::Cli,
        };
        let caller = caller_of(self.wiring.env(), kind, &self.stamp);
        match self.hears {
            true => hearing_client_of(found, caller),
            false => client_of(found, caller),
        }
    }

    /// The daemon `found` names, reached: the home's holds delivered to it
    /// first (H3), the first time this process reaches it.
    fn reached(&self, found: &run_ports::Discovery) -> daemon_client::DaemonClient {
        if self.flushes {
            flush::on_the_way_in(self.wiring, &self.stamp, found);
        }
        self.client(found)
    }

    /// This reach as a session's call makes it (OD-3,
    /// [`daemon_api_client::reconcile_session`]): an idle daemon of an older
    /// version stopped and replaced by this binary's, any other daemon of
    /// another stamp a typed mismatch, and nothing a daemon hosts ended.
    pub fn never_stopping(self) -> Reach<'a> {
        Reach {
            never_stops: true,
            ..self
        }
    }

    /// The daemon log's path as a person reads it: under `~` when it is
    /// below the home.
    fn log(&self, files: &DaemonFiles) -> String {
        let log = files.log_path();
        match self.wiring.env().home().map(PathBuf::from) {
            Some(home) => match log.strip_prefix(&home) {
                Ok(below) => under_home(self.wiring.env().flavor(), below),
                Err(_) => log.display().to_string(),
            },
            None => log.display().to_string(),
        }
    }
}

/// `below`, a path relative to the home, as a person reads it under `~`:
/// `~` and each of its components joined by `flavor`'s separator (`/`, or
/// `\` on Windows), so a Windows path never mixes the two.
pub fn under_home(flavor: os::Flavor, below: &Path) -> String {
    let separator = match flavor {
        os::Flavor::Unix => "/",
        os::Flavor::Windows => "\\",
    };
    std::iter::once("~".to_string())
        .chain(
            below
                .components()
                .map(|part| part.as_os_str().to_string_lossy().into_owned()),
        )
        .collect::<Vec<_>>()
        .join(separator)
}

/// A refusal of the wiring's, as the reach declines.
pub fn declined(refused: impl coded_diagnostic::Diagnostic + Send + Sync + 'static) -> Unreached {
    Unreached::Declined(Arc::new(refused))
}

impl Daemons for Reach<'_> {
    type Api = daemon_client::DaemonClient;

    async fn serving(&self) -> Result<Option<daemon_client::DaemonClient>, Unreached> {
        let files = files_of(self.wiring);
        match serving_here(self.wiring, &files).map_err(declined)? {
            Some(found) => Ok(Some(self.reached(&found))),
            None => Ok(None),
        }
    }

    async fn reach(&self) -> Result<daemon_client::DaemonClient, Unreached> {
        let files = files_of(self.wiring);
        if let Some(found) = serving_here(self.wiring, &files).map_err(declined)? {
            // Its answers' warnings heard (OD-55).
            let api = self.client(&found);
            // OD-3: a session's call stops an idle older daemon, and is
            // refused by any other of another stamp.
            let reconciled = if self.never_stops {
                daemon_api_client::reconcile_session(&api, &self.stamp, &found.stamp).await
            } else {
                daemon_api_client::reconcile(&api, &self.stamp, &found.stamp).await
            };
            match reconciled {
                Ok(daemon_api_client::Reconciled::Same) => return Ok(self.reached(&found)),
                Ok(daemon_api_client::Reconciled::Stopped) => {
                    let deadline = Instant::now() + STOPPED_WAIT;
                    while Instant::now() < deadline && files.discovery().is_some_and(|d| live(&d)) {
                        std::thread::sleep(Duration::from_millis(20));
                    }
                }
                Err(daemon_api_client::SkewError::Call(_)) => {}
                Err(busy) => return Err(declined(busy)),
            }
        }
        let died = ensure(self.wiring, self.wiring.env());
        match serving_here(self.wiring, &files).map_err(declined)? {
            Some(found) => Ok(self.reached(&found)),
            None => Err(match died {
                Some(Died { status, tail }) => Unreached::Exited {
                    log: self.log(&files),
                    status,
                    tail,
                },
                None => Unreached::NotStarted {
                    log: self.log(&files),
                },
            }),
        }
    }
}

/// The caller's whole environment, as a call to the daemon carries it (S7).
pub fn env_vars(env: &EnvSnapshot) -> daemon_proto::v1::EnvVars {
    daemon_proto::v1::EnvVars {
        vars: env
            .vars_utf8()
            .into_iter()
            .map(|(name, value)| daemon_proto::v1::EnvVar { name, value })
            .collect(),
    }
}

/// Which client this process is to the daemon (owner, 2026-11-18): `kind`,
/// the running binary's file name and directory (what a launch it asks for
/// runs for its hooks and leads its engine's `PATH` with), and its version
/// stamp (`stamp`, a test's own under test-support), which the daemon
/// admits only when it is its own.
pub fn calling_client(
    env: &EnvSnapshot,
    kind: daemon_proto::v1::client::Kind,
    stamp: &str,
) -> daemon_proto::v1::Client {
    let program = crate::program::running();
    daemon_proto::v1::Client {
        kind: kind as i32,
        program: program.name.clone(),
        program_dir: program.dir.clone(),
        version: own_stamp(env, stamp).to_string(),
    }
}

/// What every request this process makes on its user's behalf carries of
/// it (owner, 2026-11-18): its working directory, its whole environment
/// (S7) and [`calling_client`]; a command's own flags and presentable are its
/// request's.
pub fn caller_of(
    env: &EnvSnapshot,
    kind: daemon_proto::v1::client::Kind,
    stamp: &str,
) -> daemon_proto::v1::Caller {
    daemon_proto::v1::Caller {
        cwd: env
            .cwd()
            .map(|cwd| cwd.display().to_string())
            .unwrap_or_default(),
        env: Some(env_vars(env)),
        client: Some(calling_client(env, kind, stamp)),
        ..daemon_proto::v1::Caller::default()
    }
}

/// This command's client of the daemon (slice 2 step 6): `wiring`'s
/// project, its working directory and its whole environment. A project
/// with no identity yet is read as one where nothing is recorded (a session
/// read names no project, so it mints none: owner ruling 2026-11-14); one
/// that cannot be told is
/// outside a project, as before.
pub fn client<'a>(wiring: &'a Wiring, stamp: &str) -> daemon_api_client::Client<Reach<'a>> {
    client_reaching(wiring, Reach::new(wiring, stamp))
}

/// [`client`], reaching the daemon through `reach`.
pub fn client_reaching<'a>(
    wiring: &'a Wiring,
    reach: Reach<'a>,
) -> daemon_api_client::Client<Reach<'a>> {
    match wiring.registration() {
        Ok(Some(project)) => client_through(wiring, reach, Some(project)),
        Ok(None) | Err(LibError::ProjectUnnamed { .. }) => {
            client_through(wiring, reach, None).unnamed()
        }
        Err(_) => client_through(wiring, reach, None),
    }
}

/// A client of the daemon for `registration`'s project (none: outside one).
pub fn client_for<'a>(
    wiring: &'a Wiring,
    stamp: &str,
    registration: Option<daemon_proto::v1::ResolveProjectRequest>,
) -> daemon_api_client::Client<Reach<'a>> {
    client_through(wiring, Reach::new(wiring, stamp), registration)
}

/// [`client_for`], reaching the daemon through `reach`.
pub fn client_through<'a>(
    wiring: &'a Wiring,
    reach: Reach<'a>,
    registration: Option<daemon_proto::v1::ResolveProjectRequest>,
) -> daemon_api_client::Client<Reach<'a>> {
    let caller = caller_of(
        wiring.env(),
        daemon_proto::v1::client::Kind::Cli,
        reach.stamp(),
    );
    retrying(
        wiring,
        daemon_api_client::Client::new(reach, registration, caller)
            .with_request_ids(Box::new(request_id)),
    )
}

/// The client, its call made once more on a new daemon if this one could
/// not be reached (S8); under test-support, not when the command's
/// environment sets `CTXLOOM_TEST_NO_RETRY` (step 10b's harness: what a
/// killed daemon left is left).
/// And, under test-support, the program `CTXLOOM_TEST_BEFORE_RETRY` names
/// run (and waited for) before that one retry (step 10c U13's harness: a
/// person's hand edit between the daemon's loss and the write sent again).
#[cfg(feature = "test-support")]
pub fn retrying<'a>(
    wiring: &Wiring,
    client: daemon_api_client::Client<Reach<'a>>,
) -> daemon_api_client::Client<Reach<'a>> {
    let client = match wiring.env().non_empty("CTXLOOM_TEST_NO_RETRY") {
        Some(_) => client.without_retry(),
        None => client,
    };
    match wiring.env().non_empty(BEFORE_RETRY) {
        Some(program) => {
            let program = program.to_owned();
            client.before_retry(Box::new(move || run_before_retry(&program)))
        }
        None => client,
    }
}

/// The test-support variable naming what a client runs before its retry.
#[cfg(feature = "test-support")]
const BEFORE_RETRY: &str = "CTXLOOM_TEST_BEFORE_RETRY";

/// `program` run to its end; one that cannot be run is said on stderr
/// and the retry goes on (a test's harness, never a shipped path).
#[cfg(feature = "test-support")]
pub fn run_before_retry(program: &std::ffi::OsStr) {
    let ran = os::process::spawn_tree(std::process::Command::new(program))
        .and_then(|mut tree| tree.wait());
    if let Err(error) = ran {
        eprintln!("ctxloom: {BEFORE_RETRY} {program:?} did not run: {error}");
    }
}

/// The shipped client always retries once (S8).
#[cfg(not(feature = "test-support"))]
pub fn retrying<'a>(
    _: &Wiring,
    client: daemon_api_client::Client<Reach<'a>>,
) -> daemon_api_client::Client<Reach<'a>> {
    client
}

/// Where this process's library resource names are rooted (OD-40, OQ-2,
/// OQ-3): its project as the daemon's `ResolveProject` names it
/// (`projects/{harp}`; the daemon reached, started if none serves), or
/// `users/me` outside a project. A project nothing has named has its id
/// minted at its root, unless `minting` is a dry run's `Never`, which
/// refuses it (owner ruling 2026-11-14). A call that fails fails the command
/// in its own class.
pub fn library_parent(
    wiring: &Wiring,
    stamp: &str,
    minting: crate::Minting,
) -> Result<String, Failure> {
    let registration = wiring.library_registration(minting).map_err(|e| {
        let exit = e.exit();
        Failure::new(e, exit)
    })?;
    resolved_parent(wiring, stamp, registration)
}

/// A dry run's parent (owner rulings 2026-11-14: every dry run calls the
/// daemon, spawning it if needed; it never mints, and prints an unresolved
/// name as null): [`library_parent`]'s, or none where the project is a
/// ctxloom project nothing has named and `minting` is a dry run's `Never`.
/// The daemon is reached in every case, asked `GetDaemon` where there is
/// nothing to resolve (`daemon_api_client::Client::named_parent`), so one that
/// cannot be started fails the dry run as it fails every client's command.
pub fn library_parent_if_named(
    wiring: &Wiring,
    stamp: &str,
    minting: crate::Minting,
) -> Result<Option<String>, Failure> {
    let client = match wiring.library_registration(minting) {
        Err(LibError::ProjectUnnamed { .. }) if minting == crate::Minting::Never => {
            client_for(wiring, stamp, None).unnamed()
        }
        registration => client_for(
            wiring,
            stamp,
            registration.map_err(|e| {
                let exit = e.exit();
                Failure::new(e, exit)
            })?,
        ),
    };
    block_on(client.named_parent())?.map_err(called)
}

/// A dry run's client of the daemon (step 10a U2): its project registered,
/// never minted (owner ruling 2026-11-14: "dry run should *not* mint"); a
/// ctxloom project nothing has named is asked unnamed, under `users/me`,
/// and the daemon leaves its names unresolved.
pub fn preview_client<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api_client::Client<Reach<'a>>, Failure> {
    Ok(match wiring.library_registration(crate::Minting::Never) {
        Err(LibError::ProjectUnnamed { .. }) => client_for(wiring, stamp, None).unnamed(),
        registration => client_for(
            wiring,
            stamp,
            registration.map_err(|e| {
                let exit = e.exit();
                Failure::new(e, exit)
            })?,
        ),
    })
}

/// This command's client of the daemon for a library read (step 10a U2):
/// its project registered at its root (its id minted there if it has none
/// yet), else outside a project.
/// The loadout of a run planned on `profile` (step 10c U13's seed): the
/// profile as the daemon assembles it, its fragments' bundles.
#[cfg(feature = "test-support")]
pub fn profile_loadout(
    wiring: &Wiring,
    stamp: &str,
    profile: &str,
) -> Result<sessions::SessionLoadout, Failure> {
    use daemon_api_client::LibraryReads;
    let client = library_client(wiring, stamp)?;
    let profiles = vec![profile.to_string()];
    let assembled = answered(
        client.assemble_context(daemon_proto::v1::AssembleContextRequest {
            profiles: profiles.clone(),
            ..daemon_proto::v1::AssembleContextRequest::default()
        }),
    )?;
    Ok(daemon_application::reactions::loadout_of_assembled(
        "", &profiles, &assembled,
    ))
}

pub fn library_client<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api_client::Client<Reach<'a>>, Failure> {
    let registration = wiring
        .library_registration(crate::Minting::AtRoot)
        .map_err(|e| {
            let exit = e.exit();
            Failure::new(e, exit)
        })?;
    Ok(client_for(wiring, stamp, registration))
}

/// This command's client of the daemon for a read of the home's alone
/// (`loadout`, step 10a U2): asked under `users/me`, wherever it is run,
/// so no project is registered, minted or observed for it.
pub fn home_client<'a>(wiring: &'a Wiring, stamp: &str) -> daemon_api_client::Client<Reach<'a>> {
    client_for(wiring, stamp, None)
}

/// A client call's answer, or the failure it ends the command in, in its
/// own class.
pub fn answered<T>(
    call: impl std::future::Future<Output = Result<T, daemon_api_client::CallError>>,
) -> Result<T, Failure> {
    block_on(call)?.map_err(called)
}

/// The parent the daemon names for `registration` (`users/me` for none).
pub fn resolved_parent(
    wiring: &Wiring,
    stamp: &str,
    registration: Option<daemon_proto::v1::ResolveProjectRequest>,
) -> Result<String, Failure> {
    block_on(client_for(wiring, stamp, registration).library_parent())?.map_err(called)
}

/// The USER's client of the daemon for her child verbs (`session
/// launch|send|clear|close`, slice 4 U3): her project as the daemon names it
/// (registered or rechecked, a daemon reached or started), each write's id
/// minted per call, a launch that names no child minting its harp. Outside a
/// project it is refused (exit 48).
pub fn child_caller<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api_client::Caller<Reach<'a>>, Failure> {
    let parent = library_parent(wiring, stamp, crate::Minting::AtRoot)?;
    if ProjectName::parse(&parent).is_err() {
        let cwd = wiring
            .env()
            .cwd()
            .map(|cwd| cwd.display().to_string())
            .unwrap_or_default();
        return Err(Failure::new(
            daemon_api_client::ApiRejection::ProjectUnknown { cwd },
            Exit::REFUSED,
        ));
    }
    daemon_api_client::Caller::user(
        Reach::new(wiring, stamp),
        &parent,
        caller_of(wiring.env(), daemon_proto::v1::client::Kind::Cli, stamp),
        Box::new(request_id),
    )
    .map(|caller| {
        // `session launch --project <dir>`: the directory as the shell's
        // working directory names it (step 8 Q1).
        let base = wiring.env().cwd().unwrap_or_default();
        caller
            .naming(Box::new(crate::session_name))
            .holding(launch_locks(wiring))
            .resolving(project_resolver(wiring, base))
    })
    .map_err(|e| {
        let exit = e.exit();
        Failure::new(e, exit)
    })
}

/// Whether the user trusts the project `wiring` is in (step 8 U2, D4), as
/// the library asks it where the answer decides something: ONE `GetTrust`
/// of her daemon of this `stamp` (reached, or started), asked when the
/// library asks. Outside a project, or with no answer, it is untrusted.
pub fn trust_ask(wiring: &Wiring, stamp: &str) -> library_application::TrustAsk {
    let (env, stamp) = (Arc::clone(wiring.env()), stamp.to_string());
    std::rc::Rc::new(move || {
        let Ok(wiring) = Wiring::new(Arc::clone(&env)) else {
            return false;
        };
        let Ok(caller) = child_caller(&wiring, &stamp) else {
            return false;
        };
        block_on(caller.get_trust())
            .ok()
            .and_then(Result::ok)
            .is_some_and(|trust| trust.trusted)
    })
}

/// What a launch into another project (step 8 Q1) registers it with: the
/// directory as given, relative to `base` unless it is absolute, read as a
/// command run there reads its project (its own `.ctxloom`, never the
/// caller's `CTXLOOM_ROOT`), its id minted at its root where nothing has
/// named it, as a launch's own project's is. A directory in no ctxloom
/// project is refused, `daemon-api-contract-project-unknown`.
pub fn project_resolver(wiring: &Wiring, base: PathBuf) -> Arc<daemon_api_client::ProjectResolver> {
    let env = Arc::clone(wiring.env());
    let home = wiring.home().to_path_buf();
    let user_home = wiring.user_home();
    Arc::new(move |dir: &str| {
        let at = lexical(&base.join(dir));
        let vars: Vec<(OsString, OsString)> = env
            .vars()
            .filter(|(name, _)| *name != OsStr::new(run_ports::ROOT_VARIABLE))
            .map(|(name, value)| (name.to_os_string(), value.to_os_string()))
            .collect();
        let there = EnvSnapshot::from_parts(
            env.flavor(),
            Ok(at.clone()),
            env.temp_dir().to_path_buf(),
            vars,
        );
        let unknown = || {
            daemon_api_client::CallError::Refused(daemon_api_client::Rejection::Api(
                daemon_api_client::ApiRejection::ProjectUnknown {
                    cwd: at.display().to_string(),
                },
            ))
        };
        let wiring = Wiring::at(Arc::new(there), home.clone()).with_user_home(user_home.clone());
        match wiring.library_registration(crate::Minting::AtRoot) {
            Ok(Some(registration)) => Ok(registration),
            Ok(None) => Err(unknown()),
            Err(e) => Err(daemon_api_client::CallError::Call(
                daemon_ports::Called::Failed {
                    code: 0,
                    detail: coded_diagnostic::Diagnostic::code(&e).to_string(),
                },
            )),
        }
    })
}

/// `path` with its `.` and `..` taken out by their words alone: a
/// project's directory named `../api` is the sibling `api`, never found by a
/// walk up from `<project>/..` to the project itself.
pub fn lexical(path: &Path) -> PathBuf {
    use std::path::Component;
    let mut out = PathBuf::new();
    for part in path.components() {
        match part {
            Component::CurDir => {}
            Component::ParentDir => {
                if !out.pop() {
                    out.push(part);
                }
            }
            other => out.push(other),
        }
    }
    out
}

/// The launch locks a launch from `wiring`'s home holds (T6): its daemon's
/// run locks, which its sweep probes.
pub fn launch_locks(wiring: &Wiring) -> Arc<dyn run_ports::RunLocks> {
    Arc::new(files_of(wiring).launch_locks())
}

/// A write's request id (S8): a fresh UUIDv7, unique per call.
pub fn request_id() -> String {
    uuid::Uuid::now_v7().to_string()
}

/// Whether the daemon `found` names is live (T16, M11): it answers the
/// standard health check over the certificate it advertised, pinned, within
/// [`HEALTH_WAIT`]. The single-instance lock says nothing of it.
pub fn live(found: &run_ports::Discovery) -> bool {
    hook_sink_grpc::serving(&found.endpoint, &found.cert, HEALTH_WAIT)
}

/// The daemon a HOOK delivers to (S16, slice 4 §3.8 (ii)): the live one of
/// this home its discovery file names, else one spawned and waited for, at
/// most `wait`; none when none serves this home within it (none could be
/// started, or the one serving serves another home).
pub fn hook_daemon(env: &Arc<EnvSnapshot>, wait: Duration) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let files = files_of(&wiring);
    if let Some(found) = serving(&wiring, &files).ok()? {
        return Some(found);
    }
    let _ = ensure_within(&wiring, env, wait);
    serving(&wiring, &files).ok().flatten()
}

/// The daemon of this home the discovery file names, NOT probed (the lean
/// hook RPC: a health check is a TLS connection of its own): what a hook's
/// one call asks first; reached unreachable, it probes ([`hook_daemon`]).
/// None for a daemon of another home.
pub fn hook_daemon_unprobed(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let found = files_of(&wiring).discovery()?;
    (found.home == wiring.home().display().to_string()).then_some(found)
}

/// `discovery` published as `wiring`'s home's daemon's, for a test.
#[cfg(test)]
pub fn publish_for_test(wiring: &Wiring, discovery: &run_ports::Discovery) {
    files_of(wiring).publish(discovery).unwrap();
}

/// The daemon a hook's library read asks when it may start none (its
/// environment names an endpoint, OD-13): the live one of this home its
/// discovery file names, else none.
pub fn hook_daemon_serving(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let files = files_of(&wiring);
    serving(&wiring, &files).ok().flatten()
}

/// The variable a test harness sets to declare itself (nextest's setup script
/// `guard-homes` does, for every test): while it is non-empty a `test-support`
/// build never spawns its own executable as the daemon, which would run the
/// harness's tests again, each copy spawning once more (2026-11-27). A plain
/// build never reads it, so nothing in an install can tell a real binary not
/// to spawn.
#[cfg(feature = "test-support")]
pub const HARNESS_VARIABLE: &str = "CTXLOOM_TEST_HARNESS";

/// Whether a harness declared itself: a `test-support` build, and `value`
/// (the variable's) non-empty.
pub fn declared_harness(test_support: bool, value: Option<&std::ffi::OsStr>) -> bool {
    test_support && value.is_some_and(|value| !value.is_empty())
}

/// The executable to spawn as the daemon: this one, unless a test harness
/// declared itself ([`HARNESS_VARIABLE`]), or it cannot be named.
// Not mutated: a mutant that lets a unit test's harness spawn itself as the
// daemon starts a detached cascade (each copy runs the daemon tests, which
// spawn again), outside cargo-mutants' process group, until the run's memory
// cap kills cargo-mutants itself; such a mutant can be judged by no test.
#[cfg_attr(test, mutants::skip)]
pub fn own_executable() -> Option<PathBuf> {
    if harness_declared() {
        return None;
    }
    std::env::current_exe().ok()
}

/// Whether this process is a declared test harness: only a `test-support`
/// build looks.
#[cfg(feature = "test-support")]
fn harness_declared() -> bool {
    let env = EnvSnapshot::from_process();
    declared_harness(
        true,
        env.non_empty(HARNESS_VARIABLE).map(std::ffi::OsStr::new),
    )
}

#[cfg(not(feature = "test-support"))]
fn harness_declared() -> bool {
    declared_harness(false, None)
}

/// Make sure the user's daemon serves: spawn one when none is live, and
/// wait, within [`SPAWN_WAIT`], for a live one to advertise itself (the one
/// spawned, or another client's, whichever took the single-instance lock). A stale file
/// is left for the new daemon to publish over. A daemon that cannot be
/// spawned is not this command's failure (what it recorded stands; the next
/// client spawns one).
pub fn ensure(wiring: &Wiring, env: &EnvSnapshot) -> Option<Died> {
    ensure_within(wiring, env, SPAWN_WAIT)
}

/// [`ensure`], waiting at most `wait` for a live daemon (a hook's is what
/// its bound leaves); how the daemon it spawned died, when it exited before
/// it served and no other is coming.
pub fn ensure_within(wiring: &Wiring, env: &EnvSnapshot, wait: Duration) -> Option<Died> {
    let files = files_of(wiring);
    // A live daemon, this home's or (refused before the command ran)
    // another's: never a second.
    if files.discovery().is_some_and(|found| live(&found)) {
        return None;
    }
    let exe = own_executable()?;
    let mut command = os::process::Detach::new(exe);
    command.args(["daemon", "serve"]);
    if let Some(idle) = env.utf8(IDLE_VARIABLE) {
        command.args(["--idle-seconds", idle]);
    }
    // The daemon's own environment is what it needs to run, nothing more: a
    // child's is its launcher's (S6, D-N45), so no spawner's credential
    // lives in the daemon.
    let ctxloom_home = env
        .non_empty(run_ports::DAEMON_ROOT_VARIABLE)
        .map(PathBuf::from);
    let mut kept = daemon_env(env.flavor(), env.vars(), ctxloom_home.as_deref());
    // A test's place for the single-instance lock, and its account's home,
    // are the daemon's too.
    if let Some(dir) = instance_override(env) {
        kept.push((files::INSTANCE_DIR_VARIABLE.into(), dir.into_os_string()));
    }
    #[cfg(feature = "test-support")]
    if let Some(account) = account_home(env) {
        kept.push((ACCOUNT_HOME_VARIABLE.into(), account.into_os_string()));
    }
    // A test's clock offset is the daemon's clock's.
    #[cfg(feature = "test-support")]
    if let Some(span) = env.non_empty(CLOCK_OFFSET_VARIABLE) {
        kept.push((CLOCK_OFFSET_VARIABLE.into(), span.to_os_string()));
    }
    // A test's stamp is the daemon its client starts (M3's skew on demand):
    // the daemon admits only a client of its own version (owner,
    // 2026-11-18), so the daemon a client of a test's version starts is of
    // that version too.
    #[cfg(feature = "test-support")]
    if let Some(stamp) = env.non_empty(STAMP_VARIABLE) {
        kept.push((STAMP_VARIABLE.into(), stamp.to_os_string()));
    }
    // A test's lifeline is its daemon's, whoever starts it.
    #[cfg(feature = "test-support")]
    if let Some(line) = env.non_empty(LIFELINE_VARIABLE) {
        kept.push((LIFELINE_VARIABLE.into(), line.to_os_string()));
    }
    #[cfg(feature = "test-support")]
    if let Some(start) = env.non_empty(START_VARIABLE) {
        kept.push((START_VARIABLE.into(), start.to_os_string()));
    }
    // The daemon runs in its home, so a home not made yet (`just install`'s
    // `~/.ctxloom-rs` on a new machine) is made first, 0700 as the daemon
    // makes its own directories: a spawn into a missing directory fails.
    let _ = wiring
        .fs()
        .create_dir_all(wiring.home(), Mode::from_bits(0o700));
    command.env_clear().envs(kept).current_dir(wiring.home());
    // Its stderr is its log (appended to, as the daemon writes it), so a
    // death no panic hook hears (a stack overflow) still says why there.
    let log = files.log_path();
    if let Some(dir) = log.parent() {
        let _ = wiring.fs().create_dir_all(dir, Mode::from_bits(0o700));
    }
    if let Ok(file) = wiring.fs().open(&log, Open::Append(Mode::from_bits(0o600))) {
        command.stderr(os::process::Stream::File(file));
    }
    // DETACHED (T3, M8): a session of its own on unix (setsid), no console
    // and a process group of its own on Windows, so neither this terminal's
    // hangup or Ctrl-C nor a kill of this command's group reaches it; its
    // stdio null (the default) and no other handle of this command's
    // inherited, so a reader of this command's output sees its end when the
    // command exits; dropped, not waited for: it outlives this command.
    let Ok(mut spawned) = os::process::spawn_detached(command) else {
        return None;
    };
    let deadline = Instant::now() + wait;
    let mut exited = false;
    while Instant::now() < deadline {
        if files.discovery().is_some_and(|found| live(&found)) {
            return None;
        }
        // The daemon spawned has exited: unless another holds the lock (and
        // advertises), none is coming, and the wait is over. Looked at once,
        // after the discovery, so one that served and exited is seen first.
        if !exited {
            if let Ok(Some(status)) = spawned.try_wait() {
                if !spawn_waits_on(status.code()) {
                    let text = wiring.fs().read(&log).unwrap_or_default();
                    return Some(Died {
                        status: status.to_string(),
                        tail: tail_of(&String::from_utf8_lossy(&text), DIED_TAIL),
                    });
                }
                exited = true;
            }
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    None
}

/// How a daemon a client spawned died before it served: its exit status,
/// as the platform says it, and the last lines of its log.
#[derive(Debug, PartialEq)]
pub struct Died {
    pub status: String,
    pub tail: String,
}

/// The lines of a daemon's log [`Died`] holds.
const DIED_TAIL: usize = 12;

/// The last `lines` lines of `text`, joined by a line break, its blank lines
/// left out.
pub fn tail_of(text: &str, lines: usize) -> String {
    let kept: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    kept[kept.len().saturating_sub(lines)..].join("\n")
}

/// The environment of a call the daemon serves (S7; step 10a U2, the ONE
/// builder every per-call read and run is made over): the caller's `env`
/// as a process at `dir` (its working directory) sees it, `root`, when
/// given, the project it is in, as `CTXLOOM_ROOT` over any the caller had
/// (as the platform compares names), its temporary directory the caller's
/// ([`caller_temp`]; `temp`, the daemon's, only for a call with no
/// environment).
pub fn caller_env<'a>(
    flavor: os::Flavor,
    temp: &Path,
    (dir, root): (&str, Option<&str>),
    env: impl IntoIterator<Item = (&'a str, &'a str)>,
) -> EnvSnapshot {
    let owned = flavor.env_key(run_ports::ROOT_VARIABLE);
    let mut vars: Vec<(OsString, OsString)> = env
        .into_iter()
        .filter(|(name, _)| root.is_none() || flavor.env_key(name) != owned)
        .map(|(name, value)| (name.into(), value.into()))
        .collect();
    let temp = caller_temp(flavor, &vars, temp);
    if let Some(root) = root {
        vars.push((run_ports::ROOT_VARIABLE.into(), root.into()));
    }
    EnvSnapshot::from_parts(flavor, Ok(PathBuf::from(dir)), temp, vars)
}
/// The temporary directory of a call made in `vars` (owner, 2026-11-18):
/// the CALLER's, as the platform's standard library reads it from an
/// environment (unix: `TMPDIR`, else `/tmp`; Windows: `TMP`, `TEMP`,
/// `USERPROFILE`, else the Windows directory), never the daemon's own. A
/// call that carries no environment at all (the daemon's own work, no
/// caller's) is the daemon's, `own`.
pub fn caller_temp(flavor: os::Flavor, vars: &[(OsString, OsString)], own: &Path) -> PathBuf {
    if vars.is_empty() {
        return own.to_path_buf();
    }
    let var = |name: &str| {
        let key = flavor.env_key(name);
        vars.iter()
            .find(|(found, value)| {
                flavor.env_key(&found.to_string_lossy()) == key && !value.is_empty()
            })
            .map(|(_, value)| PathBuf::from(value))
    };
    match flavor {
        os::Flavor::Unix => var("TMPDIR").unwrap_or_else(|| PathBuf::from("/tmp")),
        os::Flavor::Windows => var("TMP")
            .or_else(|| var("TEMP"))
            .or_else(|| var("USERPROFILE"))
            .or_else(|| var("WINDIR"))
            .unwrap_or_else(|| own.to_path_buf()),
    }
}
/// The wiring of a call the daemon serves: [`caller_env`]'s process, with
/// ctxloom's `home` as its home (the caller's own home stays its own, so her
/// login and settings are found under it, M4).
pub fn caller_wiring<'a>(
    home: &Path,
    temp: &Path,
    place: (&str, Option<&str>),
    env: impl IntoIterator<Item = (&'a str, &'a str)>,
) -> Wiring {
    let snapshot = caller_env(os::host().flavor(), temp, place, env);
    Wiring::at(Arc::new(snapshot), home.to_path_buf())
}

#[cfg(test)]
#[path = "mod.test.rs"]
mod tests;
