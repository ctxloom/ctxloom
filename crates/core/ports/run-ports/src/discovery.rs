//! The user daemon's discovery file (daemon design D18, D27, slice 2 T12,
//! S5): where the one daemon of a user serves, so a client and a hook find
//! it, pin it and present the user bearer. It lives under the ctxloom HOME
//! (`<home>/.ctxloom/daemon/`), never under `XDG_RUNTIME_DIR`, so a test's
//! temporary home never reaches the developer's daemon. Its mode is 0600 in
//! the 0700 daemon directory (on Windows, the profile's inherited ACL, M8):
//! it holds the USER bearer, so what it keeps out is other users. Every
//! process of the user can read it (the separation model: one principal).
//!
//! The file is one `ctxloom.daemon.v1.Discovery` in protobuf's binary
//! encoding (`proto/ctxloom/daemon/v1/discovery.proto`). The codec is pure:
//! the daemon writes [`Discovery::encode`], a reader takes
//! [`Discovery::decode`], which answers `None` for anything it cannot read
//! (the daemon is then treated as absent).

use std::fmt;

use crate::tls::PublicCert;
use crate::token::Token;

/// The daemon's directory under the ctxloom home.
pub const DAEMON_DIR: &str = ".ctxloom/daemon";

/// The election lock: whoever holds it serves (D18; the platform's file lock).
pub const DAEMON_LOCK_FILE: &str = "daemon.lock";

/// The discovery file the serving daemon writes, 0600.
pub const DISCOVERY_FILE: &str = "daemon.discovery";

/// The file's own format (`Discovery.format`).
pub const DISCOVERY_FORMAT: u32 = 1;

/// How long the daemon's own certificate is valid (D27: about a month), from
/// the moment it is minted; it is minted again at each daemon start.
pub const DAEMON_CERT_LIFETIME_SECONDS: i64 = 30 * 86_400;

/// What a serving daemon advertises. Its `Debug` withholds the bearer.
#[derive(Clone, PartialEq, Eq)]
pub struct Discovery {
    /// The binary's full versionator stamp (M3), never the package version.
    pub stamp: String,
    /// The daemon's process id.
    pub pid: u32,
    /// Its listener, `https://<host>:<port>`.
    pub endpoint: String,
    /// The certificate the listener presents: a client's sole trust root.
    pub cert: PublicCert,
    /// The user bearer (T12), minted at the daemon's start.
    pub bearer: Token,
}

impl Discovery {
    /// The file's bytes: one `ctxloom.daemon.v1.Discovery`.
    pub fn encode(&self) -> Vec<u8> {
        Vec::new()
    }

    /// The discovery `bytes` hold; `None` when they hold none (not the
    /// message, another format, an endpoint that is not one, an empty or
    /// garbled certificate, a bearer that is not 64 hex, no stamp).
    pub fn decode(bytes: &[u8]) -> Option<Discovery> {
        let _ = bytes;
        None
    }
}

impl fmt::Debug for Discovery {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Discovery")
            .field("stamp", &self.stamp)
            .field("pid", &self.pid)
            .field("endpoint", &self.endpoint)
            .field("cert", &self.cert)
            .field("bearer", &self.bearer.to_text())
            .finish()
    }
}

#[cfg(test)]
#[path = "discovery.test.rs"]
mod tests;
