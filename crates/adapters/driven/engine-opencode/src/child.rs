//! A long-lived opencode child (D15, D23): one `opencode acp` process, many
//! turns, driven as an ACP CLIENT (agent-client-protocol =5.2.0, D21) over
//! the pipes run-within hands over (`run_piped`).
//!
//! The command: `opencode acp --cwd <worktree> --hostname 127.0.0.1 --port
//! 0`, in a turn's relocated environment (the session home as
//! `OPENCODE_CONFIG_DIR`, `HOME` and every XDG base in it, [`crate::drive`]),
//! with a random per-child `OPENCODE_SERVER_PASSWORD` (D24: `acp` opens its
//! own loopback HTTP listener, unauthenticated without one; ctxloom never
//! talks to it). Never `--mdns` (it would bind every address) nor `--pure`
//! (it would skip the plugin, and capture with it).
//!
//! [`crate::conversation`] says what to ask and what an answer means; this
//! module is its process and its connection. The connection is the crate's
//! own (`Client.v1()`, its `Lines` transport), run by `futures`' executor on
//! a thread of the child's: a reader thread turns stdout into the
//! transport's incoming lines, the transport's outgoing lines are written to
//! stdin, and the connection's main task carries out each [`Act`] in the
//! order it was decided (the control and the connection both decide under
//! one lock and post to one channel). A stderr thread keeps the start of
//! stderr for the exit's detail.
//!
//! The input's end is ordered too: [`Act::CloseInput`] is sent through the
//! connection as a notification of ctxloom's own (`_ctxloom/end_of_input`),
//! which the outgoing side never writes: it closes stdin there, after every
//! line before it (a cancel, say) was written. The connection keeps reading
//! until opencode exits, so a cancelled turn's reply is still heard.
//!
//! A permission request (`session/request_permission`) is answered from the
//! child's posture ([`ChildPermissions`]): no human is there to ask.
//!
//! The control's `Drop` closes the engine's input, so a child no one holds
//! ends (opencode runs its plugin's `dispose` and exits on its input's end).

use std::io::{BufRead, BufReader, Read, Write};
use std::process::{ChildStdin, ExitStatus};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use agent_client_protocol::schema::ProtocolVersion;
use agent_client_protocol::v1::schema::{
    CancelNotification, ClientCapabilities, ContentBlock, InitializeRequest, LoadSessionRequest,
    NewSessionRequest, NewSessionResponse, PermissionOptionKind, PromptRequest, PromptResponse,
    RequestPermissionOutcome, RequestPermissionRequest, RequestPermissionResponse,
    SelectedPermissionOutcome, SessionNotification, SessionUpdate, StopReason, TextContent,
};
use agent_client_protocol::{Client, JsonRpcNotification, JsonRpcRequest, JsonRpcResponse, Lines};
use futures::channel::mpsc::{unbounded, UnboundedReceiver, UnboundedSender};
use futures::{FutureExt, StreamExt};
use run_ports::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildObservation, ChildPermissions,
    ChildSpec, Ended, EngineError, Exit, Queued, StartedChild, Token, TurnCommand, TurnRequest,
    Urgency,
};
use run_within::{run_piped, Piped, RunError, Spec};
use serde::{Deserialize, Serialize};
use surface_ports::Json;

use crate::conversation::{Act, Conversation, Heard, Opening, Stop};
use crate::{drive, Opencode};

/// opencode's own listener's password (D24): random per child, never logged.
pub(crate) const PASSWORD_VARIABLE: &str = "OPENCODE_SERVER_PASSWORD";

/// The method of the notification that ends the engine's input: ctxloom's
/// own (an underscore method is an extension's), never written.
const END_OF_INPUT: &str = "_ctxloom/end_of_input";

/// How long, once the engine has exited, its stdout is read for: a
/// descendant that outlives it can hold the pipe open, and is then killed.
const DRAIN_GRACE: Duration = Duration::from_secs(2);

/// How often [`ChildControl::end`] looks whether the engine has exited.
const POLL: Duration = Duration::from_millis(10);

/// The most of stderr kept for the exit's detail.
const STDERR_KEPT: usize = 4096;

impl ChildEngine for Opencode {
    fn start(&self, spec: &ChildSpec) -> Result<StartedChild, ChildError> {
        let command = child_command(spec, &password()?)?;
        let opening = Opening {
            cwd: workdir(spec).to_string(),
            model: spec.model.clone(),
            resume: spec.resume.clone(),
        };
        start(&command, opening, spec.permissions)
    }
}

/// A fresh password: 256 random bits, as 64 hexadecimal digits.
fn password() -> Result<String, ChildError> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|error| ChildError::Io {
        ext_err_txt: error.to_string(),
    })?;
    Ok(Token::from_bytes(bytes).to_text())
}

/// Where the child works: the worktree, else the session home.
fn workdir(spec: &ChildSpec) -> &str {
    spec.cwd.as_deref().unwrap_or(&spec.home)
}

/// The child's command. Its stdin and stdout are the ACP connection. The
/// model is set over ACP (`session/set_model`), not by a flag; an effort
/// level is not passed (no opencode flag for it is verified, as for a turn).
pub(crate) fn child_command(spec: &ChildSpec, password: &str) -> Result<TurnCommand, EngineError> {
    let request = TurnRequest {
        prompt: String::new(),
        home: spec.home.clone(),
        context_path: spec.context_path.clone(),
        resume: spec.resume.clone(),
        env: spec.env.clone(),
        cwd: spec.cwd.clone(),
        model: spec.model.clone(),
        effort: spec.effort.clone(),
        // A hosted child's spec carries no permission mode yet (ruling 125
        // reached the turn and the terminal; the child is a follow-up).
        permission_mode: String::new(),
        settings: spec.settings.clone(),
    };
    let mut command = TurnCommand::new(drive::program(&request));
    command.args = vec![
        "acp".to_string(),
        "--cwd".to_string(),
        workdir(spec).to_string(),
        "--hostname".to_string(),
        "127.0.0.1".to_string(),
        "--port".to_string(),
        "0".to_string(),
    ];
    command.env = drive::environment(&Opencode::new(), &request)?;
    command
        .env
        .insert(PASSWORD_VARIABLE.to_string(), password.to_string());
    command.cwd = request.cwd.clone();
    Ok(command)
}

/// `session/set_model`: opencode's (verified live, 1.18.4); ACP v1's stable
/// schema has none (its `session/set_config_option` is not verified).
#[derive(Clone, Debug, Serialize, Deserialize, JsonRpcRequest)]
#[request(method = "session/set_model", response = SetModelResponse)]
#[serde(rename_all = "camelCase")]
struct SetModelRequest {
    session_id: String,
    model_id: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, JsonRpcResponse)]
struct SetModelResponse {}

/// The input's end, in order with the rest: never written.
#[derive(Clone, Debug, Serialize, Deserialize, JsonRpcNotification)]
#[notification(method = "_ctxloom/end_of_input")]
struct EndOfInput {}

/// What the connection's main task is given, in order.
enum Input {
    Act(Act),
    Heard(Heard),
}

/// What the control and the connection share.
struct Shared {
    conversation: Conversation,
    /// The main task's inbox: decided calls are posted here under the lock,
    /// so they are made in the order decided.
    inbox: UnboundedSender<Input>,
}

impl Shared {
    fn post(&self, acts: Vec<Act>) {
        for act in acts {
            // A main task that is gone has nothing left to do.
            let _ = self.inbox.unbounded_send(Input::Act(act));
        }
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

/// Start `command` as a child opened with `opening`.
fn start(
    command: &TurnCommand,
    opening: Opening,
    permissions: ChildPermissions,
) -> Result<StartedChild, ChildError> {
    let mut spec = Spec::new(&command.program)
        .args(&command.args)
        .envs(&command.env);
    if let Some(cwd) = &command.cwd {
        spec = spec.current_dir(cwd);
    }
    let (piped, pipes) = run_piped(&spec, |_| {}).map_err(|error| not_started(command, &error))?;
    let (inbox, inputs) = unbounded();
    let (conversation, first) = Conversation::new(opening);
    let shared = Arc::new(Mutex::new(Shared {
        conversation,
        inbox: inbox.clone(),
    }));
    lock(&shared).post(first);
    let (observed, observations) = mpsc::channel();
    let stderr = keep_stderr(pipes.stderr);
    let (exited, status) = watch(piped.clone());
    let incoming = read_lines(pipes.stdout);
    let stdin = pipes.stdin;
    let driver_shared = Arc::clone(&shared);
    thread::spawn(move || {
        let transport = Lines::new(Box::pin(outgoing(stdin)), incoming);
        let connection = connect(
            transport,
            &driver_shared,
            inbox,
            inputs,
            &observed,
            permissions,
        );
        // A connection that broke ends as the engine's exit says.
        let _ = futures::executor::block_on(connection);
        let _ = exited.send(());
        let (exit, failure) = match status.recv() {
            Ok(Ok(status)) => (exit_of(status), String::new()),
            Ok(Err(error)) => (no_exit(), error.to_string()),
            Err(_) => (no_exit(), String::new()),
        };
        let detail = detail_of(failure, stderr.recv_timeout(DRAIN_GRACE).ok());
        let observations = lock(&driver_shared).conversation.exited(exit, detail);
        for observation in observations {
            let _ = observed.send(observation);
        }
    });
    Ok(StartedChild {
        control: Box::new(Control { shared, piped }),
        observations: Box::new(observations.into_iter()),
    })
}

/// The engine's stdout as the transport's incoming lines, read on a thread
/// so that the engine never blocks writing it.
fn read_lines(stdout: impl Read + Send + 'static) -> UnboundedReceiver<std::io::Result<String>> {
    let (lines, incoming) = unbounded();
    thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            if lines.unbounded_send(line).is_err() {
                break;
            }
        }
    });
    incoming
}

/// The transport's outgoing lines, written to the engine's stdin, a line
/// each; [`END_OF_INPUT`] closes it instead. A line the engine can no longer
/// take is dropped (its exit follows), so the connection goes on reading.
fn outgoing(
    stdin: ChildStdin,
) -> impl futures::Sink<String, Error = std::io::Error> + Send + 'static {
    futures::sink::unfold(
        Some(stdin),
        |mut stdin: Option<ChildStdin>, line: String| async move {
            if is_end_of_input(&line) {
                return Ok(None);
            }
            if let Some(pipe) = stdin.as_mut() {
                let written = writeln!(pipe, "{line}").and_then(|()| pipe.flush());
                if written.is_err() {
                    stdin = None;
                }
            }
            Ok(stdin)
        },
    )
}

fn is_end_of_input(line: &str) -> bool {
    Json::parse(line)
        .ok()
        .and_then(|message| {
            message
                .get("method")
                .and_then(Json::as_str)
                .map(|method| method == END_OF_INPUT)
        })
        .unwrap_or(false)
}

/// The connection, until the engine's output ends.
async fn connect(
    transport: impl agent_client_protocol::ConnectTo<Client> + 'static,
    shared: &Arc<Mutex<Shared>>,
    inbox: UnboundedSender<Input>,
    mut inputs: UnboundedReceiver<Input>,
    observed: &mpsc::Sender<ChildObservation>,
    permissions: ChildPermissions,
) -> Result<(), agent_client_protocol::Error> {
    let updates = inbox.clone();
    Client
        .v1()
        .name("ctxloom")
        .on_receive_notification(
            async move |notification: SessionNotification, _connection| {
                if let Some(said) = said_of(notification) {
                    let _ = updates.unbounded_send(Input::Heard(said));
                }
                Ok(())
            },
            agent_client_protocol::on_receive_notification!(),
        )
        .on_receive_request(
            async move |request: RequestPermissionRequest, responder, _connection| {
                responder.respond(RequestPermissionResponse::new(permission_answer(
                    &request,
                    permissions,
                )))
            },
            agent_client_protocol::on_receive_request!(),
        )
        .connect_with(transport, async move |connection| {
            let closed = connection.incoming_closed().fuse();
            futures::pin_mut!(closed);
            loop {
                futures::select! {
                    input = inputs.next() => match input {
                        Some(input) => carry_out(&connection, shared, &inbox, observed, input),
                        None => break,
                    },
                    () = closed => break,
                }
            }
            // What was heard before the output ended is still said; nothing
            // more is asked of an engine that is going.
            while let Ok(input) = inputs.try_recv() {
                if let Input::Heard(_) = input {
                    carry_out(&connection, shared, &inbox, observed, input);
                }
            }
            Ok(())
        })
        .await
}

/// Carry out one input: make a call, or hear an answer.
fn carry_out(
    connection: &agent_client_protocol::v1::ConnectionTo<agent_client_protocol::Agent>,
    shared: &Mutex<Shared>,
    inbox: &UnboundedSender<Input>,
    observed: &mpsc::Sender<ChildObservation>,
    input: Input,
) {
    match input {
        Input::Heard(heard) => {
            let observations = {
                let mut shared = lock(shared);
                let (observations, acts) = shared.conversation.hear(heard);
                shared.post(acts);
                observations
            };
            for observation in observations {
                let _ = observed.send(observation);
            }
        }
        // A call that cannot be sent is the connection going: its end follows.
        Input::Act(act) => {
            let _ = call(connection, inbox, observed, act);
        }
    }
}

/// Make `act`'s call; its answer is posted back as heard. A request is
/// PREPARED, so its reply's handler holds the dispatch loop's order (the
/// crate's ordering rules): the agent message chunks a prompt's reply follows
/// are heard before it.
fn call(
    connection: &agent_client_protocol::v1::ConnectionTo<agent_client_protocol::Agent>,
    inbox: &UnboundedSender<Input>,
    observed: &mpsc::Sender<ChildObservation>,
    act: Act,
) -> Result<(), agent_client_protocol::Error> {
    match act {
        Act::Initialize => connection
            .prepare_request(
                InitializeRequest::new(ProtocolVersion::V1)
                    .client_capabilities(ClientCapabilities::new()),
            )
            .on_receiving_result(answer(inbox, |r| Heard::Initialized(r.map(|_| ())))),
        Act::NewSession { cwd } => connection
            .prepare_request(NewSessionRequest::new(cwd))
            .on_receiving_result(answer(inbox, |r: Result<NewSessionResponse, String>| {
                Heard::SessionReady(r.map(|r| r.session_id.to_string()))
            })),
        Act::LoadSession { session, cwd } => connection
            .prepare_request(LoadSessionRequest::new(session.clone(), cwd))
            .on_receiving_result(answer(inbox, move |r| {
                Heard::SessionReady(r.map(|_| session))
            })),
        Act::SetModel { session, model } => connection
            .prepare_request(SetModelRequest {
                session_id: session,
                model_id: model,
            })
            .on_receiving_result(answer(inbox, |r| Heard::ModelSet(r.map(|_| ())))),
        Act::Prompt {
            key,
            session,
            ids,
            text,
        } => {
            connection
                .prepare_request(PromptRequest::new(
                    session,
                    vec![ContentBlock::Text(TextContent::new(text))],
                ))
                .on_receiving_result(answer(inbox, move |r: Result<PromptResponse, String>| {
                    Heard::Answered {
                        key,
                        stop: r.map(|r| stop_of(r.stop_reason)),
                    }
                }))?;
            let _ = observed.send(ChildObservation::TurnStarted { message_ids: ids });
            Ok(())
        }
        Act::Cancel { session } => connection.send_notification(CancelNotification::new(session)),
        Act::CloseInput => connection.send_notification(EndOfInput {}),
    }
}

/// A reply's handler: what it means, posted to the main task.
#[allow(clippy::type_complexity)]
fn answer<T: Send + 'static>(
    inbox: &UnboundedSender<Input>,
    heard: impl FnOnce(Result<T, String>) -> Heard + Send + 'static,
) -> impl FnOnce(
    Result<T, agent_client_protocol::Error>,
) -> futures::future::Ready<Result<(), agent_client_protocol::Error>>
       + Send
       + 'static {
    let inbox = inbox.clone();
    move |result| {
        // A reply the engine never gave (its output ended): the exit says
        // what became of the call.
        if let Err(error) = &result {
            if agent_client_protocol::is_incoming_transport_closed(error)
                || agent_client_protocol::is_connection_terminated(error)
            {
                return futures::future::ready(Ok(()));
            }
        }
        let heard = heard(result.map_err(|error| error.to_string()));
        let _ = inbox.unbounded_send(Input::Heard(heard));
        futures::future::ready(Ok(()))
    }
}

/// The text of an agent message's chunk; nothing else of an update is kept.
pub(crate) fn said_of(notification: SessionNotification) -> Option<Heard> {
    let SessionUpdate::AgentMessageChunk(chunk) = notification.update else {
        return None;
    };
    let ContentBlock::Text(text) = chunk.content else {
        return None;
    };
    Some(Heard::Said {
        message_id: chunk.message_id.map(|id| id.to_string()),
        text: text.text,
    })
}

pub(crate) fn stop_of(reason: StopReason) -> Stop {
    match reason {
        StopReason::EndTurn => Stop::EndTurn,
        StopReason::MaxTokens => Stop::MaxTokens,
        StopReason::MaxTurnRequests => Stop::MaxTurnRequests,
        StopReason::Refusal => Stop::Refusal,
        StopReason::Cancelled => Stop::Cancelled,
        // A reason a later schema adds: the turn did not end as asked.
        _ => Stop::Other,
    }
}

/// The answer to a permission request: the posture's option, once (never
/// "always": nothing outlives the request), else cancelled (no such option).
pub(crate) fn permission_answer(
    request: &RequestPermissionRequest,
    permissions: ChildPermissions,
) -> RequestPermissionOutcome {
    let kind = match permissions {
        ChildPermissions::Allow => PermissionOptionKind::AllowOnce,
        ChildPermissions::Refuse => PermissionOptionKind::RejectOnce,
    };
    request
        .options
        .iter()
        .find(|option| option.kind == kind)
        .map_or(RequestPermissionOutcome::Cancelled, |option| {
            RequestPermissionOutcome::Selected(SelectedPermissionOutcome::new(
                option.option_id.clone(),
            ))
        })
}

/// Wait for the engine's exit on a thread: its status, once the connection
/// has ended; a stdout a descendant still holds DRAIN_GRACE after the exit
/// is ended by killing the tree.
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
        let _ = told.send(crate::stream::bounded(text.trim()).to_string());
    });
    kept
}

/// The exit's detail: a wait that failed says so; else the start of stderr
/// (none when its reader did not finish).
pub(crate) fn detail_of(failure: String, stderr: Option<String>) -> String {
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
/// engine-claude does).
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
        let (queued, acts) = shared.conversation.send(message, urgency)?;
        shared.post(acts);
        Ok(queued)
    }

    fn interrupt(&self) -> Result<bool, ChildError> {
        let mut shared = lock(&self.shared);
        let (asked, acts) = shared.conversation.interrupt()?;
        shared.post(acts);
        Ok(asked)
    }

    fn clear(&self) -> Result<Queued, ChildError> {
        let mut shared = lock(&self.shared);
        let (queued, acts) = shared.conversation.clear()?;
        shared.post(acts);
        Ok(queued)
    }

    /// The turn in flight is cancelled and the input ended (opencode exits
    /// on it); only an engine still there after `grace` is stopped, so a
    /// busy server is never signalled first.
    fn end(&self, grace: Duration) -> Result<Ended, ChildError> {
        let undelivered = {
            let mut shared = lock(&self.shared);
            let (undelivered, acts) = shared.conversation.end()?;
            shared.post(acts);
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
    /// The engine's input is closed: it ends.
    fn drop(&mut self) {
        let mut shared = lock(&self.shared);
        let acts = shared.conversation.release();
        shared.post(acts);
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
