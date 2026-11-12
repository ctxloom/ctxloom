//! The daemon's wire types: prost messages generated from
//! `proto/ctxloom/daemon/v1`. A `HandleSync` projection is the resource
//! message the matching `Get` returns (daemon.proto's header): a read model
//! answers its view as one of these (S32).

/// The generated package, nested as its proto package is.
pub mod ctxloom {
    pub mod daemon {
        // Generated: the request oneofs carry whole messages (a launch
        // beside a word), as the drafts define them.
        #[allow(clippy::large_enum_variant)]
        pub mod v1 {
            include!(concat!(env!("OUT_DIR"), "/ctxloom.daemon.v1.rs"));
        }
    }
}

/// `ctxloom.daemon.v1`.
pub use ctxloom::daemon::v1;

// The refusals' declared statuses, generated from the protos.
include!(concat!(env!("OUT_DIR"), "/refusals.rs"));

/// angzarr's own messages, as the stubs' extern path names them (none of
/// the service's methods carries one).
#[cfg(any(feature = "server", feature = "client"))]
mod angzarr_v1 {}

/// The `Ctxloom` service's tonic stubs (`server`: its trait and router;
/// `client`: its client), over the messages of [`v1`].
#[cfg(any(feature = "server", feature = "client"))]
pub mod service {
    // Generated.
    #![allow(clippy::all, missing_docs)]
    include!(concat!(env!("OUT_DIR"), "/services/ctxloom.daemon.v1.rs"));
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
