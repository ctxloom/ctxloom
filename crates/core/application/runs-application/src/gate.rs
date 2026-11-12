//! What the gates and the use cases share: a run's state folded from its
//! stream, a message's type URL, and `owns`, the addressing predicate (a
//! caller's own run and session). The gates themselves are angzarr's `Gate`
//! ([`crate::UserGate`], [`crate::HookGate`]); the local gate, its two
//! principals and their allowlists are gone (slice 2, the separation model:
//! one principal, the user).

use angzarr_embedded::v1::{event_page, Cover, EventPage, Uuid};
use angzarr_embedded::{Payload, StoreError};
use run_ports::Edge;

use crate::error::AppError;
use crate::identity::Identity;

/// The type URL of a domain message's wire form.
#[cfg(test)]
pub(crate) fn url<M: angzarr_embedded::markup::DomainMessage>() -> String {
    <M::Wire as prost::Name>::type_url()
}

/// A page's event as `E`, if it is one.
fn decode<E: Payload>(page: &EventPage) -> Option<E> {
    match page.body.as_ref()? {
        event_page::Body::Event(event) => E::from_any(event).ok(),
        event_page::Body::MetaEvent(_) => None,
    }
}

/// The run rooted at `run`, folded from its stream as the aggregate folds it:
/// the default (no mode) when the stream holds none.
pub(crate) async fn run_state<E: Edge>(edge: &E, run: &Uuid) -> Result<runs::RunState, AppError> {
    let cover = Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(run.clone()),
        edition: None,
    };
    let pages = edge.events(&cover, 0).await.map_err(failed)?;
    let mut state = runs::RunState::default();
    for page in &pages {
        if let Some(event) = decode::<runs::RunEvent>(page) {
            runs::apply(&mut state, &event, page.sequence);
        }
    }
    Ok(state)
}

/// A store that could not be read.
pub(crate) fn failed(error: StoreError) -> AppError {
    AppError::Failed {
        reason: error.to_string(),
    }
}

/// Whether `identity` owns the stream `cover` names: its own run, its own
/// session. A pure addressing predicate (listener plan 4.5), not a
/// permission.
pub fn owns(identity: &Identity, cover: &Cover) -> bool {
    *cover == identity.run_cover() || *cover == identity.session_cover()
}
