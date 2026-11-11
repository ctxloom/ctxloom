//! One edge over every project the user daemon has opened (D18): a stream
//! lives in exactly one project's store, so a request is routed to the store
//! that holds its stream. A bearer does not name its store (review B3); the
//! router finds it by asking each open store for the stream (once: the
//! answer is kept), so the run gates (`HookGate`, `OrchestratorGate`) work
//! unchanged over it.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, PoisonError, RwLock};

use angzarr_embedded::v1::{CommandBook, Cover, EventPage};
use angzarr_embedded::{Answered, Failure, KernelError, Malformed, Rejected, StoreError};
use run_ports::{Arrival, Edge, Unserved};

/// A stream as the router keys it: its domain and root.
type Key = (String, Vec<u8>);

fn key_of(cover: &Cover) -> Key {
    (
        cover.domain.clone(),
        cover
            .root
            .as_ref()
            .map(|root| root.value.to_vec())
            .unwrap_or_default(),
    )
}

/// The projects' edges, and which holds which stream.
pub(crate) struct Routes<E> {
    edges: RwLock<Vec<(String, Arc<E>)>>,
    found: Mutex<BTreeMap<Key, usize>>,
}

impl<E> Default for Routes<E> {
    fn default() -> Self {
        Routes {
            edges: RwLock::new(Vec::new()),
            found: Mutex::new(BTreeMap::new()),
        }
    }
}

impl<E: Edge> Routes<E> {
    /// Route to `edge`, `project`'s, too.
    pub(crate) fn add(&self, project: &str, edge: Arc<E>) {
        self.edges
            .write()
            .unwrap_or_else(PoisonError::into_inner)
            .push((project.to_string(), edge));
    }

    /// `project`'s edge, if it is open.
    pub(crate) fn project(&self, project: &str) -> Option<Arc<E>> {
        self.edges
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .iter()
            .find(|(id, _)| id == project)
            .map(|(_, edge)| Arc::clone(edge))
    }

    /// The edge whose store holds `cover`'s stream, if one does.
    async fn find(&self, cover: Option<&Cover>) -> Option<Arc<E>> {
        let cover = cover?;
        let key = key_of(cover);
        let edges: Vec<Arc<E>> = self
            .edges
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .iter()
            .map(|(_, edge)| Arc::clone(edge))
            .collect();
        let known = self
            .found
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get(&key)
            .copied();
        if let Some(edge) = known.and_then(|at| edges.get(at)) {
            return Some(Arc::clone(edge));
        }
        for (at, edge) in edges.iter().enumerate() {
            if edge
                .events(cover, 0)
                .await
                .is_ok_and(|pages| !pages.is_empty())
            {
                self.found
                    .lock()
                    .unwrap_or_else(PoisonError::into_inner)
                    .insert(key, at);
                return Some(Arc::clone(edge));
            }
        }
        None
    }
}

/// The edge the daemon's listener serves: every open project's, routed.
pub(crate) struct RoutingEdge<E>(pub(crate) Arc<Routes<E>>);

fn domain_of(cover: Option<&Cover>) -> String {
    cover.map(|c| c.domain.clone()).unwrap_or_default()
}

impl<E: Edge> Edge for RoutingEdge<E> {
    async fn serve(&self, book: CommandBook, arrival: Arrival) -> Result<Answered, Unserved> {
        match self.0.find(book.cover.as_ref()).await {
            Some(edge) => edge.serve(book, arrival).await,
            None => Err(Unserved::Hidden {
                domain: domain_of(book.cover.as_ref()),
            }),
        }
    }

    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        match self.0.find(book.cover.as_ref()).await {
            Some(edge) => edge.handle(book).await,
            None => Err(Failure::Kernel(KernelError::Malformed(
                Malformed::UnknownDomain {
                    domain: domain_of(book.cover.as_ref()),
                },
            ))),
        }
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        match self.0.find(Some(cover)).await {
            Some(edge) => edge.events(cover, from).await,
            None => Ok(Vec::new()),
        }
    }
}

#[cfg(test)]
#[path = "route.test.rs"]
mod tests;
