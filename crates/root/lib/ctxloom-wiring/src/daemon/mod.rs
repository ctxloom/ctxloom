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
mod route;
mod serve;

use std::collections::BTreeMap;
use std::ffi::OsString;
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
use files::DaemonFiles;

/// The noun.
pub(crate) const NOUN: &str = "daemon";

/// How many open children an owner may hold when nothing says (the
/// delegation limit's evidence, D-N14; no config key yet).
const DELEGATION_LIMIT: u32 = 4;

/// The variables a spawned daemon is given of its spawner's (D-N45).
const DAEMON_ENV: [&str; 8] = [
    "HOME",
    run_ports::HOME_VARIABLE,
    "PATH",
    "TMPDIR",
    "LANG",
    "LC_ALL",
    "USERPROFILE",
    "SystemRoot",
];

/// How long a client waits for a daemon it spawned to advertise itself.
const SPAWN_WAIT: Duration = Duration::from_secs(15);

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
    Stop,
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
    let files = DaemonFiles::new(wiring.fs(), wiring.home());
    let ordered = match verb {
        DaemonVerb::Serve {
            idle_seconds,
            poll_ms,
        } => {
            let pace = serve::Pace {
                idle: Duration::from_secs(idle_seconds),
                poll: Duration::from_millis(poll_ms.max(1)),
            };
            return be_the_daemon(&wiring, env, &files, pace);
        }
        DaemonVerb::Status => {
            output(render(out, format, &status(&files)))?;
            return Ok(Exit::OK);
        }
        DaemonVerb::Stop => {
            if files.serving() {
                files.request_stop().map_err(|e| {
                    failed(CliError::Output {
                        detail: e.to_string(),
                    })
                })?;
            }
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
    let session = uuid::Uuid::now_v7().hyphenated().to_string();
    let caller: BTreeMap<String, String> = env.vars_utf8().into_iter().collect();
    let settings = child_plan(
        wiring.home(),
        env.temp_dir(),
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
                harp: name.unwrap_or_else(crate::session_name),
                engine,
                prompt,
                delegation_limit: DELEGATION_LIMIT,
                session: session.clone(),
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
    files: &DaemonFiles,
    pace: serve::Pace,
) -> Result<Exit, Failure> {
    let say = logger(wiring.fs(), files.log_path());
    let place = serve::Place {
        home: wiring.home().to_path_buf(),
        temp: env.temp_dir().to_path_buf(),
    };
    serve::serve(wiring.fs(), place, pace, say)
        .map_err(|detail| failed(LibError::NoRuntime { detail }))?;
    Ok(Exit::OK)
}

/// Whether a daemon serves, and where.
fn status(files: &DaemonFiles) -> Status {
    let serving = files.serving();
    let found = files.discovery().filter(|_| serving);
    Status {
        serving,
        endpoint: found.as_ref().map(|d| d.endpoint.clone()),
        pid: found.as_ref().map(|d| d.pid),
        version: found.map(|d| d.version),
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
fn logger(fs: Arc<dyn Fs>, path: PathBuf) -> Box<dyn FnMut(&str)> {
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

/// What the user daemon's discovery file says, under the home of `env`
/// (none where there is no home, no file, or no daemon holding the
/// election: a stale file names a port nobody serves).
pub(crate) fn discovery(env: &Arc<EnvSnapshot>) -> Option<run_ports::Discovery> {
    let wiring = Wiring::new(Arc::clone(env)).ok()?;
    let files = DaemonFiles::new(wiring.fs(), wiring.home());
    // The file first: with none, the lock is never touched (nothing made).
    let found = files.discovery()?;
    files.serving().then_some(found)
}

/// Make sure the user's daemon serves: spawn one when none does, and wait
/// for it to advertise itself. A daemon that cannot be spawned is not this
/// command's failure (what it recorded stands; the next client spawns one).
fn ensure(wiring: &Wiring, env: &EnvSnapshot) {
    let files = DaemonFiles::new(wiring.fs(), wiring.home());
    if files.serving() {
        return;
    }
    files.withdraw();
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
    let kept = env
        .vars()
        .filter(|(name, _)| DAEMON_ENV.iter().any(|kept| name == kept));
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
        if files.discovery().is_some() {
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
        engine: session_policies::CHILD_ENGINE.to_string(),
        ..library_application::RunRequest::default()
    };
    library
        .dry_run(&library_application::Flags::default(), &request)
        .result
        .map_err(|e| code(&e))
}
