//! ctxloom's own MCP server (slice 3: T20, S45, S46; ruling 150's rmcp),
//! `ctxloom mcp serve`, hidden: an engine starts it over stdio, nobody types
//! it. It holds no store and no state: each tool is a call of the daemon's
//! client use cases for the session whose engine started it
//! ([`daemon_api::Caller`]), whose resource name is its identity, inherited
//! from the engine's environment (`CTXLOOM_SESSION`), never written in the
//! engine's MCP config.
//!
//! - Translation only (a driving adapter): arguments are read here, the
//!   call is the application's, and its answer is rendered as the
//!   resource's proto3 JSON (ruling 146), text content.
//! - `initialize` and `tools/list` are answered from the static list
//!   ([`tools`]), reaching no daemon: a call may wait for one to start
//!   (S16's bound), the engine's startup must not.
//! - Stdout is the protocol's alone; nothing else is written there.
//! - A call refused or failed is the tool's error (`isError`), its text
//!   the diagnostic's code and words; never a protocol error.
//! - `child_launch` launches the session's own child through the daemon's
//!   `CreateHandOff` (slice 4 U3, s4-Q1), its harp minted when it names
//!   none ([`daemon_api::Caller::naming`]);
//! - `approval_prompt` (step 8 U4, S27) is a claude engine's permission
//!   prompt: the ask recorded by the daemon (`CreateApproval`), waited on
//!   (`WaitApproval`, under a deadline of its own) and answered in claude's
//!   verified shape; a call claude cancels is withdrawn ([`approval`]).
//! - claude's `server/discover`, sent before `initialize`, is answered `{}`
//!   by the transport's reader, before rmcp sees it (P0 Q11).
//! - The terminal wake ([`wake`], owner ruling 2026-11-18): for a session
//!   whose engine exported its messaging socket to the server, the server
//!   watches its own session and posts a wake when a message would wake it.

pub mod approval;
mod elicit;
mod tools;
pub mod wake;

use std::fmt;
use std::sync::Arc;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use daemon_api::{Caller, Daemons};
use rmcp::model::{
    CallToolRequestParams, CallToolResponse, CallToolResult, ContentBlock, Implementation,
    ListToolsResult, PaginatedRequestParams, ServerCapabilities, ServerConfig,
};
use rmcp::service::RequestContext;
use rmcp::{ErrorData, RoleServer, ServerHandler, ServiceExt};

pub use tools::{tools, TOOL_NAMES};

/// How long the server's runtime is given, once its input ended, to let
/// go of what its tasks hold.
const SHUTDOWN_GRACE: std::time::Duration = std::time::Duration::from_secs(1);

/// The noun's hidden verb.
pub const VERB: &str = "serve";

/// The name the server gives itself, and engines register it under.
pub const SERVER_NAME: &str = "ctxloom";

/// `mcp serve`, hidden: an engine starts it.
pub fn verb() -> clap::Command {
    clap::Command::new(VERB)
        .about("Serve ctxloom's MCP tools over stdio (an engine starts it)")
        .hide(true)
        .arg(
            clap::Arg::new(NO_WAKE)
                .long(NO_WAKE)
                .action(clap::ArgAction::SetTrue)
                .help("Start without the terminal wake's poster (the daemon hosts the session)"),
        )
}

/// `--no-wake`: the server of a session the daemon hosts.
pub const NO_WAKE: &str = "no-wake";

/// Whether the server `serve`'s arguments describe has the terminal wake's
/// poster: a primary terminal session's has; a hosted child's, whose
/// deliveries are the daemon's, is constructed without it.
pub fn posts_wakes(serve: &clap::ArgMatches) -> bool {
    !serve.get_flag(NO_WAKE)
}

/// How a diagnostic is said to the engine: its words, as the binary's
/// catalog renders them.
pub type Say = Box<dyn Fn(&dyn Diagnostic) -> String + Send + Sync>;

/// The server over `caller`, saying itself as ctxloom of `version`.
pub struct Server<D> {
    caller: Arc<Caller<D>>,
    version: String,
    say: Say,
    /// The reasons claude gave for the calls it cancelled.
    cancels: approval::Cancels,
    /// The terminal wake's poster, started beside the protocol; none, the
    /// session is never woken (its engine exported no messaging socket).
    wakes: Option<wake::Wakes>,
}

impl<D: Daemons + 'static> Server<D> {
    /// A tool's error is said as its code and args, until [`Server::saying`].
    pub fn new(caller: Caller<D>, version: &str) -> Server<D> {
        Server {
            caller: Arc::new(caller),
            version: version.to_string(),
            say: Box::new(|d| angzarr_diagnostic::Report::of(d).to_string()),
            cancels: approval::Cancels::default(),
            wakes: None,
        }
    }

    /// This server, keeping its session awake through `wakes` (the
    /// terminal wake): its own session watched for as long as it serves.
    #[must_use]
    pub fn waking(self, wakes: wake::Wakes) -> Server<D> {
        Server {
            wakes: Some(wakes),
            ..self
        }
    }

    /// This server, a tool's error said by `say`.
    #[must_use]
    pub fn saying(self, say: Say) -> Server<D> {
        Server { say, ..self }
    }
}

/// A tool's error text: the diagnostic's code, then what `say` says of it.
pub(crate) fn error_text(say: &Say, diagnostic: &dyn Diagnostic) -> String {
    format!("{}: {}", diagnostic.code(), say(diagnostic))
}

impl<D> ServerHandler for Server<D>
where
    D: Daemons + Send + Sync + 'static,
    D::Api: Send + Sync,
{
    fn get_info(&self) -> ServerConfig {
        let mut implementation = Implementation::from_build_env();
        implementation.name = SERVER_NAME.to_string();
        implementation.version = self.version.clone();
        let mut info = ServerConfig::new(ServerCapabilities::builder().enable_tools().build());
        info.server_info = implementation;
        info
    }

    async fn list_tools(
        &self,
        _request: Option<PaginatedRequestParams>,
        _context: RequestContext<RoleServer>,
    ) -> Result<ListToolsResult, ErrorData> {
        Ok(ListToolsResult::with_all_items(tools()))
    }

    async fn call_tool(
        &self,
        request: CallToolRequestParams,
        context: RequestContext<RoleServer>,
    ) -> Result<CallToolResponse, ErrorData> {
        let arguments = request.arguments.unwrap_or_default();
        let answered = match request.name.as_ref() {
            tools::APPROVAL_PROMPT => self.prompt(&arguments, &context).await,
            name => tools::dispatch(&self.caller, &Engine(&context), name, &arguments).await,
        };
        Ok(match answered {
            Ok(texts) => {
                CallToolResult::success(texts.into_iter().map(ContentBlock::text).collect())
            }
            Err(error) => CallToolResult::error(vec![ContentBlock::text(error_text(
                &self.say,
                error.as_ref(),
            ))]),
        }
        .into())
    }
}

impl<D> Server<D>
where
    D: Daemons + Send + Sync + 'static,
    D::Api: Send + Sync,
{
    /// `approval_prompt`: the ask of `args` (`{tool_name, input,
    /// tool_use_id}`) recorded, waited on until it is settled, and answered
    /// as claude reads an answer; a call claude cancels meanwhile is
    /// withdrawn, and answered nothing (claude waits for no answer to it).
    async fn prompt(
        &self,
        args: &rmcp::model::JsonObject,
        context: &RequestContext<RoleServer>,
    ) -> Result<Vec<String>, tools::Refused> {
        let refused = |e: tools::ToolArgs| -> tools::Refused { Box::new(e) };
        let failed = |e: daemon_api::CallError| -> tools::Refused { Box::new(e) };
        let tool = tools::APPROVAL_PROMPT;
        let tool_name = tools::text(tool, args, "tool_name").map_err(refused)?;
        let request_id = tools::text(tool, args, "tool_use_id").map_err(refused)?;
        let input = args.get("input").cloned().ok_or_else(|| {
            refused(tools::ToolArgs::Missing {
                tool: tool.to_string(),
                argument: "input".to_string(),
            })
        })?;
        let asked = self
            .caller
            .create_approval(tool_name, &input.to_string(), request_id)
            .await
            .map_err(failed)?;
        let settled = match asked.state.as_str() {
            "waiting" => {
                let until = approval::wait_until(&asked);
                tokio::select! {
                    waited = self.caller.wait_approval(&asked.name, until) => waited.map_err(failed)?,
                    () = context.ct.cancelled() => {
                        let id = serde_json::to_value(&context.id)
                            .map(|id| id.to_string())
                            .unwrap_or_default();
                        let reason = self.cancels.reason(&id);
                        self.caller
                            .withdraw_approval(&asked, &reason)
                            .await
                            .map_err(failed)?;
                        return Ok(Vec::new());
                    }
                }
            }
            _ => asked,
        };
        Ok(vec![
            approval::answer_of(&settled, &input, &self.say).to_string()
        ])
    }
}

/// The engine of a call, asked through its MCP peer (step 10c U11).
struct Engine<'a>(&'a RequestContext<RoleServer>);

impl tools::Elicit for Engine<'_> {
    // The peer's own capability, which only a live client sets: the
    // acceptance tests drive it (mcp_serve.feature).
    #[cfg_attr(test, mutants::skip)]
    fn can(&self) -> bool {
        self.0
            .peer
            .supported_elicitation_modes()
            .contains(&rmcp::service::ElicitationMode::Form)
    }

    // A request over the live transport (see above).
    #[cfg_attr(test, mutants::skip)]
    async fn ask(&self, params: serde_json::Value) -> Option<(String, Option<serde_json::Value>)> {
        let params: rmcp::model::ElicitRequestParams = serde_json::from_value(params).ok()?;
        let answered = self.0.peer.create_elicitation(params).await.ok()?;
        let action = serde_json::to_value(&answered.action).ok()?;
        Some((action.as_str()?.to_string(), answered.content))
    }
}

/// Pump the engine's `input` into `feed`, a line at a time, as rmcp reads
/// it: claude's `server/discover` answered here (`answer` writes the line)
/// and not passed on, a `notifications/cancelled` heard by `cancels` and
/// passed on, anything else passed on as it came. `feed` is shut down when
/// the input ends.
pub(crate) async fn pump<R, W>(
    input: R,
    mut feed: W,
    answer: impl Fn(String),
    cancels: approval::Cancels,
) -> std::io::Result<()>
where
    R: tokio::io::AsyncRead + Unpin,
    W: tokio::io::AsyncWrite + Unpin,
{
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt};
    let mut lines = tokio::io::BufReader::new(input).lines();
    while let Some(line) = lines.next_line().await? {
        match approval::intercepted(&line) {
            approval::Intercepted::Answer(answered) => {
                answer(answered + "\n");
                continue;
            }
            approval::Intercepted::Cancelled { id, reason } => cancels.heard(id, reason),
            approval::Intercepted::Pass => {}
        }
        feed.write_all(line.as_bytes()).await?;
        feed.write_all(b"\n").await?;
        feed.flush().await?;
    }
    feed.shutdown().await
}

/// Serve `server` over this process's stdin, writing to `out` (its stdout,
/// which the command line holds locked for the whole command: the server's
/// lines reach it through a channel, written and flushed here, on this
/// thread), until its input ends.
// The process's own stdio: an acceptance test drives it (mcp_serve.test.rs).
#[cfg_attr(test, mutants::skip)]
pub fn serve<D>(mut server: Server<D>, out: &mut dyn std::io::Write) -> Result<(), McpError>
where
    D: Daemons + Send + Sync + 'static,
    D::Api: Send + Sync,
{
    let failed = |detail: String| McpError::Failed { detail };
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .map_err(|e| failed(e.to_string()))?;
    let (lines, written) = std::sync::mpsc::channel::<Vec<u8>>();
    let serving = std::thread::spawn(move || {
        let served = runtime.block_on(async move {
            // The terminal wake's poster, for the server's life.
            if let Some(wakes) = server.wakes.take() {
                tokio::spawn(wake::keep_awake(Arc::clone(&server.caller), wakes));
            }
            // The engine's input, read here first: `server/discover`
            // answered, a cancel heard, the rest passed to rmcp.
            let (feed, input) = tokio::io::duplex(1 << 16);
            let answers = lines.clone();
            let cancels = server.cancels.clone();
            tokio::spawn(pump(
                tokio::io::stdin(),
                feed,
                move |answered: String| {
                    let _ = answers.send(answered.into_bytes());
                },
                cancels,
            ));
            let transport = (input, Lines(lines));
            let running = server
                .serve(transport)
                .await
                .map_err(|e| failed(e.to_string()))?;
            running.waiting().await.map_err(|e| failed(e.to_string()))?;
            Ok(())
        });
        // What the wake's poster still holds (a call in flight, a blocking
        // read) is let go within a bound, never waited on: the server ends
        // with its input.
        runtime.shutdown_timeout(SHUTDOWN_GRACE);
        served
    });
    for bytes in written {
        out.write_all(&bytes)
            .and_then(|()| out.flush())
            .map_err(|e| failed(e.to_string()))?;
    }
    serving
        .join()
        .unwrap_or_else(|_| Err(failed("the server's thread panicked".to_string())))
}

/// The server's output, as bytes sent to the thread that holds stdout.
struct Lines(std::sync::mpsc::Sender<Vec<u8>>);

impl tokio::io::AsyncWrite for Lines {
    fn poll_write(
        self: std::pin::Pin<&mut Self>,
        _: &mut std::task::Context<'_>,
        bytes: &[u8],
    ) -> std::task::Poll<std::io::Result<usize>> {
        let sent = self.0.send(bytes.to_vec()).map(|()| bytes.len());
        std::task::Poll::Ready(sent.map_err(|_| std::io::ErrorKind::BrokenPipe.into()))
    }

    fn poll_flush(
        self: std::pin::Pin<&mut Self>,
        _: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        std::task::Poll::Ready(Ok(()))
    }

    fn poll_shutdown(
        self: std::pin::Pin<&mut Self>,
        _: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        std::task::Poll::Ready(Ok(()))
    }
}

/// Why the server did not serve.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum McpError {
    /// Started with no session to speak as (S45): it refuses rather than
    /// act as the user. `variable` is the one it reads.
    NoSession { variable: String },
    /// The protocol could not be served (the runtime, the transport).
    Failed { detail: String },
}

impl fmt::Display for McpError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for McpError {}

impl Diagnostic for McpError {
    fn code(&self) -> &str {
        match self {
            McpError::NoSession { .. } => code!("no-session"),
            McpError::Failed { .. } => code!("failed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            McpError::NoSession { variable } => vec![("variable", variable.as_str().into())],
            McpError::Failed { detail } => vec![("ext_err_txt", detail.as_str().into())],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every diagnostic of the crate, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let samples: Vec<Box<dyn Diagnostic>> = vec![
        Box::new(McpError::NoSession {
            variable: "CTXLOOM_SESSION".to_string(),
        }),
        Box::new(McpError::Failed {
            detail: "the transport closed".to_string(),
        }),
        Box::new(tools::ToolArgs::Missing {
            tool: "child_send".to_string(),
            argument: "text".to_string(),
        }),
        Box::new(tools::ToolArgs::Unknown {
            tool: "approval_prompt".to_string(),
        }),
        Box::new(tools::ToolArgs::Urgency {
            given: "shout".to_string(),
        }),
    ];
    samples.into_iter().chain(approval::samples()).collect()
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
