//! The run's hook listener (owner rulings 2026-11-09, items 7, 9, 16 and 17):
//! while an interactive run is live, this process serves the standard angzarr
//! `CommandHandlerCoordinatorService.Handle` on a unix socket, so the hook
//! processes of the run's engine can deliver capture facts.
//!
//! Nothing here is ctxloom's own ingress service: the wire is angzarr's
//! generic coordinator (`angzarr-coordinator-grpc`'s server on a `Listener::uds`,
//! a socket of mode 0600 in a directory of mode 0700 that is created if need
//! be, a stale socket replaced, a live one refused, the socket removed when
//! the listener stops). What is ctxloom's is the admission gate in front of it
//! ([`runs_application::HookGate`]: who the bearer is, the six capture-fact
//! types, the caller's own session stream, validity until the run ends) and the
//! command half behind it ([`runs_application::EdgeCommands`]: the kernel).
//! `Handle` is the only operation served: the other operations of the service
//! answer UNIMPLEMENTED, and the query service is not mounted at all (a hook
//! never reads).
//!
//! A listener spawns its tasks on the runtime that awaits [`HookListener::serve`]
//! and needs it to be a multi-thread runtime (or its own): the run's engine is
//! waited for in a blocking call, and a current-thread runtime blocked in it
//! would serve nothing.

mod error;

use std::path::Path;
use std::sync::Arc;

use angzarr_coordinator::Coordinator;
use angzarr_coordinator_grpc::{Edge as Served, Listener, Running, Server};
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_server::CommandHandlerCoordinatorServiceServer;
use runs_application::{Edge, EdgeCommands, HookGate};

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{ListenerError, CATALOGS, EN_US};

/// A hook listener that is serving: its socket is bound, and a hook that
/// connects finds it. Dropping it stops it at once; [`HookListener::stop`]
/// stops it gracefully.
pub struct HookListener {
    running: Running,
    endpoint: String,
}

impl HookListener {
    /// Serve the hooks of one run on the unix socket at `endpoint`, through
    /// `edge` (the kernel). The socket's directory is created (mode 0700)
    /// when it does not exist, and must be ours with that mode when it does.
    pub async fn serve<E: Edge + 'static>(
        edge: Arc<E>,
        endpoint: &Path,
    ) -> Result<HookListener, ListenerError> {
        let coordinator = Coordinator::new(
            EdgeCommands::new(Arc::clone(&edge)),
            (),
            HookGate::new(edge),
        );
        let running = Server::builder()
            .add_service(CommandHandlerCoordinatorServiceServer::new(Served::new(
                Arc::new(coordinator),
            )))
            .listen(Listener::uds(endpoint))
            .bind()
            .await
            .map_err(|cause| ListenerError::bind(endpoint, &cause))?;
        Ok(HookListener {
            running,
            endpoint: endpoint.display().to_string(),
        })
    }

    /// The socket's path, as served.
    pub fn endpoint(&self) -> &str {
        &self.endpoint
    }

    /// Stop serving: the calls open finish, and the socket is removed.
    pub async fn stop(self) -> Result<(), ListenerError> {
        let endpoint = self.endpoint;
        self.running
            .shutdown()
            .await
            .map_err(|cause| ListenerError::stop(&endpoint, &cause))
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
