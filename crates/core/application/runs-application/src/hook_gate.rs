//! The gate the HOOK principal passes: core's [`Gate`] for ctxloom (owner
//! rulings 2026-11-08 item 7 and 2026-11-09 items 9, 16 and 17).
//!
//! A hook process of the interactive engine holds the run's hook bearer
//! (`<run-id>.<64 hex>`, [`run_ports::Bearer`]) and nothing else of the run:
//! no runner token, no model token. The gate
//!
//! - `identify`: parses the bearer, finds the run by its id, and matches the
//!   secret's SHA-256 against the `hook_credential_hash` the run's enqueue
//!   journaled, in constant time. A run the stream does not hold, a secret
//!   that does not match, a bearer that is not one, and a run that has ended
//!   (its credentials are revoked in the fold) are all UNAUTHENTICATED, and
//!   the caller is told no more than that;
//! - allows exactly the six capture-fact types as FACTS ([`hook_allowlist`]):
//!   not the runner's lifecycle facts, and no command of any type;
//! - `owns` only the SESSION stream of the run the bearer names;
//! - `still_valid` is false once the run has ended, so a token dies with its
//!   run on the very next request.

use std::sync::Arc;

use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{
    constant_time_eq, Allowlist, Arrival, BodyKind, Credential, Gate, Principal, Scheme,
    Unauthenticated,
};
use angzarr_embedded::v1::{Cover, Uuid};
use run_ports::{Bearer, Edge};

use crate::gate::{run_state, url};

/// Who a hook bearer identifies: the run it names and that run's session.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HookIdentity {
    /// The run stream's root.
    pub run: Uuid,
    /// The session stream's root: the one stream the hook may write to.
    pub session: Uuid,
}

/// What a credential may send: the message types it is scoped to, as FACTS
/// and as COMMANDS. One value, checked by one function ([`Scope::permits`]);
/// widening a credential later is adding to its scope, not changing the check.
/// Today the hook credential's scope is derived from its role in code
/// ([`Scope::capture`]); nothing of it is journaled.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Scope {
    facts: std::collections::BTreeSet<String>,
    commands: std::collections::BTreeSet<String>,
}

impl Scope {
    /// The hook role's scope: the six capture facts and nothing else.
    pub fn capture() -> Scope {
        [
            url::<sessions::RecordPromptSubmitted>(),
            url::<sessions::RecordAssistantText>(),
            url::<sessions::RecordToolCall>(),
            url::<sessions::RecordTurnEnded>(),
            url::<sessions::RecordSessionEnded>(),
            url::<sessions::RecordCompaction>(),
        ]
        .into_iter()
        .fold(Scope::default(), Scope::with_fact)
    }

    /// This scope and `type_url` as a fact.
    pub fn with_fact(mut self, type_url: String) -> Scope {
        self.facts.insert(type_url);
        self
    }

    /// This scope and `type_url` as a command.
    #[cfg_attr(not(test), allow(dead_code))] // widening is a later landing's
    pub fn with_command(mut self, type_url: String) -> Scope {
        self.commands.insert(type_url);
        self
    }

    #[cfg_attr(not(test), allow(dead_code))] // the allowlist is what the coordinator enforces
    /// Whether a body of `kind` and `type_url` is within the scope.
    pub fn permits(&self, kind: BodyKind, type_url: &str) -> bool {
        match kind {
            BodyKind::Fact => self.facts.contains(type_url),
            BodyKind::Command => self.commands.contains(type_url),
        }
    }

    /// The allowlist the coordinator enforces for this scope.
    pub fn allowlist(&self) -> Allowlist {
        let facts = self.facts.iter().map(|u| (BodyKind::Fact, u));
        let commands = self.commands.iter().map(|u| (BodyKind::Command, u));
        facts
            .chain(commands)
            .fold(Allowlist::none(), |list, (kind, type_url)| {
                list.send(kind, type_url.clone())
            })
    }
}

/// What the hook principal may send: the six capture facts, as FACTS, and
/// nothing else (no command, no other fact, no read).
pub fn hook_allowlist() -> Allowlist {
    Scope::capture().allowlist()
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
            Ok(Principal::new(identity, Scope::capture().allowlist()))
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
