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
use angzarr_coordinator::{
    Allowlist, Arrival, BodyKind, Credential, Gate, Principal, Scheme, Unauthenticated,
};
use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Domain, Payload};
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
    let commands = <sessions::SessionAggregate as Domain>::Command::type_urls();
    let facts = <sessions::SessionAggregate as Domain>::Fact::type_urls();
    let commands = commands.into_iter().map(|url| (BodyKind::Command, url));
    let facts = facts.into_iter().map(|url| (BodyKind::Fact, url));
    commands
        .chain(facts)
        .fold(Allowlist::none(), |list, (kind, url)| list.send(kind, url))
}

impl UserGate {
    /// Whether `credential` is the user bearer: its text is a token whose
    /// digest is this gate's, compared in constant time.
    fn is_the_user(&self, credential: &Credential) -> bool {
        credential.scheme() == Scheme::Bearer
            && std::str::from_utf8(credential.expose_secret())
                .ok()
                .and_then(|text| Token::from_text(text).ok())
                .is_some_and(|token| self.digest.matches(&token))
    }
}

impl Gate for UserGate {
    type Identity = User;

    fn identify<'a>(
        &'a self,
        arrival: Arrival,
        credential: Option<&'a Credential>,
    ) -> BoxFuture<'a, Result<Principal<User>, Unauthenticated>> {
        // The arrival adds nothing: the listener binds loopback alone.
        let _ = arrival;
        let admitted = credential.is_some_and(|credential| self.is_the_user(credential));
        Box::pin(async move {
            match admitted {
                true => Ok(Principal::new(User, user_allowlist())),
                false => Err(Unauthenticated),
            }
        })
    }

    /// Every stream the daemon reaches is the user's.
    fn owns(&self, identity: &User, cover: &Cover) -> bool {
        let _ = (identity, cover);
        true
    }
}

#[cfg(test)]
#[path = "user_gate.test.rs"]
mod tests;
