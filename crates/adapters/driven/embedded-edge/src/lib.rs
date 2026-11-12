//! The driven adapter that implements `run_ports::Edge` over
//! `angzarr_embedded_host::App` (the owner approved the in-process slice; no
//! transport). The conversions are pure and tested here; the
//! whole path over a real `App` is `ctxloom-wiring`'s `core_link` test, because a
//! driven adapter sees ports and never a use case.
//!
//! angzarr-embedded stays reusable tooling: nothing of ctxloom (session, run,
//! bundle, approval) appears in this crate's signatures.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::v1::{CommandBook, Cover, EventBook, EventPage};
use angzarr_embedded::{Answered, Failure, Handled, KernelError, Rejected, Sampled, StoreError};
use angzarr_embedded_host::config::Expose;
use angzarr_embedded_host::stores::Stores;
use angzarr_embedded_host::App;
use run_ports::{Arrival, Edge, SyncAnswered, Unserved};

/// The host's arrival for the core's.
pub fn host_arrival(arrival: Arrival) -> angzarr_embedded_host::Arrival {
    match arrival {
        Arrival::Loopback => angzarr_embedded_host::Arrival::Loopback,
        Arrival::Ip => angzarr_embedded_host::Arrival::Ip,
    }
}

/// The core's `Unserved` for the host's.
pub fn unserved(unserved: angzarr_embedded_host::Unserved) -> Unserved {
    match unserved {
        angzarr_embedded_host::Unserved::Hidden { hidden, .. } => Unserved::Hidden {
            domain: hidden.domain,
        },
        angzarr_embedded_host::Unserved::Failed(failure) => Unserved::Failed(failure),
    }
}

/// What `Unserved`-free handling answers: the addressed stream's events (an
/// empty book when the chain wrote none to it) and the delivery outcome, as
/// the exposure seam answers.
fn answered(handled: Handled, cover: Option<Cover>) -> Answered {
    let events = handled
        .books()
        .into_iter()
        .find(|book| book.cover == cover)
        .unwrap_or(EventBook {
            cover,
            ..EventBook::default()
        });
    Answered {
        events: Some(events),
        delivery: handled.delivery(),
    }
}

/// An application's edge: `serve` is `App::serve_in`, `handle` is
/// `App::handle_in`, `events` is `App::events_in`, each on the one store key;
/// `serve_sync` is the kernel's `handle_sync` (T16) behind the same exposure
/// as `serve`, which the edge is told ([`EmbeddedEdge::exposed`]: the host
/// keeps its own map to itself).
pub struct EmbeddedEdge<R: Stores<Key = ()>> {
    app: Arc<App<R>>,
    sample: Box<dyn Fn() -> Sampled + Send + Sync>,
    exposed: BTreeMap<String, Expose>,
}

impl<R: Stores<Key = ()>> EmbeddedEdge<R> {
    /// An edge over `app`; `sample` gives each call its instant and command id
    /// (the composition root's clock and ids).
    pub fn new(
        app: Arc<App<R>>,
        sample: impl Fn() -> Sampled + Send + Sync + 'static,
    ) -> EmbeddedEdge<R> {
        EmbeddedEdge {
            app,
            sample: Box::new(sample),
            exposed: BTreeMap::new(),
        }
    }

    /// The edge, its SIMPLE requests served behind `exposed`: the exposure
    /// the application was built with. A domain not in it is exposed
    /// `"none"`, as the host's own seam says.
    #[must_use]
    pub fn exposed(mut self, exposed: BTreeMap<String, Expose>) -> EmbeddedEdge<R> {
        self.exposed = exposed;
        self
    }
}

/// The domain of `book` where `arrival` may not reach it under `exposed`
/// (hidden, as the host's exposure seam hides it); `None` where it may.
fn hidden(
    exposed: &BTreeMap<String, Expose>,
    book: &CommandBook,
    arrival: Arrival,
) -> Option<String> {
    let cover = book.cover.as_ref()?;
    let expose = exposed.get(&cover.domain).copied().unwrap_or_default();
    match host_arrival(arrival).admitted_by(expose) {
        true => None,
        false => Some(cover.domain.clone()),
    }
}

impl<R: Stores<Key = ()>> Edge for EmbeddedEdge<R> {
    async fn serve(&self, book: CommandBook, arrival: Arrival) -> Result<Answered, Unserved> {
        self.app
            .serve(book, host_arrival(arrival), &(self.sample)())
            .await
            .map_err(unserved)
    }

    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        let cover = book.cover.clone();
        let handled = self.app.handle(book, &(self.sample)()).await?;
        Ok(answered(handled, cover))
    }

    async fn serve_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
        arrival: Arrival,
    ) -> Result<SyncAnswered, Unserved> {
        // RED: every SIMPLE request is hidden.
        let _ = (hidden(&self.exposed, &book, arrival), &projectors);
        if let Some(domain) = book.cover.as_ref().map(|c| c.domain.clone()) {
            return Err(Unserved::Hidden { domain });
        }
        let cover = book.cover.clone();
        let store = self
            .app
            .stores()
            .store(&())
            .await
            .map_err(|error| Unserved::Failed(Failure::Kernel(KernelError::Store(error))))?;
        let synced = self
            .app
            .kernel()
            .handle_sync(&store, book, &projectors, &(self.sample)())
            .await
            .map_err(Unserved::Failed)?;
        Ok(SyncAnswered {
            answered: answered(synced.handled, cover),
            projections: synced.projections,
        })
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        let pages = self.app.events(cover).await?;
        Ok(pages
            .into_iter()
            .filter(|page| page.sequence >= from)
            .collect())
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
