//! The user daemon's gate (slice 2, the separation model): ONE principal,
//! the user. A caller is admitted when it presents the user bearer the
//! daemon minted at its start (T12, read from the 0600 discovery file);
//! the daemon keeps only its SHA-256, in memory, and compares in constant
//! time. Anything else (no credential, another bearer, a run's hook bearer,
//! another scheme) is a bare UNAUTHENTICATED that says nothing of why
//! (S11).
//!
//! Authenticated means the user: the principal may send every command and
//! fact the session domain handles (angzarr's allowlist is a closed list, so
//! it is that domain's whole list, read from the aggregate), and owns every
//! stream the daemon reaches. The bearer keeps NON-USER callers off the
//! socket (other users, web pages, containers, machines); it separates
//! nothing among the user's own processes, which can all read the file.

use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{Allowlist, Arrival, Credential, Gate, Principal, Unauthenticated};
use angzarr_embedded::v1::Cover;
use run_ports::{Token, TokenHash};

/// Who the daemon's gate admits: the user, the one principal.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct User;

/// The gate over the user bearer's digest. Its `Debug` shows no secret: it
/// holds none.
#[derive(Clone, Debug)]
pub struct UserGate {
    digest: TokenHash,
}

impl UserGate {
    /// The gate of the daemon that minted `bearer`; only its digest is kept.
    pub fn new(bearer: &Token) -> UserGate {
        UserGate {
            digest: bearer.hash(),
        }
    }
}

/// What the user may send: every command and fact of the session domain.
pub fn user_allowlist() -> Allowlist {
    Allowlist::none()
}

impl Gate for UserGate {
    type Identity = User;

    fn identify<'a>(
        &'a self,
        arrival: Arrival,
        credential: Option<&'a Credential>,
    ) -> BoxFuture<'a, Result<Principal<User>, Unauthenticated>> {
        let _ = (arrival, credential, &self.digest);
        Box::pin(async { Err(Unauthenticated) })
    }

    fn owns(&self, identity: &User, cover: &Cover) -> bool {
        let _ = (identity, cover);
        false
    }
}

#[cfg(test)]
#[path = "user_gate.test.rs"]
mod tests;
