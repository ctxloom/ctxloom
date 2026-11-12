//! The user daemon (D18) and the orchestrator's commands, as the hidden
//! `ctxloom daemon` noun:
//!
//! - `serve`: be the daemon ([`serve`]); a client spawns it ([`ensure`]);
//! - `status`, `stop`: whether one serves, and ask it to drain and exit;
//! - `send`, `close`, `clear`, `launch`: the orchestrator's commands as the
//!   USER sends them (D25: the human acts through the CLI as the user), each
//!   recorded in the project's store in this process (the sessions use
//!   cases), then the daemon made sure of, which hears them through the
//!   store (its plan reads owed state, D-N27). No user credential crosses the
//!   wire (review B2): the listener serves run credentials alone.

mod files;
mod serve;

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
use file_system::{Fs, Open};
use os::Mode;
use process_env::EnvSnapshot;
use serde::Serialize;
use sessions_application::{ClearRequest, CloseRequest, LaunchRequest, Ordered, SendRequest};

use crate::{LibError, Wiring};
use files::{instance_lock_path, DaemonFiles};

/// The noun.
pub(crate) const NOUN: &str = "daemon";

/// How many open children an owner may hold when nothing says (the
/// delegation limit's evidence, D-N14; no config key yet).
const DELEGATION_LIMIT: u32 = 4;

/// The variables a spawned daemon is given of its spawner's (D-N45), as
/// the platform compares names: how to find programs, temp space and locale,
/// the user's home (`HOME`; on Windows her profile, `USERPROFILE`, `APPDATA`
/// and `LOCALAPPDATA`), and on Windows what a program needs to start
/// (`run_ports::WINDOWS_STARTUP_VARIABLES`, kept beside these). ctxloom's own
/// home is not taken from here: it is the one the spawner resolved.
const DAEMON_ENV: [&str; 8] = [
    "HOME",
    "PATH",
    "TMPDIR",
    "LANG",
    "LC_ALL",
    "USERPROFILE",
    "APPDATA",
    "LOCALAPPDATA",
];

/// S16: how long a hook spends, in all, reaching a daemon (rediscovering
/// it, or spawning one and waiting) and delivering, before it fails open.
const HOOK_BOUND: Duration = Duration::from_secs(10);

/// What a delivery is left of [`HOOK_BOUND`] after the longest spawn wait.
const DELIVERY_MARGIN: Duration = Duration::from_secs(2);

/// How long a client waits for a daemon it spawned to answer its health
/// check: what [`HOOK_BOUND`] leaves once a delivery has its margin.
const SPAWN_WAIT: Duration = HOOK_BOUND.saturating_sub(DELIVERY_MARGIN);

/// How long one health check of a daemon may take (a loopback TLS
/// handshake and one call; a stale port refuses at once).
const HEALTH_WAIT: Duration = Duration::from_secs(1);

/// The environment a spawned daemon is given of its spawner's `vars`
/// (D-N45): what it needs to run, named as `flavor` compares names (so
/// Windows' `Path` is `PATH`), and ctxloom's `home` as the spawner resolved
/// it. Nothing else of the spawner's crosses: a child's environment is its
/// launcher's (S6).
fn daemon_env<N: AsRef<OsStr>, V: AsRef<OsStr>>(
    flavor: os::Flavor,
    vars: impl IntoIterator<Item = (N, V)>,
    home: &Path,
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
    env.push((
        run_ports::HOME_VARIABLE.into(),
        home.as_os_str().to_os_string(),
    ));
    env
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
    },
    /// Say whether a daemon serves, and where
    Status,
    /// Ask the daemon to drain and exit
    Stop {
        /// Stop it even while it hosts live runs, ending them first
        #[arg(long)]
        even_if_live: bool,
    },
    /// Register this project with the daemon, or recheck it
    Resolve,
    /// Send a session a message
    Send {
        /// The session's name
        harp: String,
        /// The message
        text: String,
        /// inform, wake or steer
        #[arg(long, default_value = "wake")]
        urgency: String,
        /// The message's id (minted when not given)
        #[arg(long, default_value = "")]
        id: String,
    },
    /// Close a session; its children are closed with it
    Close {
        /// The session's name
        harp: String,
        /// Why
        #[arg(long, default_value = "closed by the user")]
        cause: String,
    },
    /// Ask a child to clear its conversation
    Clear {
        /// The session's name
        harp: String,
    },
    /// Launch a child of a session
    Launch {
        /// The owner's name
        #[arg(long)]
        owner: String,
        /// The child's first task
        #[arg(long, default_value = "")]
        prompt: String,
        /// The child's engine
        #[arg(long, default_value = "claude-code")]
        engine: String,
        /// The child's name (minted when not given)
        #[arg(long)]
        name: Option<String>,
    },
}

/// The noun, hidden from the help.
pub(crate) fn noun() -> Command {
    shaped(DaemonVerb::augment_subcommands(
        Command::new(NOUN)
            .about("The user's daemon and the orchestrator's commands")
            .hide(true)
            .subcommand_required(true),
    ))
}

/// What an orchestration command answers.
#[derive(Serialize)]
struct Done {
    harp: String,
    id: String,
    recorded: bool,
}

impl From<Ordered> for Done {
    fn from(ordered: Ordered) -> Done {
        Done {
            harp: ordered.harp,
            id: ordered.id,
            recorded: ordered.recorded,
        }
    }
}

impl Render for Done {
    fn json(&self) -> impl Serialize + '_ {
        self
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        let how = if self.recorded {
            "recorded"
        } else {
            "already recorded"
        };
        writeln!(out, "{}: {} {how}", self.harp, self.id)
    }
}

/// What `status` answers.
#[derive(Serialize)]
struct Status {
    serving: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    endpoint: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pid: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    version: Option<String>,
}

impl Render for Status {
    fn json(&self) -> impl Serialize + '_ {
        self
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        match (&self.endpoint, self.pid) {
            (Some(endpoint), Some(pid)) if self.serving => {
                writeln!(out, "serving at {endpoint} (pid {pid})")
            }
            _ if self.serving => writeln!(out, "serving"),
            _ => writeln!(out, "not serving"),
        }
    }
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

/// A sessions use case's failure as the command line's, with its own exit
/// (a refusal 2).
fn refused(error: sessions_application::AppError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit)
}

/// The urgency `word` names.
fn urgency_of(word: &str) -> Result<sessions::Urgency, Failure> {
    match word {
        "inform" => Ok(sessions::Urgency::Inform),
        "wake" => Ok(sessions::Urgency::Wake),
        "steer" => Ok(sessions::Urgency::Steer),
        other => Err(Failure::new(
            CliError::Usage {
                detail: format!("--urgency is inform, wake or steer, not {other}"),
            },
            Exit::REFUSED,
        )),
    }
}

/// Run the `daemon` noun's verb.
pub(crate) fn run(
    env: &Arc<EnvSnapshot>,
    stamp: &str,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let _ = catalog;
    let matches = call
        .matches
        .subcommand_matches(NOUN)
        .unwrap_or_else(|| unreachable!("the line named {NOUN}"));
    let verb = DaemonVerb::from_arg_matches(matches)
        .unwrap_or_else(|_| unreachable!("the command line parsed {NOUN}"));
    let format = call.format();
    let wiring = Wiring::new(Arc::clone(env)).map_err(failed)?;
    let files = files_of(&wiring);
    // One daemon per OS user: a command whose ctxloom home is not the
    // serving daemon's is refused before it does anything (`serve` itself
    // finds the lock held, and exits).
    let serving = match verb {
        DaemonVerb::Serve { .. } => None,
        _ => serving(&wiring, &files)?,
    };
    // M3: a daemon of another version is stopped, idle, or refuses this
    // command, busy, before it writes anything.
    if matches!(
        verb,
        DaemonVerb::Send { .. }
            | DaemonVerb::Close { .. }
            | DaemonVerb::Clear { .. }
            | DaemonVerb::Launch { .. }
    ) {
        reconciled(&files, serving.as_ref(), own_stamp(env, stamp))?;
    }
    let ordered = match verb {
        DaemonVerb::Serve {
            idle_seconds,
            poll_ms,
        } => {
            let pace = serve::Pace {
                idle: Duration::from_secs(idle_seconds),
                poll: Duration::from_millis(poll_ms.max(1)),
            };
            return be_the_daemon(&wiring, env, stamp, &files, pace);
        }
        DaemonVerb::Status => {
            output(render(out, format, &status(serving)))?;
            return Ok(Exit::OK);
        }
        DaemonVerb::Stop { even_if_live } => {
            if let Some(found) = serving {
                block_on(daemon_api::stop(&client_of(&found), even_if_live))?.map_err(called)?;
            }
            return Ok(Exit::OK);
        }
        DaemonVerb::Resolve => {
            let project = resolve(&wiring, env, stamp)?;
            output(render(out, format, &Resolved(project)))?;
            return Ok(Exit::OK);
        }
        DaemonVerb::Send {
            harp,
            text,
            urgency,
            id,
        } => {
            let urgency = urgency_of(&urgency)?;
            orchestrate(&wiring, async |sessions| {
                sessions
                    .send(SendRequest {
                        project: None,
                        harp,
                        message_id: id,
                        urgency,
                        text,
                    })
                    .await
            })?
        }
        DaemonVerb::Close { harp, cause } => orchestrate(&wiring, async |sessions| {
            sessions
                .close(CloseRequest {
                    project: None,
                    harp,
                    cause,
                })
                .await
        })?,
        DaemonVerb::Clear { harp } => orchestrate(&wiring, async |sessions| {
            sessions
                .clear(ClearRequest {
                    project: None,
                    harp,
                    clear_id: String::new(),
                })
                .await
        })?,
        DaemonVerb::Launch {
            owner,
            prompt,
            engine,
            name,
        } => launch(&wiring, env, &files, owner, prompt, engine, name)?,
    };
    ensure(&wiring, env);
    output(render(out, format, &Done::from(ordered)))?;
    Ok(Exit::OK)
}

/// How long one call to the daemon may take.
const CALL_WAIT: Duration = Duration::from_secs(5);

/// How long a client waits for a daemon of another version it stopped to
/// exit (M3).
const STOPPED_WAIT: Duration = Duration::from_secs(8);

/// The daemon `found` names, as a client calls it.
fn client_of(found: &run_ports::Discovery) -> daemon_client::DaemonClient {
    daemon_client::DaemonClient::new(found, CALL_WAIT)
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
fn own_stamp<'a>(env: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
    env.utf8(STAMP_VARIABLE)
        .filter(|s| !s.is_empty())
        .unwrap_or(stamp)
}

#[cfg(not(feature = "test-support"))]
fn own_stamp<'a>(_: &'a EnvSnapshot, stamp: &'a str) -> &'a str {
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
    let outcome = block_on(daemon_api::reconcile(&client_of(found), own, &found.stamp))?;
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
    ensure(wiring, env);
    let Some(found) = serving(wiring, &files)? else {
        return Err(failed(LibError::NoRuntime {
            detail: "no ctxloom daemon could be started".to_string(),
        }));
    };
    let request = wiring.registration().unwrap_or_default();
    block_on(daemon_api::resolve(&client_of(&found), request))?.map_err(called)
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

/// `launch`: the child's id minted here, the environment a child may be
/// given handed off to the daemon under it (S6, D-N45) BEFORE the launch is
/// recorded, and withdrawn if the launch is refused.
fn launch(
    wiring: &Wiring,
    env: &EnvSnapshot,
    files: &DaemonFiles,
    owner: String,
    prompt: String,
    engine: String,
    name: Option<String>,
) -> Result<Ordered, Failure> {
    // The child's stable id is the UUIDv5 of its name (slice 2 B3), its
    // owner's project named first (M1): the hand-off is keyed by it.
    let harp = name.unwrap_or_else(crate::session_name);
    let project_harp = wiring.name_project().map_err(failed)?.harp;
    let session = sessions::SessionName::new(&project_harp, &harp)
        .map_err(|rejection| {
            failed(CliError::Output {
                detail: angzarr_diagnostic::Diagnostic::code(&rejection).to_string(),
            })
        })?
        .uid();
    // Keyed as the platform compares names (uppercase on Windows), as a
    // run's environment is (`os::Flavor::env_key`).
    let flavor = env.flavor();
    let caller: BTreeMap<String, String> = env
        .vars_utf8()
        .into_iter()
        .map(|(name, value)| (flavor.env_key(&name), value))
        .collect();
    let settings = child_plan(
        wiring.home(),
        env.temp_dir(),
        &engine,
        &wiring.project_dir().to_string_lossy(),
        &caller,
    )
    .map(|previewed| {
        run_application::hosted_child(
            &previewed,
            &engine,
            &[],
            run_ports::Credentials::NONE,
            &BTreeMap::new(),
        )
        .settings
    })
    .unwrap_or_default();
    let sent = engine_claude::launcher_environment(&settings, &caller);
    files.hand_off(&session, &sent).map_err(|e| {
        failed(CliError::Output {
            detail: e.to_string(),
        })
    })?;
    let launched = orchestrate(wiring, async |sessions| {
        sessions
            .launch_child(LaunchRequest {
                project: None,
                owner,
                harp,
                engine,
                prompt,
                delegation_limit: DELEGATION_LIMIT,
            })
            .await
    });
    if launched.is_err() {
        files.withdraw_hand_off(&session);
    }
    launched
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
        stamp: stamp.to_string(),
        home: wiring.home().to_path_buf(),
        instance_lock: instance_lock(wiring),
        temp: env.temp_dir().to_path_buf(),
        os: wiring.os_name().to_string(),
        user_home: wiring.user_home(),
    };
    serve::serve(wiring.fs(), place, pace, say)
        .map_err(|detail| failed(LibError::NoRuntime { detail }))?;
    Ok(Exit::OK)
}

/// The single-instance lock of `wiring`'s account ([`instance_lock_path`]),
/// a test's override first.
fn instance_lock(wiring: &Wiring) -> PathBuf {
    instance_lock_path(wiring.home(), instance_override(wiring.env()).as_deref())
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
    let Some(found) = files.discovery().filter(live) else {
        return Ok(None);
    };
    let home = wiring.home().display().to_string();
    if found.home == home {
        Ok(Some(found))
    } else {
        Err(Failure::new(
            LibError::DaemonOtherHome {
                home,
                daemon_home: found.home,
            },
            Exit::REFUSED,
        ))
    }
}

/// Whether a daemon serves, and where: the one `found` serving.
fn status(found: Option<run_ports::Discovery>) -> Status {
    Status {
        serving: found.is_some(),
        endpoint: found.as_ref().map(|d| d.endpoint.clone()),
        pid: found.as_ref().map(|d| d.pid),
        version: found.map(|d| d.stamp),
    }
}

/// Run `use_case` on the sessions use cases, on a runtime of its own.
fn orchestrate(
    wiring: &Wiring,
    use_case: impl AsyncFnOnce(
        &mut crate::SessionUseCases,
    ) -> Result<Ordered, sessions_application::AppError>,
) -> Result<Ordered, Failure> {
    let mut sessions = wiring.sessions();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            failed(LibError::NoRuntime {
                detail: e.to_string(),
            })
        })?;
    runtime
        .block_on(use_case(&mut sessions.use_cases))
        .map_err(refused)
}

/// A log line writer: each line appended to `path` with its time, through
/// the file-system seam. Nothing it is given holds a secret.
fn logger(fs: Arc<dyn Fs>, path: PathBuf) -> Box<dyn FnMut(&str) + Send> {
    Box::new(move |line: &str| {
        if let Some(dir) = path.parent() {
            let _ = fs.create_dir_all(dir, Mode::from_bits(0o700));
        }
        if let Ok(mut file) = fs.open(&path, Open::Append(Mode::from_bits(0o600))) {
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

/// What the user daemon's discovery file says, under the home of `env`
/// (none where there is no home, no file, or no live daemon at what it
/// names: a stale file names a port nobody serves).
pub(crate) fn discovery(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let files = files_of(&wiring);
    serving(&wiring, &files).ok().flatten()
}

/// Make sure the user's daemon serves: spawn one when none is live, and
/// wait, within [`SPAWN_WAIT`], for a live one to advertise itself (the one
/// spawned, or another client's, whichever took the single-instance lock). A stale file
/// is left for the new daemon to publish over. A daemon that cannot be
/// spawned is not this command's failure (what it recorded stands; the next
/// client spawns one).
fn ensure(wiring: &Wiring, env: &EnvSnapshot) {
    let files = files_of(wiring);
    // A live daemon, this home's or (refused before the command ran)
    // another's: never a second.
    if files.discovery().is_some_and(|found| live(&found)) {
        return;
    }
    let Ok(exe) = std::env::current_exe() else {
        return;
    };
    let mut command = std::process::Command::new(exe);
    command.args(["daemon", "serve"]);
    if let Some(idle) = env.utf8(IDLE_VARIABLE) {
        command.args(["--idle-seconds", idle]);
    }
    // The daemon's own environment is what it needs to run, nothing more: a
    // child's is its launcher's (S6, D-N45), so no spawner's credential
    // lives in the daemon.
    let mut kept = daemon_env(env.flavor(), env.vars(), wiring.home());
    // A test's place for the single-instance lock is the daemon's too.
    if let Some(dir) = instance_override(env) {
        kept.push((files::INSTANCE_DIR_VARIABLE.into(), dir.into_os_string()));
    }
    command
        .env_clear()
        .envs(kept)
        .current_dir(wiring.home())
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    // Its own process group (a Ctrl-C at this terminal does not reach it);
    // dropped, not killed: it outlives this command.
    if os::process::spawn_tree(command).is_err() {
        return;
    }
    let deadline = Instant::now() + SPAWN_WAIT;
    while Instant::now() < deadline {
        if files.discovery().is_some_and(|found| live(&found)) {
            return;
        }
        std::thread::sleep(Duration::from_millis(20));
    }
}

/// The plan of a child of the project at `project_dir` (D18, D-N46): that
/// project's dry run, for its default agent on the child's engine, read as a
/// process in that project would, with `env` (the environment its launcher
/// sent, S6) and ctxloom's `home`. Where it cannot be planned, the error's
/// code.
pub(crate) fn child_plan(
    home: &Path,
    temp: &Path,
    engine: &str,
    project_dir: &str,
    env: &BTreeMap<String, String>,
) -> Result<library_application::Previewed, String> {
    let mut vars: Vec<(OsString, OsString)> = env
        .iter()
        .filter(|(name, _)| !matches!(name.as_str(), "CTXLOOM_ROOT" | "HOME"))
        .map(|(name, value)| (name.into(), value.into()))
        .collect();
    vars.push(("CTXLOOM_ROOT".into(), project_dir.into()));
    vars.push(("HOME".into(), home.as_os_str().to_os_string()));
    let snapshot = EnvSnapshot::from_parts(
        os::host().flavor(),
        Ok(PathBuf::from(project_dir)),
        temp.to_path_buf(),
        vars,
    );
    let code = |e: &dyn angzarr_diagnostic::Diagnostic| e.code().to_string();
    let wiring = Wiring::new(Arc::new(snapshot)).map_err(|e| code(&e))?;
    let library = crate::library::build(&wiring, None).map_err(|e| code(&e))?;
    let request = library_application::RunRequest {
        engine: engine.to_string(),
        ..library_application::RunRequest::default()
    };
    library
        .dry_run(&library_application::Flags::default(), &request)
        .result
        .map_err(|e| code(&e))
}

#[cfg(test)]
#[path = "mod.test.rs"]
mod tests;
