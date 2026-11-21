//! THE launch (T10): everything needed to start one engine process, as one
//! value, computed in one place (`run_launcher::launch_of`) and started by
//! whichever process holds it: the daemon, for the one-shots and children it
//! hosts, and the frontend, for the terminal session it starts. It is the
//! domain value of `ctxloom.v1.Launch` (`launch.proto`); its conversion to
//! and from the wire is the daemon API's (`daemon_api::launch`).
//!
//! A launch CARRIES SECRETS (an engine credential in its environment): its
//! `Debug` names the environment's variables and withholds every value
//! ([`RedactedEnv`]), and shows how much input it has, never the input (a
//! one-shot's prompt).
//!
//! What the launch SAID (the permits it did not follow, the user's settings
//! it left out) rides beside it for the caller to tell, and its input (a
//! one-shot turn's prompt on stdin) is the host's alone: neither is on the
//! wire.

use std::collections::BTreeMap;
use std::fmt;
use std::time::Duration;

use crate::hook::RedactedEnv;
use crate::{Ignored, LeftOut, TurnCommand};

/// The signal a launch is asked to stop with (SIGTERM).
pub const STOP_SIGNAL: u32 = 15;

/// How long a launch asked to stop has before it is killed (ruling 114).
pub const STOP_GRACE: Duration = Duration::from_secs(5);

/// Which consumer starts a launch, and how (`Launch.Kind`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LaunchKind {
    /// The interactive TUI, on the user's terminal: inherited stdio, the
    /// starter's process group, no deadline.
    Terminal,
    /// A long-lived child hosted by the daemon, in its engine's protocol.
    Child,
    /// A one-shot's turn (owner ruling s4-Q8: the engine's turn command).
    OneShot,
    /// `$EDITOR` on a file, on the user's terminal as a terminal launch is.
    Editor,
}

/// The protocol on a launch's stdin and stdout (`Launch.Stdio`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Stdio {
    /// The turn command's own pipes (a one-shot: its prompt on stdin, its
    /// output captured and read by the engine). It has no value of its own
    /// on the wire, where it is `STDIO_UNSPECIFIED`: a one-shot's launch is
    /// the daemon's and never crosses it.
    Piped,
    /// The user's terminal (a terminal launch, an editor's).
    Inherit,
    /// claude's stream-json lines (D15).
    StreamJson,
    /// opencode's ACP over JSON-RPC lines (D21, D23).
    Acp,
}

/// How a launch's holder stops it when asked (ruling 114).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct StopPolicy {
    /// The signal it is asked to stop with.
    pub signal: u32,
    /// How long it has before it is killed.
    pub grace: Duration,
    /// A hosted child's input is closed first (claude exits on its end).
    pub close_stdin_first: bool,
}

impl StopPolicy {
    /// How a launch of `kind` is stopped: SIGTERM, five seconds, then the
    /// kill; a child's input closed first.
    pub fn of(kind: LaunchKind) -> StopPolicy {
        StopPolicy {
            signal: STOP_SIGNAL,
            grace: STOP_GRACE,
            close_stdin_first: kind == LaunchKind::Child,
        }
    }
}

/// Which run a launch starts (`LaunchAt`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LaunchAt {
    pub project_id: String,
    /// The session's stable id (its stream root, UUID text).
    pub session: String,
    pub harp: String,
    /// The run's id, and the incarnation of the session it is.
    pub run: String,
    pub incarnation: u32,
}

/// One engine process to start.
#[derive(Clone, PartialEq, Eq)]
pub struct Launch {
    pub at: LaunchAt,
    pub kind: LaunchKind,
    /// The engine's name.
    pub engine: String,
    /// The program, as the launch found it on the command's own `PATH`.
    pub program: String,
    pub args: Vec<String>,
    /// Its WHOLE environment: nothing is inherited from the starter.
    pub env: BTreeMap<String, String>,
    /// What it is fed on stdin (a one-shot turn's prompt); never on the wire.
    pub stdin: Vec<u8>,
    /// Where it runs (the project, or the session's checkout).
    pub cwd: Option<String>,
    /// The session home, already written.
    pub session_home: String,
    /// The session's lock the starter takes before the start and holds
    /// until the exit is reported; empty names none.
    pub session_lock: String,
    pub stdio: Stdio,
    pub stop: StopPolicy,
    /// The caller's permits the launch did not follow (rulings 78, 78b), by
    /// the permit's name: the caller says them. Never on the wire.
    pub ignored: Vec<Ignored>,
    /// The user's own engine settings the session home was not started
    /// from (ruling 169), by path: the caller says them. Never on the wire.
    pub left_out: Vec<LeftOut>,
}

impl Launch {
    /// A launch of `kind` on `engine` that starts `command` as it is: no run
    /// named, no session home or lock, the kind's stdio and stop.
    pub fn new(kind: LaunchKind, engine: &str, stdio: Stdio, command: TurnCommand) -> Launch {
        Launch {
            at: LaunchAt::default(),
            kind,
            engine: engine.to_string(),
            program: command.program,
            args: command.args,
            env: command.env,
            stdin: command.stdin,
            cwd: command.cwd,
            session_home: String::new(),
            session_lock: String::new(),
            stdio,
            stop: StopPolicy::of(kind),
            ignored: Vec::new(),
            left_out: Vec::new(),
        }
    }

    /// The command that starts it: its program, arguments, whole
    /// environment, input and directory.
    pub fn command(&self) -> TurnCommand {
        TurnCommand {
            program: self.program.clone(),
            args: self.args.clone(),
            env: self.env.clone(),
            stdin: self.stdin.clone(),
            cwd: self.cwd.clone(),
        }
    }
}

impl fmt::Debug for Launch {
    /// The environment's names alone and the input's size: a launch is
    /// logged, no value of its environment and none of its input is.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Launch")
            .field("at", &self.at)
            .field("kind", &self.kind)
            .field("engine", &self.engine)
            .field("program", &self.program)
            .field("args", &self.args)
            .field("env", &RedactedEnv(&self.env))
            .field("stdin_bytes", &self.stdin.len())
            .field("cwd", &self.cwd)
            .field("session_home", &self.session_home)
            .field("session_lock", &self.session_lock)
            .field("stdio", &self.stdio)
            .field("stop", &self.stop)
            .field("ignored", &self.ignored)
            .field("left_out", &self.left_out)
            .finish()
    }
}

#[cfg(test)]
#[path = "launch.test.rs"]
mod tests;
