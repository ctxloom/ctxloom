//! A long-lived claude child (D15): one `claude -p --input-format stream-json
//! --output-format stream-json --verbose --replay-user-messages` process,
//! many turns, on pipes run-within hands over (`run_piped`).
//!
//! The command is a turn's ([`crate::drive`]: the context file, the session
//! home's `--settings` and its hooks, the model and effort, a child's
//! environment), with streaming input and the echo of each message given.
//! [`crate::conversation`] says what to write and what a line means; this
//! module is its process: the control writes under one lock, a reader thread
//! reads stdout, writes what a line asks (the next held turn) and passes the
//! observations on, and a stderr thread keeps the start of stderr for the
//! exit's detail.
//!
//! The control's `Drop` closes the engine's input, so a child no one holds
//! ends after the turn it is on (claude exits on its input's end).

use std::io::{BufRead, BufReader, Read, Write};
use std::process::{ChildStdin, ExitStatus};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use run_ports::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildSpec, Ended, EngineError, Exit,
    Queued, StartedChild, TurnCommand, TurnRequest, Urgency,
};
use run_within::{run_piped, Piped, RunError, Spec};

use crate::conversation::Conversation;
use crate::{drive, stream, Claude};

/// How long, once the engine has exited, its stdout is read for: a
/// descendant that outlives it can hold the pipe open, and is then killed.
const DRAIN_GRACE: Duration = Duration::from_secs(2);

/// How often [`ChildControl::end`] looks whether the engine has exited.
const POLL: Duration = Duration::from_millis(10);

/// The most of stderr kept for the exit's detail.
const STDERR_KEPT: usize = 4096;

impl ChildEngine for Claude {
    fn start(&self, spec: &ChildSpec) -> Result<StartedChild, ChildError> {
        start(&child_command(spec)?)
    }
}

/// The child's command: a turn's, streaming. Its stdin is the conversation.
pub(crate) fn child_command(spec: &ChildSpec) -> Result<TurnCommand, EngineError> {
    let request = TurnRequest {
        prompt: String::new(),
        home: spec.home.clone(),
        context_path: spec.context_path.clone(),
        resume: spec.resume.clone(),
        env: spec.env.clone(),
        cwd: spec.cwd.clone(),
        model: spec.model.clone(),
        effort: spec.effort.clone(),
        settings: spec.settings.clone(),
    };
    let mut command = TurnCommand::new(drive::program(&request));
    command.args = [
        "-p",
        "--input-format",
        "stream-json",
        "--output-format",
        "stream-json",
        "--verbose",
        "--replay-user-messages",
        "--append-system-prompt-file",
    ]
    .map(String::from)
    .to_vec();
    command.args.push(request.context_path.clone());
    drive::push_settings(&mut command.args, &request);
    drive::push_model_and_effort(&mut command.args, &request)?;
    if let Some(native_id) = &spec.resume {
        command.args.push("--resume".to_string());
        command.args.push(native_id.clone());
    } else if let Some(session_id) = &spec.session_id {
        command.args.push("--session-id".to_string());
        command.args.push(session_id.clone());
    }
    command.env = drive::environment(&Claude::new(), false, &request)?;
    command.cwd = request.cwd.clone();
    Ok(command)
}

/// What the control and the reader share.
struct Shared {
    conversation: Conversation,
    /// The engine's input; `None` once it is closed.
    stdin: Option<ChildStdin>,
}

impl Shared {
    /// Write `lines`, a line each. Nothing to write to is [`ChildError::Closed`].
    fn write(&mut self, lines: &[String]) -> Result<(), ChildError> {
        if lines.is_empty() {
            return Ok(());
        }
        let stdin = self.stdin.as_mut().ok_or(ChildError::Closed)?;
        for line in lines {
            writeln!(stdin, "{line}").map_err(io)?;
        }
        stdin.flush().map_err(io)
    }
}

fn io(error: std::io::Error) -> ChildError {
    ChildError::Io {
        ext_err_txt: error.to_string(),
    }
}

fn lock(shared: &Mutex<Shared>) -> MutexGuard<'_, Shared> {
    shared.lock().unwrap_or_else(PoisonError::into_inner)
}

/// The child's control.
struct Control {
    shared: Arc<Mutex<Shared>>,
    piped: Piped,
}

/// Start `command` as a child.
fn start(command: &TurnCommand) -> Result<StartedChild, ChildError> {
    let mut spec = Spec::new(&command.program)
        .args(&command.args)
        .envs(&command.env);
    if let Some(cwd) = &command.cwd {
        spec = spec.current_dir(cwd);
    }
    let (piped, pipes) = run_piped(&spec, |_| {}).map_err(|error| not_started(command, &error))?;
    let shared = Arc::new(Mutex::new(Shared {
        conversation: Conversation::default(),
        stdin: Some(pipes.stdin),
    }));
    let (observed, observations) = mpsc::channel();
    let stderr = keep_stderr(pipes.stderr);
    let (exited, status) = watch(piped.clone());
    let reader_shared = Arc::clone(&shared);
    let stdout = pipes.stdout;
    thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            let Ok(line) = line else { break };
            let observations = {
                let mut shared = lock(&reader_shared);
                let step = shared.conversation.read(&line);
                // A write that fails is the engine going: its exit follows.
                let _ = shared.write(&step.writes);
                step.observations
            };
            for observation in observations {
                let _ = observed.send(observation);
            }
        }
        let _ = exited.send(());
        let (exit, failure) = match status.recv() {
            Ok(Ok(status)) => (exit_of(status), String::new()),
            Ok(Err(error)) => (no_exit(), error.to_string()),
            Err(_) => (no_exit(), String::new()),
        };
        let detail = detail_of(failure, stderr.recv_timeout(DRAIN_GRACE).ok());
        let observations = {
            let mut shared = lock(&reader_shared);
            shared.stdin = None;
            shared.conversation.exited(exit, detail)
        };
        for observation in observations {
            let _ = observed.send(observation);
        }
    });
    Ok(StartedChild {
        control: Box::new(Control { shared, piped }),
        observations: Box::new(observations.into_iter()),
    })
}

/// Wait for the engine's exit on a thread: its status, once stdout has
/// ended; a stdout a descendant still holds DRAIN_GRACE after the exit is
/// ended by killing the tree.
fn watch(
    piped: Piped,
) -> (
    mpsc::Sender<()>,
    mpsc::Receiver<Result<ExitStatus, RunError>>,
) {
    let (exited, read_out) = mpsc::channel::<()>();
    let (told, status) = mpsc::channel();
    thread::spawn(move || {
        let waited = piped.wait();
        if let Err(RecvTimeoutError::Timeout) = read_out.recv_timeout(DRAIN_GRACE) {
            let _ = piped.kill_tree();
        }
        let _ = told.send(waited);
    });
    (exited, status)
}

/// The start of what the engine writes to stderr, read to its end on a
/// thread so that the engine never blocks writing it.
fn keep_stderr(stderr: impl Read + Send + 'static) -> mpsc::Receiver<String> {
    let (told, kept) = mpsc::channel();
    thread::spawn(move || {
        let mut start = Vec::new();
        let mut reader = stderr;
        let mut buffer = [0u8; 4096];
        while let Ok(read) = reader.read(&mut buffer) {
            if read == 0 {
                break;
            }
            let room = STDERR_KEPT.saturating_sub(start.len());
            start.extend_from_slice(&buffer[..read.min(room)]);
        }
        let text = String::from_utf8_lossy(&start);
        let _ = told.send(stream::bounded(text.trim()).to_string());
    });
    kept
}

/// The exit's detail: a wait that failed says so; else the start of stderr
/// (none when its reader did not finish).
fn detail_of(failure: String, stderr: Option<String>) -> String {
    if failure.is_empty() {
        stderr.unwrap_or_default()
    } else {
        failure
    }
}

fn not_started(command: &TurnCommand, error: &RunError) -> ChildError {
    ChildError::NotStarted {
        program: command.program.clone(),
        // A program that is not there is not there next time either.
        deterministic: error.io().kind() == std::io::ErrorKind::NotFound,
        ext_err_txt: error.io().to_string(),
    }
}

fn no_exit() -> Exit {
    Exit {
        code: None,
        signal: None,
    }
}

fn exit_of(status: ExitStatus) -> Exit {
    Exit {
        code: status.code(),
        signal: signal_of(&status),
    }
}

/// The signal that ended a process, when one did: std spells it in the
/// status's text ("signal: 9 (SIGKILL)"), and the os crate has no accessor
/// for it, so that text is read rather than naming the platform here (as
/// host-launcher does).
fn signal_of(status: &ExitStatus) -> Option<i32> {
    status
        .to_string()
        .strip_prefix("signal: ")?
        .split(|c: char| !c.is_ascii_digit())
        .next()?
        .parse()
        .ok()
}

impl ChildControl for Control {
    fn pid(&self) -> u32 {
        self.piped.id()
    }

    fn send(&self, message: ChildMessage, urgency: Urgency) -> Result<Queued, ChildError> {
        let mut shared = lock(&self.shared);
        let (queued, writes) = shared.conversation.send(message, urgency)?;
        shared.write(&writes)?;
        Ok(queued)
    }

    fn interrupt(&self) -> Result<bool, ChildError> {
        let mut shared = lock(&self.shared);
        let (asked, writes) = shared.conversation.interrupt()?;
        shared.write(&writes)?;
        Ok(asked)
    }

    fn clear(&self) -> Result<Queued, ChildError> {
        let mut shared = lock(&self.shared);
        let (queued, writes) = shared.conversation.clear()?;
        shared.write(&writes)?;
        Ok(queued)
    }

    fn end(&self, grace: Duration) -> Result<Ended, ChildError> {
        let undelivered = {
            let mut shared = lock(&self.shared);
            let (undelivered, writes) = shared.conversation.end()?;
            // An engine that cannot hear the interrupt is ended below anyway.
            let _ = shared.write(&writes);
            shared.stdin = None;
            undelivered
        };
        let deadline = Instant::now() + grace;
        let status = loop {
            if let Some(status) = self.piped.try_wait().map_err(run_error)? {
                break status;
            }
            if Instant::now() >= deadline {
                self.piped.terminate_tree(grace).map_err(run_error)?;
                break self.piped.wait().map_err(run_error)?;
            }
            thread::sleep(POLL);
        };
        Ok(Ended {
            exit: exit_of(status),
            undelivered,
        })
    }
}

impl Drop for Control {
    /// The engine's input is closed: it ends after the turn it is on.
    fn drop(&mut self) {
        lock(&self.shared).stdin = None;
    }
}

fn run_error(error: RunError) -> ChildError {
    ChildError::Io {
        ext_err_txt: error.to_string(),
    }
}

#[cfg(test)]
#[path = "child.test.rs"]
mod tests;
