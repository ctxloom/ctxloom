//! The user daemon's gate (D25, S-f): a run's hook credential, as
//! [`HookGate`](crate::HookGate) admits it, widened by the ORCHESTRATION
//! scope. The owner agent (the orchestrator) holds its run's hook bearer; it
//! may send the orchestration commands (`SendMessage`, `CloseSession`,
//! `ClearChild`, `LaunchChild`) to its own session's stream and to the
//! streams of the children it holds, and nothing else beyond the capture
//! facts.
//!
//! "Only its own children" is the DOMAIN's rule (S-f): the gate stamps the
//! requester, the identity's own session as a `SessionRef`, into every
//! command the owner rule guards ([`crate::stamped`]), whatever the payload
//! said, and the session aggregate refuses `NotOwner`. The gate's own share
//! is the command types and the streams it may reach (its own, its
//! children's: the owner's folded `children`, read at each identify).
//!
//! An accepted residual: the owner's credential reaches its children's
//! streams for the capture facts too, so an owner agent can forge capture
//! facts into its own child's transcript (the same user, ruling 37; the
//! child is the owner's to direct).

use std::sync::Arc;

use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{Arrival, Credential, Gate, Principal, Unauthenticated};
use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, event_page, CommandBook, Cover, Uuid};
use angzarr_embedded::{Any, Domain, PageContext, Payload};
use run_ports::Edge;
use sessions::{
    ClearChild, CloseSession, LaunchChild, SendMessage, SessionAggregate, SessionCommand,
    SessionEvent, SessionRef, SessionState,
};

use crate::gate::url;
use crate::hook_gate::{HookGate, HookIdentity, Scope};

/// Who the daemon's gate admits: the run's hook identity, the session it
/// speaks for, and the children that session holds (their stream roots).
#[derive(Clone, Debug, PartialEq)]
pub struct OrchestratorIdentity {
    pub hook: HookIdentity,
    /// The session as the owner rule names it: its project and id.
    pub requester: SessionRef,
    /// The roots of the session streams of the children it holds.
    pub children: Vec<Uuid>,
}

/// The orchestration scope (D25): the capture facts, and the four commands
/// the orchestrator sends.
pub fn orchestration_scope() -> Scope {
    [
        url::<SendMessage>(),
        url::<CloseSession>(),
        url::<ClearChild>(),
        url::<LaunchChild>(),
    ]
    .into_iter()
    .fold(Scope::capture(), Scope::with_command)
}

/// The session stream rooted at `session`, folded.
async fn session_state<E: Edge>(edge: &E, session: &Uuid) -> Option<SessionState> {
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(session.clone()),
        edition: None,
    };
    let pages = edge.events(&cover, 0).await.ok()?;
    let mut folded = SessionAggregate::default();
    for page in &pages {
        let Some(event_page::Body::Event(event)) = &page.body else {
            continue;
        };
        let at = PageContext {
            cover: Some(&cover),
            sequence: page.sequence,
            meta: page.meta.as_ref(),
        };
        SessionAggregate::apply(&mut folded, &SessionEvent::from_any(event).ok()?, &at);
    }
    Some(folded.state().clone())
}

/// A session id's text as a stream root.
fn root_of(session: &str) -> Option<Uuid> {
    let id = uuid::Uuid::parse_str(session).ok()?;
    Some(Uuid::from_bytes(*id.as_bytes()))
}

/// The run's hook credential, gated, with the orchestration scope.
pub struct OrchestratorGate<E: Edge> {
    edge: Arc<E>,
    hooks: HookGate<E>,
}

impl<E: Edge> OrchestratorGate<E> {
    pub fn new(edge: Arc<E>) -> OrchestratorGate<E> {
        OrchestratorGate {
            hooks: HookGate::new(Arc::clone(&edge)),
            edge,
        }
    }
}

/// `book` with the requester stamped into its session command, if it holds
/// one the owner rule guards; any other book as it was.
pub fn stamp_book(book: CommandBook, requester: &SessionRef) -> CommandBook {
    let mut book = book;
    let Some(page) = book.page.as_mut() else {
        return book;
    };
    let Some(command_page::Body::Command(command)) = page.body.as_mut() else {
        return book;
    };
    let Some(payload) = command.payload.as_ref() else {
        return book;
    };
    let Some(decoded) = decode(payload) else {
        return book;
    };
    let stamped = crate::stamped(decoded, requester);
    if let Some(any) = encode(&stamped) {
        command.payload = Some(any);
    }
    book
}

/// The guarded session command `payload` holds.
fn decode(payload: &Any) -> Option<SessionCommand> {
    fn one<M: DomainMessage + Into<SessionCommand>>(payload: &Any) -> Option<SessionCommand> {
        if payload.type_url != url::<M>() {
            return None;
        }
        let wire = payload.to_msg::<M::Wire>().ok()?;
        M::from_wire(wire).ok().map(Into::into)
    }
    one::<SendMessage>(payload)
        .or_else(|| one::<CloseSession>(payload))
        .or_else(|| one::<ClearChild>(payload))
        .or_else(|| one::<LaunchChild>(payload))
}

/// The wire form of a stamped command.
fn encode(command: &SessionCommand) -> Option<Any> {
    match command {
        SessionCommand::SendMessage(m) => Any::from_msg(&m.to_wire()).ok(),
        SessionCommand::CloseSession(m) => Any::from_msg(&m.to_wire()).ok(),
        SessionCommand::ClearChild(m) => Any::from_msg(&m.to_wire()).ok(),
        SessionCommand::LaunchChild(m) => Any::from_msg(&m.to_wire()).ok(),
        _ => None,
    }
}

impl<E: Edge> Gate for OrchestratorGate<E> {
    type Identity = OrchestratorIdentity;

    fn identify<'a>(
        &'a self,
        arrival: Arrival,
        credential: Option<&'a Credential>,
    ) -> BoxFuture<'a, Result<Principal<OrchestratorIdentity>, Unauthenticated>> {
        Box::pin(async move {
            let hook = self.hooks.identify(arrival, credential).await?;
            let hook = hook.identity().clone();
            let state = session_state(&*self.edge, &hook.session)
                .await
                .ok_or(Unauthenticated)?;
            let session = uuid::Uuid::from_slice(&hook.session.value)
                .map(|id| id.hyphenated().to_string())
                .map_err(|_| Unauthenticated)?;
            let children = state
                .children
                .iter()
                .filter_map(|child| child.session.as_ref())
                .filter(|child| child.project_id == state.project_id)
                .filter_map(|child| root_of(&child.session))
                .collect();
            let identity = OrchestratorIdentity {
                requester: SessionRef {
                    project_id: state.project_id.clone(),
                    session,
                },
                children,
                hook,
            };
            Ok(Principal::new(identity, orchestration_scope().allowlist()))
        })
    }

    fn owns(&self, identity: &OrchestratorIdentity, cover: &Cover) -> bool {
        let reaches =
            |root: &Uuid| *root == identity.hook.session || identity.children.contains(root);
        cover.domain == sessions::SESSION_DOMAIN
            && cover.root.as_ref().is_some_and(reaches)
            && cover.edition.is_none()
    }

    fn stamp(&self, identity: &OrchestratorIdentity, book: CommandBook) -> CommandBook {
        stamp_book(book, &identity.requester)
    }

    fn still_valid<'a>(&'a self, identity: &'a OrchestratorIdentity) -> BoxFuture<'a, bool> {
        self.hooks.still_valid(&identity.hook)
    }
}

#[cfg(test)]
#[path = "orchestrate_gate.test.rs"]
mod tests;
