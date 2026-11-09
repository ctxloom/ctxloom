//! The run's hook listener as a port: what the run use case asks of whatever
//! serves the hook processes of an interactive engine (owner ruling
//! 2026-11-09, item 17). The use case names the socket path
//! ([`endpoint_path`](crate::endpoint_path)); the server is the composition
//! root's.

use std::future::Future;
use std::pin::Pin;

use angzarr_diagnostic::Report;

/// A boxed future, `Send`: what a use case holding a `dyn` server awaits.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Where the run's hook listener is served.
pub trait HookServer: Send + Sync {
    /// Start serving the run's hooks on the socket at `endpoint`, until the
    /// returned [`Serving`] is stopped or dropped. It is bound when this
    /// returns: a hook that connects after it finds the listener.
    fn serve<'a>(&'a self, endpoint: &'a str) -> BoxFuture<'a, Result<Box<dyn Serving>, Report>>;

    /// Start serving the run's hooks on TCP at `bind` (port 0: the system
    /// gives a free one, [`Serving::port`] says which), until the returned
    /// [`Serving`] is stopped or dropped. Bound when this returns. An
    /// unspecified address is refused.
    fn serve_tcp<'a>(
        &'a self,
        bind: std::net::IpAddr,
    ) -> BoxFuture<'a, Result<Box<dyn Serving>, Report>>;
}

/// A listener that is serving.
pub trait Serving: Send {
    /// The port a TCP listener is bound to (0 for a listener that is not one).
    fn port(&self) -> u16;

    /// Stop serving and remove the socket. Infallible to the caller: a run
    /// that has ended has no use for a stop that failed (the socket's guard
    /// removes the file when the listener drops).
    fn stop(self: Box<Self>) -> BoxFuture<'static, ()>;
}
