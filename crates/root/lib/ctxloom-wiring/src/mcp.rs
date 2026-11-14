//! `ctxloom mcp serve` composed (slice 3, T20): ctxloom's MCP server over
//! the session the engine's environment names (`CTXLOOM_SESSION`, s3-Q3),
//! reaching the user's daemon as every client does (its discovery file
//! under ctxloom's home, `CTXLOOM_HOME`; one spawned and waited for when
//! none serves, S16), but NEVER stopping one (OD-3: a daemon of another
//! version is a typed refusal of the call, never stopped under the
//! sessions it hosts).

use std::sync::Arc;

use angzarr_presentation::Catalog;
use cli_support::Exit;
use ctxloom_cli::Failure;
use daemon_api::{Caller, Daemons, Unreached};
use process_env::EnvSnapshot;

use crate::Wiring;

/// The noun the verb joins.
pub(crate) const NOUN: &str = "mcp";

/// Whether `noun verb` is `mcp serve`.
pub(crate) fn is_serve(noun: &str, verb: &str) -> bool {
    noun == NOUN && verb == mcp_server::VERB
}

/// `mcp serve`: this process serves the session `CTXLOOM_SESSION` names
/// until its input ends. With none named it is refused (exit 20), never
/// served as the user.
pub(crate) fn serve(
    env: &Arc<EnvSnapshot>,
    stamp: &str,
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
    let caller = Caller::new(
        SessionReach {
            wiring: Arc::new(wiring),
            stamp: stamp.to_string(),
        },
        &session,
        crate::daemon::env_vars(env),
        Box::new(crate::daemon::request_id),
    )
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
    mcp_server::serve(server, out).map_err(|e| Failure::new(e, Exit::FAILED))?;
    Ok(Exit::OK)
}

/// The messages a tool's error is said in: the wiring's sources, which hold
/// every crate a call can come to (the daemon's client and its refusals,
/// the sessions domain's, the server's own).
fn tool_catalog() -> Result<Catalog, angzarr_presentation::CatalogError> {
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
