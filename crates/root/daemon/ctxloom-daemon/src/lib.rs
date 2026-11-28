//! The daemon: the store's one writer. Its composition (the store, the
//! kernel, the scheduler, the listener, the hosted runs and terminals) is
//! served by `ctxloom daemon serve`; the binary hands it to the command
//! line ([`entry`]), whose wiring (`ctxloom-wiring`) is a client of it and
//! opens no store.

mod core_link;
pub use core_link::{
    AlarmConsumer, CoreStores, InProcessCore, Projectors, SharedCore, SystemClock, Unclosed,
    ALARM_CONSUMERS,
};
mod deps;
mod foreign;
mod heartbeat;
mod hosted_oneshots;
mod hosted_terminals;
mod imports;
mod launch_plans;
mod project_secrets;
pub use imports::EdgeDispatch;
mod places;
mod scheduling;
pub use scheduling::SchedulerDues;
#[cfg(feature = "test-support")]
mod library_events;
#[cfg(feature = "test-support")]
mod record;
#[cfg(feature = "test-support")]
mod seed;
mod serve;
mod setup_reads;
#[cfg(feature = "test-support")]
mod store_tests;

use std::collections::BTreeMap;
use std::ffi::OsString;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use ::process_env::EnvSnapshot;
use cli_support::Exit;
use cli_support::Failure;
use ctxloom_compose::daemon::files::DaemonFiles;
use file_system::{Fs, Open};
use os::Mode;

// What the CLI's wiring exposes of the daemon (its client half, the shared
// files) and of itself: the server half builds on them as its own.
use ctxloom_compose::daemon::{
    account_home, caller_env, caller_wiring, clock_offset, daemon_home, drain, failed, files,
    files_of, flush, instance_lock, library_parent_if_named, own_stamp, runtime, DaemonEntry, Pace,
    StoreTests, DAEMON_STACK,
};
#[cfg(feature = "test-support")]
use ctxloom_compose::daemon::{LIFELINE_VARIABLE, START_VARIABLE};
#[cfg(test)]
use ctxloom_compose::fixtures;
#[cfg(test)]
use ctxloom_compose::run_id;
#[cfg(feature = "test-support")]
use ctxloom_compose::{checkout_harp, checkout_tree, Minting, SeedArgs, TestError};
use ctxloom_compose::{fresh_project_harps, registry, session_name, LibError, Wiring};

/// The daemon's composition as the command line takes it (handed in by the
/// binary): `daemon serve` and, under `test-support`, the hidden commands that read or write the store.
pub fn entry() -> DaemonEntry {
    DaemonEntry {
        serve,
        #[cfg(feature = "test-support")]
        store_tests: StoreTests::of(store_tests::run),
        #[cfg(not(feature = "test-support"))]
        store_tests: StoreTests::none(),
    }
}

/// Every catalog the store and the kernel host add to the binary's.
pub fn catalog_sources() -> Vec<presentation::Source> {
    vec![
        ctxloom_store_sqlite::EN_US,
        ctxloom_store_sqlite::OPENER_EN_US,
        daemon_api::EN_US,
    ]
}

/// One value of every diagnostic this crate's adapters raise, for the
/// binary's catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    ctxloom_store_sqlite::diagnostic_samples()
}

/// `daemon serve`: be the user's daemon until it exits.
pub fn serve(env: &Arc<EnvSnapshot>, stamp: &str, pace: Pace) -> Result<Exit, Failure> {
    let wiring = daemon_wiring(env)?;
    let files = files_of(&wiring);
    be_the_daemon(&wiring, env, stamp, &files, pace)
}

/// A project's store from before the one store (T17, S48), as the daemon's
/// resolution of the project refuses it: nothing is read from it or imported.
pub(crate) struct LegacyStores {
    pub(crate) stores: ctxloom_store_sqlite::Stores,
}

impl daemon_api::LegacyStores for LegacyStores {
    fn held(&self, project_id: &str) -> Option<String> {
        match self.stores.refuse_per_project(project_id) {
            Err(ctxloom_store_sqlite::StoreOpenError::PerProjectStore { path }) => Some(path),
            _ => None,
        }
    }
}

/// The engines the daemon hosts a child on (T18), each its `Engine` beside
/// its `ChildEngine`, by name: claude, then opencode (its ACP child). The
/// launcher resolves a child's engine here, never by naming an adapter.
/// They are composed as EVERY launch's are (`oneshot::launch_engines`, the
/// one session-setup path): calling `program`, over `wiring`'s user's own
/// settings.
pub fn hosted_engines(wiring: &Wiring, program: &str) -> Vec<daemon_application::HostedEngine> {
    let ctxloom_compose::oneshot::LaunchEngines { claude, opencode } =
        ctxloom_compose::oneshot::launch_engines(wiring, program);
    // Each launch composed anew (T21): her settings read at that launch,
    // where her launcher finds them, never once at the daemon's start.
    let home = wiring.home().to_path_buf();
    let user_home = wiring.user_home();
    let temp = wiring.env().temp_dir().to_path_buf();
    let program = program.to_string();
    let for_launch = move |claude_side: bool| -> daemon_application::ForLaunch {
        let (home, user_home, temp, program) = (
            home.clone(),
            user_home.clone(),
            temp.clone(),
            program.clone(),
        );
        Arc::new(
            move |launcher: &BTreeMap<String, String>,
                  launched: Option<&daemon_application::Program>| {
                // The launcher's program, else the daemon's own (owner,
                // 2026-11-18).
                let program = launched.map_or(program.as_str(), |launched| launched.name.as_str());
                let engines =
                    launch_engines_for(&home, user_home.as_deref(), &temp, launcher, program);
                match claude_side {
                    true => (
                        engines.claude.clone() as Arc<dyn run_ports::Engine>,
                        engines.claude as Arc<dyn run_ports::ChildEngine>,
                    ),
                    false => (
                        engines.opencode.clone() as Arc<dyn run_ports::Engine>,
                        engines.opencode as Arc<dyn run_ports::ChildEngine>,
                    ),
                }
            },
        )
    };
    vec![
        daemon_application::HostedEngine {
            engine: claude.clone(),
            child: claude,
            transcripts: Arc::new(engine_claude::transcripts_of),
            for_launch: Some(for_launch(true)),
        },
        daemon_application::HostedEngine {
            engine: opencode.clone(),
            child: opencode,
            transcripts: Arc::new(|_| None),
            for_launch: Some(for_launch(false)),
        },
    ]
}

/// The daemon's wiring: its home [`daemon_home`]'s; refused, as any command
/// with no home is, where there is none.
pub(crate) fn daemon_wiring(env: &Arc<EnvSnapshot>) -> Result<Wiring, Failure> {
    let home = daemon_home(env).ok_or_else(|| {
        failed(LibError::NoHome {
            variable: run_ports::DAEMON_ROOT_VARIABLE.to_string(),
        })
    })?;
    Ok(Wiring::at(Arc::clone(env), home).with_user_home(account_home(env)))
}
/// `serve`: this process is the daemon until it exits.
pub(crate) fn be_the_daemon(
    wiring: &Wiring,
    env: &EnvSnapshot,
    stamp: &str,
    files: &DaemonFiles,
    pace: Pace,
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
        engines: hosted_engines(wiring, &ctxloom_compose::program::running().name),
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
/// not serve failed (32).
fn served_exit(served: Result<(), serve::Unserved>) -> Result<Exit, Failure> {
    match served {
        Ok(()) => Ok(Exit::OK),
        Err(serve::Unserved::Held { lock }) => {
            Err(Failure::of(LibError::DaemonInstanceHeld { lock }))
        }
        Err(serve::Unserved::Failed(detail)) => Err(failed(LibError::NoRuntime { detail })),
    }
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
) -> ctxloom_compose::oneshot::LaunchEngines {
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
    ctxloom_compose::oneshot::launch_engines(&wiring, program)
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
    ctxloom_compose::oneshot::git_user(&wiring, Path::new(project_dir))
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
        .map_err(|e| coded_diagnostic::Diagnostic::code(&e).to_string())
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
    let root = ctxloom_compose::oneshot::output_root(Path::new(project_dir), &written);
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
    let code = |e: &dyn coded_diagnostic::Diagnostic| e.code().to_string();
    ctxloom_compose::library_build::build(&wiring).map_err(|e| code(&e))
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod capture_tests;
#[cfg(test)]
#[path = "flush.test.rs"]
mod flush_tests;
#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
