//! The run's hook listener (owner rulings 2026-11-09, items 7, 9, 16, 17 and
//! 31-33): while an interactive run is live, this process serves the standard
//! angzarr `CommandHandlerCoordinatorService.Handle` on TCP, so the hook
//! processes of the run's engine can deliver capture facts.
//!
//! Nothing here is ctxloom's own ingress service: the wire is angzarr's
//! generic coordinator (`angzarr-coordinator-grpc`'s server on a
//! `Listener::tcp`, port 0, never an unspecified address, the arrival declared
//! from the bind). What is ctxloom's is the admission gate in front of it
//! ([`runs_application::HookGate`]: who the bearer is, the credential's scope,
//! the caller's own session stream, validity until the run ends) and the
//! command half behind it ([`runs_application::EdgeCommands`]: the kernel).
//! `Handle` is the only operation served: the other operations of the service
//! answer UNIMPLEMENTED, and the query service is not mounted at all (a hook
//! never reads). A request over [`MAX_REQUEST_BYTES`] is refused undecoded.
//!
//! The listener is plaintext: the bearer is the authentication (ruling 32),
//! and it crosses loopback in the clear. The endpoint is a URL, so TLS is an
//! additive step.
//!
//! A listener spawns its tasks on the runtime that awaits
//! [`HookListener::serve_tcp`] and needs it to be a multi-thread runtime (or
//! its own): the run's engine is waited for in a blocking call, and a
//! current-thread runtime blocked in it would serve nothing.

mod error;

use std::sync::Arc;
use std::time::Duration;

use angzarr_coordinator::Coordinator;
use angzarr_coordinator_grpc::{BoundEndpoint, Edge as Served, Listener, Running, Server};
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_server::CommandHandlerCoordinatorServiceServer;
use runs_application::{Edge, EdgeCommands, HookGate};

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{ListenerError, CATALOGS, EN_US};

/// How long the open calls have to finish when the listener is stopped.
pub const STOP_GRACE: Duration = Duration::from_secs(3);

/// The most bytes a request to the hook listener may hold. Past it a request
/// is refused before it is decoded.
pub const MAX_REQUEST_BYTES: usize = 1024 * 1024;

/// What a listener declares of the requests it admits.
pub use angzarr_coordinator::Arrival as Reach;

/// The reach a bind at `ip` declares: loopback for a loopback address, else an IP's.
pub fn reach_of(ip: std::net::IpAddr) -> Reach {
    angzarr_coordinator_grpc::arrival_for_bind(std::net::SocketAddr::new(ip, 0))
}

/// A hook listener that is serving: its socket is bound, and a hook that
/// connects finds it. Dropping it stops it at once; [`HookListener::stop`]
/// stops it gracefully.
pub struct HookListener {
    running: Running,
    endpoint: String,
    addr: std::net::SocketAddr,
    reach: Reach,
}

impl HookListener {
    /// Serve the hooks of one run on TCP at `addr` (port 0: the system gives
    /// a free one, [`HookListener::local_addr`] says which), through `edge`.
    /// An unspecified address is refused. The listener declares the arrival of
    /// its bind: loopback for a loopback address, otherwise an IP's.
    pub async fn serve_tcp<E: Edge + 'static>(
        edge: Arc<E>,
        addr: std::net::SocketAddr,
    ) -> Result<HookListener, ListenerError> {
        if addr.ip().is_unspecified() {
            return Err(ListenerError::Unspecified {
                addr: addr.to_string(),
            });
        }
        let coordinator = Coordinator::new(
            EdgeCommands::new(Arc::clone(&edge)),
            (),
            HookGate::new(edge),
        );
        let service =
            CommandHandlerCoordinatorServiceServer::new(Served::new(Arc::new(coordinator)))
                .max_decoding_message_size(MAX_REQUEST_BYTES);
        let listener = Listener::tcp(addr).declare(reach_of(addr.ip()));
        let running = Server::builder()
            .add_service(service)
            .listen(listener)
            .bind()
            .await
            .map_err(|cause| ListenerError::bind_addr(addr, &cause))?;
        let (addr, reach) = running
            .bound()
            .endpoints()
            .iter()
            .find_map(|e| match e {
                BoundEndpoint::Tcp { addr, arrival } => Some((*addr, *arrival)),
                BoundEndpoint::Uds { .. } => None,
            })
            .ok_or_else(|| ListenerError::NotBound {
                addr: addr.to_string(),
            })?;
        Ok(HookListener {
            running,
            endpoint: addr.to_string(),
            addr,
            reach,
        })
    }

    /// The address a TCP listener is bound to.
    pub fn local_addr(&self) -> std::net::SocketAddr {
        self.addr
    }

    /// What the TCP listener declares of its requests.
    pub fn reach(&self) -> Reach {
        self.reach
    }

    /// Stop serving: the calls open get [`STOP_GRACE`] to finish, then the
    /// listener is aborted either way.
    pub async fn stop(self) -> Result<(), ListenerError> {
        self.stop_within(STOP_GRACE).await
    }

    /// [`HookListener::stop`] with `grace` for the open calls. A client that
    /// holds a connection open must never hold the run's end: past `grace`
    /// the shutdown is dropped, which aborts the listener and removes the
    /// socket, and the stop is [`ListenerError::StopTimedOut`].
    pub async fn stop_within(self, grace: Duration) -> Result<(), ListenerError> {
        let endpoint = self.endpoint;
        match tokio::time::timeout(grace, self.running.shutdown()).await {
            Ok(stopped) => stopped.map_err(|cause| ListenerError::stop(&endpoint, &cause)),
            Err(_elapsed) => Err(ListenerError::timed_out(&endpoint, grace)),
        }
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
