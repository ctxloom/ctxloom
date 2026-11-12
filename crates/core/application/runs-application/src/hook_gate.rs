//! The gate of a CLI run's own listener: core's [`Gate`] for ctxloom (owner
//! rulings 2026-11-08 item 7 and 2026-11-09 items 9, 16 and 17, as the
//! separation model of slice 2 left them).
//!
//! A hook process of a run's engine (interactive or one-shot) holds the
//! run's hook bearer (`<run-id>.<64 hex>`, [`run_ports::Bearer`]). It is that
//! listener's authentication, kept while the CLI serves its own runs: it
//! keeps NON-USER callers off the socket, and separates nothing among the
//! user's processes (one principal: whoever presents it is the user, with
//! the user's allowlist). The gate
//!
//! - `identify`: parses the bearer, finds the run by its id, and matches the
//!   secret's SHA-256 against the `hook_credential_hash` the run's enqueue
//!   journaled, in constant time. A run the stream does not hold, a secret
//!   that does not match, a bearer that is not one, and a run that has ended
//!   (its credentials are revoked in the fold) are all UNAUTHENTICATED, and
//!   the caller is told no more than that;
//! - allows what the user may send ([`crate::user_allowlist`]);
//! - `owns` only the SESSION stream of the run the bearer names: an
//!   addressing check (a fact's session must be its run's), not a permission;
//! - `still_valid` is false once the run has ended, so a token dies with its
//!   run on the very next request (a bound on how long a harvested bearer is
//!   good for).

use std::sync::Arc;

use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{
    constant_time_eq, Arrival, Credential, Gate, Principal, Scheme, Unauthenticated,
};
use angzarr_embedded::v1::{Cover, Uuid};
use run_ports::{Bearer, Edge};

use crate::gate::run_state;
use crate::user_gate::user_allowlist;

/// Who a hook bearer identifies: the run it names and that run's session.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HookIdentity {
    /// The run stream's root.
    pub run: Uuid,
    /// The session stream's root: the one stream the hook may write to.
    pub session: Uuid,
}

/// The run's hook credential, gated: reads the run's stream through the edge
/// on every request, so an end takes effect at once.
pub struct HookGate<E: Edge> {
    edge: Arc<E>,
}

impl<E: Edge> HookGate<E> {
    pub fn new(edge: Arc<E>) -> HookGate<E> {
        HookGate { edge }
    }

    /// The run of `bearer`, if its secret is the run's live hook credential.
    async fn resolve(&self, credential: &Credential) -> Option<HookIdentity> {
        if credential.scheme() != Scheme::Bearer {
            return None;
        }
        let text = std::str::from_utf8(credential.expose_secret()).ok()?;
        let (run_text, token) = Bearer::parse(text).ok()?;
        let run = uuid::Uuid::parse_str(&run_text).ok()?;
        let run = Uuid::from_bytes(*run.as_bytes());
        let state = run_state(&*self.edge, &run).await.ok()?;
        // No run by that id: nothing to match against.
        state.mode?;
        // The secret's digest against the journaled one: constant time, and a
        // stored digest of any other length never matches.
        if !constant_time_eq(token.hash().as_bytes(), &state.hook_credential_hash) {
            return None;
        }
        if state.credential_revoked {
            return None;
        }
        let session = uuid::Uuid::parse_str(&state.session).ok()?;
        Some(HookIdentity {
            run,
            session: Uuid::from_bytes(*session.as_bytes()),
        })
    }
}

impl<E: Edge> Gate for HookGate<E> {
    type Identity = HookIdentity;

    fn identify<'a>(
        &'a self,
        arrival: Arrival,
        credential: Option<&'a Credential>,
    ) -> BoxFuture<'a, Result<Principal<HookIdentity>, Unauthenticated>> {
        // The arrival adds nothing: the socket is this machine's, and the
        // domains it reaches are the caller's session's alone (`owns`).
        let _ = arrival;
        Box::pin(async move {
            let credential = credential.ok_or(Unauthenticated)?;
            let identity = self.resolve(credential).await.ok_or(Unauthenticated)?;
            Ok(Principal::new(identity, user_allowlist()))
        })
    }

    fn owns(&self, identity: &HookIdentity, cover: &Cover) -> bool {
        cover.domain == sessions::SESSION_DOMAIN
            && cover.root.as_ref() == Some(&identity.session)
            && cover.edition.is_none()
    }

    fn still_valid<'a>(&'a self, identity: &'a HookIdentity) -> BoxFuture<'a, bool> {
        Box::pin(async move {
            // A run that cannot be read is not vouched for.
            run_state(&*self.edge, &identity.run)
                .await
                .is_ok_and(|state| state.mode.is_some() && !state.credential_revoked)
        })
    }
}

#[cfg(test)]
#[path = "hook_gate.test.rs"]
mod tests;
