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

mod deps;
mod files;
pub(crate) mod flush;
mod foreign;
pub(crate) mod heard;
mod places;
pub(crate) mod runtime;
mod serve;
mod setup_reads;
pub(crate) mod shutdown;
#[cfg(feature = "test-support")]
pub(crate) mod watch;

use std::collections::BTreeMap;
use std::ffi::{OsStr, OsString};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, Instant};

use angzarr_presentation::{render, Catalog, Render};
use clap::{Command, FromArgMatches, Subcommand};
use cli_support::{Exit, ExitClass};
use ctxloom_cli::{shaped, Call, CliError, Failure};
use daemon_ports::{Daemons, Unreached};
use file_system::{Fs, Open};
use os::Mode;
use process_env::EnvSnapshot;
use serde::Serialize;
use sessions::ProjectName;

use crate::{LibError, Wiring};
use files::{instance_lock_path, DaemonFiles};

/// The noun.
pub(crate) const NOUN: &str = "daemon";

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
pub(crate) const HOOK_BOUND: Duration = Duration::from_secs(10);

/// What a delivery is left of [`HOOK_BOUND`] after the longest spawn wait.
pub(crate) const DELIVERY_MARGIN: Duration = Duration::from_secs(2);

/// How long a client waits for a daemon it spawned to answer its health
/// check: what [`HOOK_BOUND`] leaves once a delivery has its margin.
pub(crate) const SPAWN_WAIT: Duration = HOOK_BOUND.saturating_sub(DELIVERY_MARGIN);

/// How long one health check of a daemon may take (a loopback TLS
/// handshake and one call; a stale port refuses at once).
pub(crate) const HEALTH_WAIT: Duration = Duration::from_secs(1);

/// The environment a spawned daemon is given of its spawner's `vars`
/// (D-N45): what it needs to run, named as `flavor` compares names (so
/// Windows' `Path` is `PATH`), and ctxloom's `home` when the spawner set
/// one (its `CTXLOOM_HOME`; with none the daemon serves the account's home,
/// [`daemon_home`]). Nothing else of the spawner's crosses: a child's
/// environment is its launcher's (S6).
fn daemon_env<N: AsRef<OsStr>, V: AsRef<OsStr>>(
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
            run_ports::HOME_VARIABLE.into(),
            home.as_os_str().to_os_string(),
        ));
    }
    env
}

/// The test-support variable moving the daemon's clock on by whole days
/// (step 10b's harness, QUESTIONS 5): passed on to a daemon a command
/// starts, read by its trash clock.
#[cfg(feature = "test-support")]
pub(crate) const CLOCK_OFFSET_VARIABLE: &str = "CTXLOOM_TEST_CLOCK_OFFSET";

/// The test-support variable naming a test's LIFELINE: a lock file the
/// test process holds for its life (the harness's `tests::lifeline`). A
/// daemon given it that can take that lock has outlived its test (one
/// killed by nextest's timeout drops nothing, so nothing else stops it):
/// it ends what it hosts and exits (`serve::Lifeline`). Passed on to a
/// daemon a command starts. Read only under `test-support`.
#[cfg(feature = "test-support")]
pub(crate) const LIFELINE_VARIABLE: &str = "CTXLOOM_TEST_LIFELINE";

/// The test-support variable naming the OS account's home, so a test's
/// temporary home is the account's for the daemon (and a test can tell the
/// account's home from a moved HOME). Read only under `test-support`.
#[cfg(feature = "test-support")]
pub(crate) const ACCOUNT_HOME_VARIABLE: &str = "CTXLOOM_TEST_ACCOUNT_HOME";

/// The OS account's home, as the system keeps it (angzarr-platform's
/// `Os::user_home`: the account database's, never `HOME` or `USERPROFILE`,
/// so a process whose HOME was moved still finds the account's); none when
/// the account has none. One daemon per OS USER: the single-instance lock
/// and the daemon's own home are derived from it.
#[cfg(not(feature = "test-support"))]
pub(crate) fn account_home(_: &EnvSnapshot) -> Option<PathBuf> {
    os::host().user_home().ok()
}

/// Under `test-support` the account's home is the test's
/// ([`ACCOUNT_HOME_VARIABLE`]), else the process's ctxloom home
/// (`CTXLOOM_HOME`), else its HOME (each test's temporary home), never the
/// real account's: no test reaches the real account's lock or files. The
/// ctxloom home stands in before HOME because it is what an engine's hook
/// processes carry (their HOME is the session home): as the account's real
/// home does in a shipped binary, it finds them the daemon that started
/// their engine (slice 4 U7: no run tells its hooks an endpoint of its own).
#[cfg(feature = "test-support")]
pub(crate) fn account_home(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(ACCOUNT_HOME_VARIABLE)
        .or_else(|| env.non_empty(run_ports::HOME_VARIABLE))
        .or_else(|| env.home())
        .map(PathBuf::from)
}

/// The daemon's ctxloom home (M4): `CTXLOOM_HOME` when it was given one
/// (its spawner's, [`daemon_env`]), else the ACCOUNT's home, never its HOME:
/// the daemon serves the account, whoever spawned it.
pub(crate) fn daemon_home(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(run_ports::HOME_VARIABLE)
        .map(PathBuf::from)
        .or_else(|| account_home(env))
}

/// The variable a client passes on as the spawned daemon's idle time, in
/// seconds (the tests' short one).
const IDLE_VARIABLE: &str = "CTXLOOM_DAEMON_IDLE_SECONDS";

#[derive(Subcommand, Debug)]
enum DaemonVerb {
    /// Be the user's daemon
    Serve {
        /// Exit after this many seconds with nothing hosted and nothing to do
        #[arg(long, default_value_t = 600)]
        idle_seconds: u64,
        /// How often to look at the stores, in milliseconds
        #[arg(long, default_value_t = 100)]
        poll_ms: u64,
        /// How long a drain waits for a busy child, in seconds, before it
        /// hands it over anyway, its turn cut
        #[arg(long, default_value_t = daemon_application::HAND_OVER_PERIOD.as_secs())]
        hand_over_seconds: u64,
    },
    /// Say whether a daemon serves, and where
    Status,
    /// Ask the daemon to drain and exit
    Stop {
        /// Stop it even while it hosts live runs, ending them first
        #[arg(long, conflicts_with = "drain")]
        even_if_live: bool,
        /// Hand what it hosts over to the next daemon, which resumes each
        /// child in its own conversation, then exit
        #[arg(long)]
        drain: bool,
    },
    /// Register this project with the daemon, or recheck it
    Resolve,
}

/// The noun, hidden from the help.
pub(crate) fn noun() -> Command {
    shaped(DaemonVerb::augment_subcommands(
        Command::new(NOUN)
            .about("The user's daemon")
            .hide(true)
            .subcommand_required(true),
    ))
}

/// What `status` answers: the daemon as it says itself (`GetDaemon`), or
/// none serving. Its json is the `Daemon`'s proto3 JSON (ruling 146), the
/// empty message when none serves.
struct Status(Option<daemon_proto::v1::Daemon>, Vec<String>);

impl Render for Status {
    fn json(&self) -> impl Serialize + '_ {
        // None serving: the empty message, `{}`.
        self.0.clone().unwrap_or_default()
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        let Some(daemon) = &self.0 else {
            return writeln!(out, "no ctxloom daemon is serving");
        };
        writeln!(
            out,
            "serving at {} (pid {}), ctxloom {}",
            daemon.endpoint, daemon.pid, daemon.version
        )?;
        writeln!(
            out,
            "idle {}s of {}s",
            daemon.idle_seconds, daemon.idle_limit_seconds
        )?;
        if daemon.draining {
            writeln!(
                out,
                "draining: handing what it hosts over to the next daemon"
            )?;
        }
        // What it warns of (OD-52), in this command's catalog's words.
        for warning in &self.1 {
            writeln!(out, "{warning}")?;
        }
        for run in &daemon.runs {
            writeln!(out, "hosting {} ({})", run.harp, run.session)?;
        }
        Ok(())
    }
}

/// What `stop` answers (smoke 2026-11-19): the daemon it stopped, as the
/// stop's answer says it, or none serving; whether it drained first. Its
/// json is the `Daemon`'s proto3 JSON (ruling 146), `{}` for none.
struct Stopped(Option<daemon_proto::v1::Daemon>, bool);

impl Render for Stopped {
    fn json(&self) -> impl Serialize + '_ {
        self.0.clone().unwrap_or_default()
    }

    // DRAFT text (smoke 2026-11-19): `daemon stop` said nothing.
    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        let Some(daemon) = &self.0 else {
            return writeln!(out, "no ctxloom daemon was serving: nothing to stop");
        };
        let done = if self.1 {
            "drained and stopped"
        } else {
            "stopped"
        };
        writeln!(
            out,
            "{done} the ctxloom daemon (pid {}), ctxloom {}",
            daemon.pid, daemon.version
        )
    }
}

/// `warnings`, as `daemon status` says them in text: each its message in
/// `catalog`'s words, then its fix where the catalog has one.
fn warned(warnings: &[daemon_proto::v1::Notice], catalog: &Catalog) -> Vec<String> {
    warnings
        .iter()
        .map(|notice| {
            let warning = run_application::client::report_of(&notice.code, &notice.args);
            let fix =
                run_application::client::report_of(&format!("{}-fix", notice.code), &notice.args);
            match catalog.render(&fix) {
                Ok(fix) => format!("warning: {}\n  fix: {fix}", catalog.message(&warning)),
                Err(_) => format!("warning: {}", catalog.message(&warning)),
            }
        })
        .collect()
}

fn failed(cause: impl angzarr_diagnostic::Diagnostic + 'static) -> Failure {
    Failure::new(cause, Exit::FAILED)
}

fn output(written: std::io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        failed(CliError::Output {
            detail: e.to_string(),
        })
    })
}

/// Run the `daemon` noun's verb.
pub(crate) fn run(
    env: &Arc<EnvSnapshot>,
    stamp: &str,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let matches = call
        .matches
        .subcommand_matches(NOUN)
        .unwrap_or_else(|| unreachable!("the line named {NOUN}"));
    let verb = DaemonVerb::from_arg_matches(matches)
        .unwrap_or_else(|_| unreachable!("the command line parsed {NOUN}"));
    let format = call.format();
    // The daemon's home is the account's (M4), never its HOME; a client's
    // is the one any command resolves.
    let wiring = match verb {
        DaemonVerb::Serve { .. } => daemon_wiring(env)?,
        _ => Wiring::new(Arc::clone(env)).map_err(failed)?,
    };
    let files = files_of(&wiring);
    match verb {
        DaemonVerb::Serve {
            idle_seconds,
            poll_ms,
            hand_over_seconds,
        } => {
            let pace = serve::Pace {
                idle: Duration::from_secs(idle_seconds),
                poll: Duration::from_millis(poll_ms.max(1)),
                hand_over: Duration::from_secs(hand_over_seconds),
            };
            be_the_daemon(&wiring, env, stamp, &files, pace)
        }
        // Clients of the daemon (slice 2 step 6), answered here when none
        // serves (S14): neither starts one.
        // They look at the daemon and change nothing: no hold is delivered
        // on their way in (H3).
        DaemonVerb::Status => {
            // It shows the daemon's warnings itself (OD-52): it hears none.
            let reach = Reach::new(&wiring, stamp).observing().showing_warnings();
            let found = block_on(client_reaching(&wiring, reach).status())?.map_err(called)?;
            let warnings = found
                .as_ref()
                .map(|daemon| warned(&daemon.warnings, catalog))
                .unwrap_or_default();
            output(render(out, format, &Status(found, warnings)))?;
            Ok(Exit::OK)
        }
        DaemonVerb::Stop {
            even_if_live,
            drain,
        } => {
            let reach = Reach::new(&wiring, stamp).observing();
            let client = client_reaching(&wiring, reach);
            // Its warnings (a drain's hand-over among them) are heard from
            // the stop's answer, as every call's (OD-55), and said at once.
            let stopped = match drain {
                // OD-54 (3): handed over within the configured timeout.
                true => block_on(client.drain(None))?,
                false => block_on(client.stop(even_if_live))?,
            }
            .map_err(called)?;
            output(render(out, format, &Stopped(stopped, drain)))?;
            Ok(Exit::OK)
        }
        DaemonVerb::Resolve => {
            let project = resolve(&wiring, env, stamp)?;
            output(render(out, format, &Resolved(project)))?;
            Ok(Exit::OK)
        }
    }
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
fn client_of(
    found: &run_ports::Discovery,
    caller: daemon_proto::v1::Caller,
) -> daemon_client::DaemonClient {
    daemon_client::DaemonClient::new(found, CALL_WAIT)
        .destroying(DESTROY_WAIT)
        .filling(caller)
}

/// The daemon `found` names, as a command calls it: the warnings each of
/// its answers carries heard, to be said as the command ends (OD-55).
fn hearing_client_of(
    found: &run_ports::Discovery,
    caller: daemon_proto::v1::Caller,
) -> daemon_client::DaemonClient {
    client_of(found, caller).hearing(std::sync::Arc::new(heard::hear))
}

/// `future`, on a runtime of its own.
fn block_on<T>(future: impl std::future::Future<Output = T>) -> Result<T, Failure> {
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
fn called(error: daemon_api::CallError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit)
}

/// The stamp this client compares with the daemon's (M3): the binary's,
/// or, under test-support, the one a test names.
#[cfg(feature = "test-support")]
pub(crate) fn own_stamp<'a>(env: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
    env.utf8(STAMP_VARIABLE)
        .filter(|s| !s.is_empty())
        .unwrap_or(stamp)
}

#[cfg(not(feature = "test-support"))]
pub(crate) fn own_stamp<'a>(_: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
    stamp
}

/// The variable a test names a client's stamp by (M3's skew, on demand).
#[cfg(feature = "test-support")]
const STAMP_VARIABLE: &str = "CTXLOOM_TEST_STAMP";

/// M3: the serving daemon `found`, reconciled with this client's stamp
/// `own`: of another version and idle, it is stopped and waited out (the
/// next `ensure` starts this one's); busy, the command is refused.
fn reconciled(
    files: &DaemonFiles,
    found: Option<&run_ports::Discovery>,
    own: &str,
) -> Result<(), Failure> {
    let Some(found) = found else {
        return Ok(());
    };
    // Its calls (`GetDaemon`, `StopDaemon`) carry no caller.
    let outcome = block_on(daemon_api::reconcile(
        &hearing_client_of(found, daemon_proto::v1::Caller::default()),
        own,
        &found.stamp,
    ))?;
    match outcome {
        Ok(daemon_api::Reconciled::Same) => Ok(()),
        Ok(daemon_api::Reconciled::Stopped) => {
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

/// `resolve`: this project registered with the daemon, or rechecked
/// (owner, 2026-11-12), the daemon made sure of first (M3 included).
fn resolve(
    wiring: &Wiring,
    env: &Arc<EnvSnapshot>,
    stamp: &str,
) -> Result<daemon_proto::v1::Project, Failure> {
    let files = files_of(wiring);
    reconciled(
        &files,
        serving(wiring, &files)?.as_ref(),
        own_stamp(env, stamp),
    )?;
    let died = ensure(wiring, env);
    let Some(found) = serving(wiring, &files)? else {
        let detail = match died {
            Some(Died { status, tail }) => format!(
                "no ctxloom daemon could be started: the one started exited ({status}); the end of its log:\n{tail}"
            ),
            None => "no ctxloom daemon could be started".to_string(),
        };
        return Err(failed(LibError::NoRuntime { detail }));
    };
    // It answers the project's name, so it names it (owner ruling
    // 2026-11-14): one nothing has named gets its id at its root.
    let request = wiring
        .naming_registration()
        .map_err(|e| {
            let exit = e.exit();
            Failure::new(e, exit)
        })?
        .unwrap_or_default();
    let caller = caller_of(env, daemon_proto::v1::client::Kind::Cli, stamp);
    block_on(daemon_api::resolve(
        &hearing_client_of(&found, caller),
        request,
    ))?
    .map_err(called)
}

/// What `resolve` answers: the project the daemon named.
struct Resolved(daemon_proto::v1::Project);

#[derive(Serialize)]
struct ResolvedJson<'a> {
    name: &'a str,
    root: &'a str,
    uid: &'a str,
    git_common_dir: &'a str,
    root_commit: &'a str,
}

impl Render for Resolved {
    fn json(&self) -> impl Serialize + '_ {
        let p = &self.0;
        ResolvedJson {
            name: &p.name,
            root: &p.root,
            uid: &p.uid,
            git_common_dir: &p.git_common_dir,
            root_commit: &p.root_commit,
        }
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        writeln!(out, "{} ({})", self.0.name, self.0.root)
    }
}

/// The daemon's wiring: its home [`daemon_home`]'s; refused, as any command
/// with no home is, where there is none.
fn daemon_wiring(env: &Arc<EnvSnapshot>) -> Result<Wiring, Failure> {
    let home = daemon_home(env).ok_or_else(|| {
        failed(LibError::NoHome {
            variable: run_ports::HOME_VARIABLE.to_string(),
        })
    })?;
    Ok(Wiring::at(Arc::clone(env), home).with_user_home(account_home(env)))
}

/// `serve`: this process is the daemon until it exits.
fn be_the_daemon(
    wiring: &Wiring,
    env: &EnvSnapshot,
    stamp: &str,
    files: &DaemonFiles,
    pace: serve::Pace,
) -> Result<Exit, Failure> {
    let say = logger(wiring.fs(), files.log_path());
    let place = serve::Place {
        // The binary's; under test-support a test's own (a daemon of an
        // older or newer version, s4-Q14's hold scenarios).
        stamp: own_stamp(env, stamp).to_string(),
        home: wiring.home().to_path_buf(),
        instance_lock: instance_lock(wiring),
        temp: env.temp_dir().to_path_buf(),
        os: wiring.os_name().to_string(),
        // Her own settings and login are the ACCOUNT's (the daemon has no
        // HOME of its own, M4).
        user_home: wiring.user_home(),
        // Composed as every launch's engines are (the one session-setup
        // path): calling this binary, over her own settings.
        engines: crate::commands::hosted_engines(wiring, &crate::program::running().name),
        // The environment its children's checkouts' git runs in.
        env: Arc::clone(wiring.env()),
    };
    log_panics(wiring.fs(), files.log_path());
    #[cfg(feature = "test-support")]
    let start = env.utf8(START_VARIABLE).map(str::to_string);
    let fs = wiring.fs();
    // On a thread of its own, as each hosted session is: the process's main
    // thread is the platform's to size (1 MiB on Windows), and the daemon's
    // first round outgrew it in an unoptimized build (CI, 2026-11-19).
    let serving = std::thread::Builder::new()
        .name("ctxloom-daemon".to_string())
        .stack_size(DAEMON_STACK)
        .spawn(move || {
            #[cfg(feature = "test-support")]
            at_start(start.as_deref());
            serve::serve(fs, place, pace, say)
        });
    let served = match serving {
        // A panic is in the log already ([`log_panics`]).
        Ok(serving) => serving
            .join()
            .unwrap_or_else(|_| Err(serve::Unserved::Failed("the daemon panicked".to_string()))),
        Err(e) => Err(serve::Unserved::Failed(format!(
            "the daemon's thread could not be started: {e}"
        ))),
    };
    served_exit(served)
}

/// The stack the daemon serves on (a hosted session's is the same size).
pub(crate) const DAEMON_STACK: usize = 16 * 1024 * 1024;

/// Every panic of this daemon process is a line of its log at `path`, then
/// told as before (to its stderr): a daemon that dies by panic says why
/// even when its stderr is not the log (`daemon serve` run by hand).
fn log_panics(fs: Arc<dyn Fs>, path: PathBuf) {
    let say = std::sync::Mutex::new(logger(fs, path));
    let told = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        if let Ok(mut say) = say.lock() {
            say(&format!("the daemon panicked: {info}"));
        }
        told(info);
    }));
}

/// The test-support variable that makes the daemon's start die or deepen
/// (CI on Windows, 2026-11-19): `panic` panics on the daemon's thread before
/// it serves; `stack:<MiB>` uses that much of its stack first (more than
/// the thread has overflows it). Passed on to a daemon a command starts.
#[cfg(feature = "test-support")]
pub(crate) const START_VARIABLE: &str = "CTXLOOM_TEST_DAEMON_START";

/// What [`START_VARIABLE`] says to do at the daemon's start.
#[cfg(feature = "test-support")]
fn at_start(switch: Option<&str>) {
    match switch {
        Some("panic") => panic!("the test's daemon panics at its start"),
        Some(other) => {
            if let Some(mib) = other
                .strip_prefix("stack:")
                .and_then(|m| m.parse::<usize>().ok())
            {
                let base = 0u8;
                std::hint::black_box(use_stack(std::ptr::addr_of!(base) as usize, mib << 20));
            }
        }
        None => {}
    }
}

/// Recurse until the stack below `base` is `bytes` deep (each frame holding
/// a page the optimizer cannot drop).
#[cfg(feature = "test-support")]
fn use_stack(base: usize, bytes: usize) -> u8 {
    let page = std::hint::black_box([1u8; 4096]);
    if base.abs_diff(std::ptr::addr_of!(page) as usize) >= bytes {
        return page[0];
    }
    use_stack(base, bytes).wrapping_add(std::hint::black_box(page)[7])
}

/// `daemon serve`'s exit: a daemon that lost the single-instance lock to
/// another declines (20: a lock another process holds), which its spawner
/// reads as another daemon to wait for ([`spawn_waits_on`]); one that could
/// not serve failed (10).
fn served_exit(served: Result<(), serve::Unserved>) -> Result<Exit, Failure> {
    match served {
        Ok(()) => Ok(Exit::OK),
        Err(serve::Unserved::Held { lock }) => {
            Err(Failure::of(LibError::DaemonInstanceHeld { lock }))
        }
        Err(serve::Unserved::Failed(detail)) => Err(failed(LibError::NoRuntime { detail })),
    }
}

/// Whether a client waits on for a daemon to advertise itself once the one
/// it spawned has exited with the status `code` (`None`: a signal's): only
/// when that one lost the single-instance lock to another daemon, which is
/// the one to wait for. Any other exit means no daemon is coming of this
/// spawn, so the client stops waiting (the daemon cluster's cap, owner
/// ruling 2026-11-18: it waited out all of [`SPAWN_WAIT`] for a daemon that
/// had died at once).
fn spawn_waits_on(code: Option<i32>) -> bool {
    let held = LibError::DaemonInstanceHeld {
        lock: String::new(),
    };
    code == Some(i32::from(held.exit().code(false)))
}

/// The single-instance lock of `wiring`'s ACCOUNT ([`instance_lock_path`]
/// under [`account_home`], its ctxloom home where the account has none), a
/// test's override first.
pub(crate) fn instance_lock(wiring: &Wiring) -> PathBuf {
    let account = account_home(wiring.env());
    let base = account.as_deref().unwrap_or(wiring.home());
    instance_lock_path(base, instance_override(wiring.env()).as_deref())
}

/// The directory a test names for the single-instance lock
/// ([`files::INSTANCE_DIR_VARIABLE`]); compiled out of the shipped binary.
#[cfg(feature = "test-support")]
fn instance_override(env: &EnvSnapshot) -> Option<PathBuf> {
    env.non_empty(files::INSTANCE_DIR_VARIABLE)
        .map(PathBuf::from)
}

/// The shipped binary has no override: the account's place alone.
#[cfg(not(feature = "test-support"))]
fn instance_override(_: &EnvSnapshot) -> Option<PathBuf> {
    None
}

/// The daemon files of `wiring`'s home, its account's single-instance lock.
fn files_of(wiring: &Wiring) -> DaemonFiles {
    DaemonFiles::with_instance(wiring.fs(), wiring.home(), instance_lock(wiring))
}

/// The live daemon `files` advertise, if one serves; refused when it serves
/// another ctxloom home than `wiring`'s (one daemon per OS user, so this
/// command is never given a second).
fn serving(wiring: &Wiring, files: &DaemonFiles) -> Result<Option<run_ports::Discovery>, Failure> {
    serving_here(wiring, files).map_err(|refused| Failure::new(refused, Exit::REFUSED))
}

/// [`serving`]'s answer, its refusal the wiring's own.
fn serving_here(
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
pub(crate) struct Reach<'a> {
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
    pub(crate) fn new(wiring: &'a Wiring, stamp: &str) -> Reach<'a> {
        Reach {
            wiring,
            stamp: own_stamp(wiring.env(), stamp).to_string(),
            never_stops: false,
            flushes: true,
            hears: true,
        }
    }

    /// The stamp this client compares with the daemon's (M3).
    pub(crate) fn stamp(&self) -> &str {
        &self.stamp
    }

    /// This reach as the `daemon` noun's own verbs make it (`status`,
    /// `stop`, `resolve`): it delivers no hold on its way in.
    pub(crate) fn observing(self) -> Reach<'a> {
        Reach {
            flushes: false,
            ..self
        }
    }

    /// This reach as `daemon status` makes it: the daemon's warnings are
    /// its answer, not heard to be said on stderr.
    pub(crate) fn showing_warnings(self) -> Reach<'a> {
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
    /// [`daemon_api::reconcile_session`]): an idle daemon of an older
    /// version stopped and replaced by this binary's, any other daemon of
    /// another stamp a typed mismatch, and nothing a daemon hosts ended.
    pub(crate) fn never_stopping(self) -> Reach<'a> {
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
fn under_home(flavor: os::Flavor, below: &Path) -> String {
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
fn declined(refused: impl angzarr_diagnostic::Diagnostic + Send + Sync + 'static) -> Unreached {
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
                daemon_api::reconcile_session(&api, &self.stamp, &found.stamp).await
            } else {
                daemon_api::reconcile(&api, &self.stamp, &found.stamp).await
            };
            match reconciled {
                Ok(daemon_api::Reconciled::Same) => return Ok(self.reached(&found)),
                Ok(daemon_api::Reconciled::Stopped) => {
                    let deadline = Instant::now() + STOPPED_WAIT;
                    while Instant::now() < deadline && files.discovery().is_some_and(|d| live(&d)) {
                        std::thread::sleep(Duration::from_millis(20));
                    }
                }
                Err(daemon_api::SkewError::Call(_)) => {}
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
pub(crate) fn env_vars(env: &EnvSnapshot) -> daemon_proto::v1::EnvVars {
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
pub(crate) fn calling_client(
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
pub(crate) fn caller_of(
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
pub(crate) fn client<'a>(wiring: &'a Wiring, stamp: &str) -> daemon_api::Client<Reach<'a>> {
    client_reaching(wiring, Reach::new(wiring, stamp))
}

/// [`client`], reaching the daemon through `reach`.
fn client_reaching<'a>(wiring: &'a Wiring, reach: Reach<'a>) -> daemon_api::Client<Reach<'a>> {
    match wiring.registration() {
        Ok(Some(project)) => client_through(wiring, reach, Some(project)),
        Ok(None) | Err(LibError::ProjectUnnamed { .. }) => {
            client_through(wiring, reach, None).unnamed()
        }
        Err(_) => client_through(wiring, reach, None),
    }
}

/// A client of the daemon for `registration`'s project (none: outside one).
fn client_for<'a>(
    wiring: &'a Wiring,
    stamp: &str,
    registration: Option<daemon_proto::v1::ResolveProjectRequest>,
) -> daemon_api::Client<Reach<'a>> {
    client_through(wiring, Reach::new(wiring, stamp), registration)
}

/// [`client_for`], reaching the daemon through `reach`.
fn client_through<'a>(
    wiring: &'a Wiring,
    reach: Reach<'a>,
    registration: Option<daemon_proto::v1::ResolveProjectRequest>,
) -> daemon_api::Client<Reach<'a>> {
    let caller = caller_of(
        wiring.env(),
        daemon_proto::v1::client::Kind::Cli,
        reach.stamp(),
    );
    retrying(
        wiring,
        daemon_api::Client::new(reach, registration, caller).with_request_ids(Box::new(request_id)),
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
fn retrying<'a>(
    wiring: &Wiring,
    client: daemon_api::Client<Reach<'a>>,
) -> daemon_api::Client<Reach<'a>> {
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
fn run_before_retry(program: &std::ffi::OsStr) {
    let ran = os::process::spawn_tree(std::process::Command::new(program))
        .and_then(|mut tree| tree.wait());
    if let Err(error) = ran {
        eprintln!("ctxloom: {BEFORE_RETRY} {program:?} did not run: {error}");
    }
}

/// The shipped client always retries once (S8).
#[cfg(not(feature = "test-support"))]
fn retrying<'a>(
    _: &Wiring,
    client: daemon_api::Client<Reach<'a>>,
) -> daemon_api::Client<Reach<'a>> {
    client
}

/// Where this process's library resource names are rooted (OD-40, OQ-2,
/// OQ-3): its project as the daemon's `ResolveProject` names it
/// (`projects/{harp}`; the daemon reached, started if none serves), or
/// `users/me` outside a project. A project nothing has named has its id
/// minted at its root, unless `minting` is a dry run's `Never`, which
/// refuses it (owner ruling 2026-11-14). A call that fails fails the command
/// in its own class.
pub(crate) fn library_parent(
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
/// nothing to resolve (`daemon_api::Client::named_parent`), so one that
/// cannot be started fails the dry run as it fails every client's command.
pub(crate) fn library_parent_if_named(
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
pub(crate) fn preview_client<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api::Client<Reach<'a>>, Failure> {
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
pub(crate) fn profile_loadout(
    wiring: &Wiring,
    stamp: &str,
    profile: &str,
) -> Result<sessions::SessionLoadout, Failure> {
    use daemon_api::LibraryReads;
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

pub(crate) fn library_client<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api::Client<Reach<'a>>, Failure> {
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
pub(crate) fn home_client<'a>(wiring: &'a Wiring, stamp: &str) -> daemon_api::Client<Reach<'a>> {
    client_for(wiring, stamp, None)
}

/// A client call's answer, or the failure it ends the command in, in its
/// own class.
pub(crate) fn answered<T>(
    call: impl std::future::Future<Output = Result<T, daemon_api::CallError>>,
) -> Result<T, Failure> {
    block_on(call)?.map_err(called)
}

/// The parent the daemon names for `registration` (`users/me` for none).
fn resolved_parent(
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
/// project it is refused (exit 20).
pub(crate) fn child_caller<'a>(
    wiring: &'a Wiring,
    stamp: &str,
) -> Result<daemon_api::Caller<Reach<'a>>, Failure> {
    let parent = library_parent(wiring, stamp, crate::Minting::AtRoot)?;
    if ProjectName::parse(&parent).is_err() {
        let cwd = wiring
            .env()
            .cwd()
            .map(|cwd| cwd.display().to_string())
            .unwrap_or_default();
        return Err(Failure::new(
            daemon_api::ApiRejection::ProjectUnknown { cwd },
            Exit::REFUSED,
        ));
    }
    daemon_api::Caller::user(
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
pub(crate) fn trust_ask(wiring: &Wiring, stamp: &str) -> library_application::TrustAsk {
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
/// project is refused, `daemon-api-project-unknown`.
pub(crate) fn project_resolver(wiring: &Wiring, base: PathBuf) -> Arc<daemon_api::ProjectResolver> {
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
            daemon_api::CallError::Refused(daemon_api::Rejection::Api(
                daemon_api::ApiRejection::ProjectUnknown {
                    cwd: at.display().to_string(),
                },
            ))
        };
        let wiring = Wiring::at(Arc::new(there), home.clone()).with_user_home(user_home.clone());
        match wiring.library_registration(crate::Minting::AtRoot) {
            Ok(Some(registration)) => Ok(registration),
            Ok(None) => Err(unknown()),
            Err(e) => Err(daemon_api::CallError::Call(daemon_ports::Called::Failed {
                code: 0,
                detail: angzarr_diagnostic::Diagnostic::code(&e).to_string(),
            })),
        }
    })
}

/// `path` with its `.` and `..` taken out by their words alone: a
/// project's directory named `../api` is the sibling `api`, never found by a
/// walk up from `<project>/..` to the project itself.
pub(crate) fn lexical(path: &Path) -> PathBuf {
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
pub(crate) fn launch_locks(wiring: &Wiring) -> Arc<dyn run_ports::RunLocks> {
    Arc::new(files_of(wiring).launch_locks())
}

/// A write's request id (S8): a fresh UUIDv7, unique per call.
pub(crate) fn request_id() -> String {
    uuid::Uuid::now_v7().to_string()
}

/// A log line writer: each line appended with its time to `path`, opened
/// once, here, as the daemon starts (its directory made first), through the
/// file-system seam, and written through that handle for the daemon's life:
/// never reopened or made again by its path (owner, 2026-11-17), so a line
/// said once its home is gone (the user deleted her ctxloom home, j000100's
/// exit) makes nothing there. A log that cannot be opened says nothing.
/// Nothing it is given holds a secret.
fn logger(fs: Arc<dyn Fs>, path: PathBuf) -> Box<dyn FnMut(&str) + Send> {
    if let Some(dir) = path.parent() {
        let _ = fs.create_dir_all(dir, Mode::from_bits(0o700));
    }
    let mut file = fs.open(&path, Open::Append(Mode::from_bits(0o600))).ok();
    Box::new(move |line: &str| {
        if let Some(file) = file.as_mut() {
            let seconds = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map_or(0, |d| d.as_secs());
            let _ = writeln!(file, "{seconds} {line}");
        }
    })
}

/// Whether the daemon `found` names is live (T16, M11): it answers the
/// standard health check over the certificate it advertised, pinned, within
/// [`HEALTH_WAIT`]. The single-instance lock says nothing of it.
fn live(found: &run_ports::Discovery) -> bool {
    hook_sink_grpc::serving(&found.endpoint, &found.cert, HEALTH_WAIT)
}

/// The daemon a HOOK delivers to (S16, slice 4 §3.8 (ii)): the live one of
/// this home its discovery file names, else one spawned and waited for, at
/// most `wait`; none when none serves this home within it (none could be
/// started, or the one serving serves another home).
pub(crate) fn hook_daemon(env: &Arc<EnvSnapshot>, wait: Duration) -> Option<run_ports::Discovery> {
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
pub(crate) fn hook_daemon_unprobed(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let found = files_of(&wiring).discovery()?;
    (found.home == wiring.home().display().to_string()).then_some(found)
}

/// `discovery` published as `wiring`'s home's daemon's, for a test.
#[cfg(test)]
pub(crate) fn publish_for_test(wiring: &Wiring, discovery: &run_ports::Discovery) {
    files_of(wiring).publish(discovery).unwrap();
}

/// The daemon a hook's library read asks when it may start none (its
/// environment names an endpoint, OD-13): the live one of this home its
/// discovery file names, else none.
pub(crate) fn hook_daemon_serving(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let files = files_of(&wiring);
    serving(&wiring, &files).ok().flatten()
}

/// Whether this executable may spawn itself as the daemon: a unit test's is
/// its harness, never a ctxloom binary (spawned, it would run its tests
/// detached), so it never does.
#[cfg(not(test))]
const SPAWNS_ITSELF: bool = true;
#[cfg(test)]
const SPAWNS_ITSELF: bool = false;

/// Make sure the user's daemon serves: spawn one when none is live, and
/// wait, within [`SPAWN_WAIT`], for a live one to advertise itself (the one
/// spawned, or another client's, whichever took the single-instance lock). A stale file
/// is left for the new daemon to publish over. A daemon that cannot be
/// spawned is not this command's failure (what it recorded stands; the next
/// client spawns one).
fn ensure(wiring: &Wiring, env: &EnvSnapshot) -> Option<Died> {
    ensure_within(wiring, env, SPAWN_WAIT)
}

/// [`ensure`], waiting at most `wait` for a live daemon (a hook's is what
/// its bound leaves); how the daemon it spawned died, when it exited before
/// it served and no other is coming.
fn ensure_within(wiring: &Wiring, env: &EnvSnapshot, wait: Duration) -> Option<Died> {
    let files = files_of(wiring);
    // A live daemon, this home's or (refused before the command ran)
    // another's: never a second.
    if files.discovery().is_some_and(|found| live(&found)) {
        return None;
    }
    if !SPAWNS_ITSELF {
        return None;
    }
    let Ok(exe) = std::env::current_exe() else {
        return None;
    };
    let mut command = os::process::Detach::new(exe);
    command.args(["daemon", "serve"]);
    if let Some(idle) = env.utf8(IDLE_VARIABLE) {
        command.args(["--idle-seconds", idle]);
    }
    // The daemon's own environment is what it needs to run, nothing more: a
    // child's is its launcher's (S6, D-N45), so no spawner's credential
    // lives in the daemon.
    let ctxloom_home = env.non_empty(run_ports::HOME_VARIABLE).map(PathBuf::from);
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
    // A test's clock offset (step 10b, the trash's 7 days) is the daemon's
    // clock's.
    #[cfg(feature = "test-support")]
    if let Some(days) = env.non_empty(CLOCK_OFFSET_VARIABLE) {
        kept.push((CLOCK_OFFSET_VARIABLE.into(), days.to_os_string()));
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
pub(crate) struct Died {
    pub(crate) status: String,
    pub(crate) tail: String,
}

/// The lines of a daemon's log [`Died`] holds.
const DIED_TAIL: usize = 12;

/// The last `lines` lines of `text`, joined by a line break, its blank lines
/// left out.
fn tail_of(text: &str, lines: usize) -> String {
    let kept: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    kept[kept.len().saturating_sub(lines)..].join("\n")
}

/// The variables that say where the user's own engine settings are (her
/// claude config dir, her XDG bases): a launcher sends its own with the
/// launch (S7), and the launch reads her settings there (T21), never from
/// the daemon's environment; none of them is the child's.
pub(crate) const SETTINGS_PLACES: [&str; 4] = [
    "CLAUDE_CONFIG_DIR",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_STATE_HOME",
];

/// The places of [`SETTINGS_PLACES`] `caller` sets (keyed as the platform
/// compares names).
fn settings_places(caller: &BTreeMap<String, String>) -> BTreeMap<String, String> {
    SETTINGS_PLACES
        .iter()
        .filter_map(|name| {
            caller
                .get(*name)
                .filter(|value| !value.is_empty())
                .map(|value| ((*name).to_string(), value.clone()))
        })
        .collect()
}

/// The engines a hosted child's launch is set up and started with (T21):
/// composed as every launch's are (`oneshot::launch_engines`, calling
/// `program`), over the user's own settings read NOW, where her launcher
/// finds them (`launcher`, its [`SETTINGS_PLACES`]) under her home
/// (`user_home`, the account's), with ctxloom's `home`.
pub(crate) fn launch_engines_for(
    home: &Path,
    user_home: Option<&Path>,
    temp: &Path,
    launcher: &BTreeMap<String, String>,
    program: &str,
) -> crate::oneshot::LaunchEngines {
    let flavor = os::host().flavor();
    let mut vars: Vec<(OsString, OsString)> = launcher
        .iter()
        .filter(|(name, _)| SETTINGS_PLACES.contains(&name.as_str()))
        .map(|(name, value)| (name.into(), value.into()))
        .collect();
    if let Some(user_home) = user_home {
        vars.push((
            flavor.home_var().into(),
            user_home.as_os_str().to_os_string(),
        ));
    }
    let snapshot =
        EnvSnapshot::from_parts(flavor, Ok(home.to_path_buf()), temp.to_path_buf(), vars);
    let wiring = Wiring::at(Arc::new(snapshot), home.to_path_buf())
        .with_user_home(user_home.map(Path::to_path_buf));
    crate::oneshot::launch_engines(&wiring, program)
}

/// The user's git identity in the child's project at `project_dir` (owner
/// ruling 112), asked of git as a CLI run asks it: in her own environment,
/// the one her launcher sent (`env`) with her home (`user_home`, the
/// account's) as `HOME`. None when git knows neither part.
pub(crate) fn child_git_user(
    temp: &Path,
    user_home: Option<&Path>,
    project_dir: &str,
    env: &BTreeMap<String, String>,
) -> Option<run_ports::GitUser> {
    let flavor = os::host().flavor();
    let home_var = flavor.home_var();
    let mut vars: Vec<(OsString, OsString)> = env
        .iter()
        .filter(|(name, _)| flavor.env_key(name) != flavor.env_key(home_var))
        .map(|(name, value)| (name.into(), value.into()))
        .collect();
    let home = user_home?;
    vars.push((home_var.into(), home.as_os_str().to_os_string()));
    let snapshot = EnvSnapshot::from_parts(
        flavor,
        Ok(PathBuf::from(project_dir)),
        temp.to_path_buf(),
        vars,
    );
    let wiring = Wiring::new(Arc::new(snapshot)).ok()?;
    crate::oneshot::git_user(&wiring, Path::new(project_dir))
}

/// The environment of a call the daemon serves (S7; step 10a U2, the ONE
/// builder every per-call read and run is made over): the caller's `env`
/// as a process at `dir` (its working directory) sees it, `root`, when
/// given, the project it is in, as `CTXLOOM_ROOT` over any the caller had
/// (as the platform compares names), its temporary directory the caller's
/// ([`caller_temp`]; `temp`, the daemon's, only for a call with no
/// environment).
pub(crate) fn caller_env<'a>(
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
pub(crate) fn caller_temp(
    flavor: os::Flavor,
    vars: &[(OsString, OsString)],
    own: &Path,
) -> PathBuf {
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
pub(crate) fn caller_wiring<'a>(
    home: &Path,
    temp: &Path,
    place: (&str, Option<&str>),
    env: impl IntoIterator<Item = (&'a str, &'a str)>,
) -> Wiring {
    let snapshot = caller_env(os::host().flavor(), temp, place, env);
    Wiring::at(Arc::new(snapshot), home.to_path_buf())
}

/// The plan of a child of the project at `project_dir` (D18, D-N46): that
/// project's dry run, for `agent` (empty: its default) on the child's
/// engine, read as a
/// process in that project would, with `env` (the environment its launcher
/// sent, S6) and ctxloom's `home`. Where it cannot be planned, the error's
/// code.
pub(crate) fn child_plan(
    home: &Path,
    temp: &Path,
    engine: &str,
    agent: &str,
    project_dir: &str,
    env: &BTreeMap<String, String>,
    trusted: bool,
) -> Result<library_application::Previewed, String> {
    let library = child_library(home, temp, project_dir, env)?;
    let request = library_application::RunRequest {
        agent: agent.to_string(),
        engine: engine.to_string(),
        ..library_application::RunRequest::default()
    };
    let flags = library_application::Flags {
        trusted,
        ..library_application::Flags::default()
    };
    library
        .dry_run(&flags, &request)
        .result
        .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())
}

/// The root of the session output folders of the project at
/// `project_dir`, as a ctxloom process there reads its config with `env`
/// (step 8 U2b: a child launched into it from another project keeps its
/// outputs under it): `output.dir` against the project's root, its default
/// where the config does not say or does not read, as a hosted run's is.
pub(crate) fn child_output_root(
    home: &Path,
    temp: &Path,
    project_dir: &str,
    env: &BTreeMap<String, String>,
) -> Result<String, String> {
    let library = child_library(home, temp, project_dir, env)?;
    let written = library
        .run_settings(&library_application::Flags::default())
        .map_or_else(
            || library_application::OUTPUT_DIR_DEFAULT.to_string(),
            |settings| settings.output_dir,
        );
    let root = crate::oneshot::output_root(Path::new(project_dir), &written);
    Ok(root.display().to_string())
}

/// The library of the project at `project_dir`, as a ctxloom process
/// there would build it: `env` with the project as its root and the
/// daemon's home as its home.
fn child_library(
    home: &Path,
    temp: &Path,
    project_dir: &str,
    env: &BTreeMap<String, String>,
) -> Result<library_application::Library, String> {
    let wiring = caller_wiring(
        home,
        temp,
        (project_dir, Some(project_dir)),
        env.iter()
            .map(|(name, value)| (name.as_str(), value.as_str())),
    );
    let code = |e: &dyn angzarr_diagnostic::Diagnostic| e.code().to_string();
    // The remotes too, as every run's library has them (a hosted run's,
    // `places`' reads): a profile naming an installed remote bundle resolves.
    let deps: std::rc::Rc<dyn deps_ports::RemoteLibrary> =
        std::rc::Rc::new(crate::commands::build_deps(&wiring).map_err(|e| code(&e))?);
    crate::library::build(&wiring, Some(deps)).map_err(|e| code(&e))
}

#[cfg(test)]
#[path = "mod.test.rs"]
mod tests;
