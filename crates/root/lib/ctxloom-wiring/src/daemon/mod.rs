//! The daemon, server half: what `daemon serve` hosts (its listener, its
//! kernel, its hosted runs and the per-call wirings), beside the
//! [`client`] half every other command reaches it through.

mod client;
pub(crate) use client::*;

mod deps;
pub(crate) mod files;
mod foreign;
mod imports;
pub(crate) use imports::EdgeDispatch;
mod places;
pub(crate) mod runtime;
mod serve;
mod setup_reads;
#[cfg(feature = "test-support")]
pub(crate) mod watch;

use std::collections::BTreeMap;
use std::ffi::OsString;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use cli_support::Exit;
use ctxloom_cli::Failure;
use file_system::{Fs, Open};
use os::Mode;
use process_env::EnvSnapshot;

use crate::{LibError, Wiring};
use client::failed;
use files::DaemonFiles;

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
    let code = |e: &dyn coded_diagnostic::Diagnostic| e.code().to_string();
    crate::library::build(&wiring).map_err(|e| code(&e))
}

#[cfg(test)]
#[path = "mod.test.rs"]
mod tests;
