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
//! - `approval_prompt` is not offered until approvals are built (OD-1).

mod tools;

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

/// The noun's hidden verb.
pub const VERB: &str = "serve";

/// The name the server gives itself, and engines register it under.
pub const SERVER_NAME: &str = "ctxloom";

/// `mcp serve`, hidden: an engine starts it.
pub fn verb() -> clap::Command {
    clap::Command::new(VERB)
        .about("Serve ctxloom's MCP tools over stdio (an engine starts it)")
        .hide(true)
}

/// How a diagnostic is said to the engine: its words, as the binary's
/// catalog renders them.
pub type Say = Box<dyn Fn(&dyn Diagnostic) -> String + Send + Sync>;

/// The server over `caller`, saying itself as ctxloom of `version`.
pub struct Server<D> {
    caller: Arc<Caller<D>>,
    version: String,
    say: Say,
}

impl<D: Daemons + 'static> Server<D> {
    /// A tool's error is said as its code and args, until [`Server::saying`].
    pub fn new(caller: Caller<D>, version: &str) -> Server<D> {
        Server {
            caller: Arc::new(caller),
            version: version.to_string(),
            say: Box::new(|d| angzarr_diagnostic::Report::of(d).to_string()),
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
        _context: RequestContext<RoleServer>,
    ) -> Result<CallToolResponse, ErrorData> {
        let arguments = request.arguments.unwrap_or_default();
        let answered = tools::dispatch(&self.caller, &request.name, &arguments).await;
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

/// Serve `server` over this process's stdin, writing to `out` (its stdout,
/// which the command line holds locked for the whole command: the server's
/// lines reach it through a channel, written and flushed here, on this
/// thread), until its input ends.
// The process's own stdio: an acceptance test drives it (mcp_serve.test.rs).
#[cfg_attr(test, mutants::skip)]
pub fn serve<D>(server: Server<D>, out: &mut dyn std::io::Write) -> Result<(), McpError>
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
        runtime.block_on(async move {
            let transport = (tokio::io::stdin(), Lines(lines));
            let running = server
                .serve(transport)
                .await
                .map_err(|e| failed(e.to_string()))?;
            running.waiting().await.map_err(|e| failed(e.to_string()))?;
            Ok(())
        })
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
    vec![
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
    ]
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
