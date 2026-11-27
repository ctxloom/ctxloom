//! The client a request names (owner, 2026-11-18): every request made on a
//! user's behalf carries one `Caller` (`daemon_proto::Carries`), and its
//! `client` says which program asked. The daemon admits only a client of its
//! own version, whatever its kind (the CLI, `mcp serve`, a hook): one
//! ctxloom version runs at a time (OD-61/OD-62), so a client of another
//! version is refused `ClientVersionMismatch`, its fix `ctxloom daemon stop`.
//! A launch runs the client's program for its hooks and leads its engine's
//! `PATH` with the client's directory ([`program_of`]).

use daemon_proto::v1;

use daemon_api_contract::{ApiError, ApiRefusal};

/// Admit a request whose caller is `caller` to the daemon whose stamp is
/// `own`: a client of the same stamp. A request that names no caller, or a
/// caller no client, names no version, which is not the daemon's.
pub fn admit(own: &str, caller: Option<&v1::Caller>) -> Result<(), ApiError> {
    let client = caller.and_then(|caller| caller.client.as_ref());
    let version = client.map_or("", |client| client.version.as_str());
    if version == own {
        return Ok(());
    }
    Err(ApiError::Refused(ApiRefusal::of(
        &v1::ClientVersionMismatch {
            client_version: version.to_string(),
            daemon_version: own.to_string(),
            kind: client.map_or(0, |client| client.kind),
        },
    )))
}

/// The program a launch asked by `client` runs (its hooks, its statusline)
/// and leads its engine's `PATH` with: the client's, when it names one; none,
/// the daemon's own.
pub fn program_of(client: Option<&v1::Client>) -> Option<daemon_application::Program> {
    client
        .filter(|client| !client.program.is_empty())
        .map(|client| daemon_application::Program {
            name: client.program.clone(),
            dir: client.program_dir.clone(),
        })
}

#[cfg(test)]
#[path = "admit.test.rs"]
mod tests;
