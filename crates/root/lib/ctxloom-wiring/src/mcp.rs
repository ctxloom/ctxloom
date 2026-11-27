//! `ctxloom mcp serve` composed (slice 3, T20): ctxloom's MCP server over
//! the session the engine's environment names (`CTXLOOM_SESSION`, s3-Q3),
//! reaching the user's daemon as every client does (its discovery file
//! under ctxloom's home, `CTXLOOM_DAEMON_ROOT`; one spawned and waited for when
//! none serves, S16), but NEVER stopping one (OD-3: a daemon of another
//! version is a typed refusal of the call, never stopped under the
//! sessions it hosts).

use std::sync::Arc;

use cli_support::Exit;
use ctxloom_cli::Failure;
use daemon_api_client::{Caller, Daemons, Unreached};
use presentation::Catalog;
use process_env::EnvSnapshot;

use crate::Wiring;

/// The noun the verb joins.
pub const NOUN: &str = "mcp";

/// Whether `noun verb` is `mcp serve`.
pub fn is_serve(noun: &str, verb: &str) -> bool {
    noun == NOUN && verb == mcp_server::VERB
}

/// `mcp serve`: this process serves the session `CTXLOOM_SESSION` names
/// until its input ends. With none named it is refused (exit 48), never
/// served as the user.
pub fn serve(
    env: &Arc<EnvSnapshot>,
    stamp: &str,
    wakes: bool,
    out: &mut dyn std::io::Write,
) -> Result<Exit, Failure> {
    let Some(session) = env.non_empty(run_ports::SESSION_VARIABLE) else {
        return Err(Failure::new(
            mcp_server::McpError::NoSession {
                variable: run_ports::SESSION_VARIABLE.to_string(),
            },
            Exit::REFUSED,
        ));
    };
    let session = session.to_string_lossy().into_owned();
    let wiring = Wiring::new(Arc::clone(env)).map_err(|e| Failure::new(e, Exit::FAILED))?;
    let locks = crate::daemon::launch_locks(&wiring);
    // `child_launch`'s `project`: relative to the session's own project
    // root, `CTXLOOM_ROOT` (step 8 Q-C), never the engine's working
    // directory, which may be a scratch checkout; none set, the server's.
    let base = env
        .non_empty(run_ports::ROOT_VARIABLE)
        .map(std::path::PathBuf::from)
        .or_else(|| env.cwd().ok())
        .unwrap_or_default();
    let resolver = crate::daemon::project_resolver(&wiring, base);
    let caller = Caller::new(
        SessionReach {
            wiring: Arc::new(wiring),
            stamp: stamp.to_string(),
        },
        &session,
        crate::daemon::caller_of(env, daemon_proto::v1::client::Kind::Mcp, stamp),
        Box::new(crate::daemon::request_id),
    )
    // `child_launch` names a child that names none, and holds its launch
    // lock while it is made (slice 4 U3).
    .map(|caller| {
        caller
            .naming(Box::new(crate::session_name))
            .holding(locks)
            .resolving(resolver)
    })
    .map_err(|e| {
        let exit = cli_support::ExitClass::exit(&e);
        Failure::new(e, exit)
    })?;
    let catalog = tool_catalog().map_err(|e| {
        Failure::new(
            mcp_server::McpError::Failed {
                detail: e.to_string(),
            },
            Exit::FAILED,
        )
    })?;
    let server =
        mcp_server::Server::new(caller, stamp).saying(Box::new(move |d| catalog.message(d)));
    // A hosted child is fed its messages as turns, never as elements.
    let server = if wakes {
        server
    } else {
        server.without_message_notes()
    };
    // The terminal wake: where claude exported its messaging socket to this
    // server (its engine started it), the server keeps its session awake.
    let exported = |name: &str| {
        env.non_empty(name)
            .map(|value| value.to_string_lossy().into_owned())
    };
    // Only a server constructed with the poster (a terminal session's) has
    // one; a hosted child's deliveries are the daemon's.
    let socket = engine_claude::messaging_wake(&exported).filter(|_| wakes);
    let server = match socket {
        Some(socket) => server.waking(mcp_server::wake::Wakes {
            post: Arc::new(socket),
            mint: Box::new(nonce),
        }),
        None => server,
    };
    mcp_server::serve(server, out).map_err(|e| Failure::new(e, Exit::FAILED))?;
    Ok(Exit::OK)
}

/// A wake's nonce: the random tail of a fresh v7 id, 16 hex digits.
fn nonce() -> String {
    let id = uuid::Uuid::now_v7().simple().to_string();
    id[id.len() - run_ports::NONCE_DIGITS..].to_string()
}

/// The messages a tool's error is said in: the wiring's sources, which hold
/// every crate a call can come to (the daemon's client and its refusals,
/// the sessions domain's, the server's own).
fn tool_catalog() -> Result<Catalog, presentation::CatalogError> {
    Catalog::en_us(&crate::catalog_sources())
}

/// The user's daemon as a session's MCP server reaches it: the CLI's reach
/// (S14, S16), owned, so the server holds it for its life; a daemon of
/// another version is refused, never stopped (OD-3).
struct SessionReach {
    wiring: Arc<Wiring>,
    stamp: String,
}

impl Daemons for SessionReach {
    type Api = daemon_client::DaemonClient;

    async fn serving(&self) -> Result<Option<daemon_client::DaemonClient>, Unreached> {
        crate::daemon::Reach::new(&self.wiring, &self.stamp)
            .never_stopping()
            .serving()
            .await
    }

    async fn reach(&self) -> Result<daemon_client::DaemonClient, Unreached> {
        crate::daemon::Reach::new(&self.wiring, &self.stamp)
            .never_stopping()
            .reach()
            .await
    }
}

#[cfg(test)]
#[path = "mcp.test.rs"]
mod tests;
