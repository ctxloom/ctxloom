//! The user daemon's discovery file (daemon design D18, D27, review S7): where
//! the one daemon of a user serves, so a client and a hook find it. It lives
//! under the ctxloom HOME (`<home>/.ctxloom/daemon/`), never under
//! `XDG_RUNTIME_DIR`, so a test's temporary home never reaches the
//! developer's daemon. Its mode is 0600: it names the endpoint and the
//! daemon's PUBLIC certificate, no secret (a hook still presents its own
//! run's bearer).
//!
//! The codec is pure: the daemon writes [`Discovery::encode`], a reader takes
//! [`Discovery::decode`], which answers `None` for anything it cannot read
//! (a hook then falls back to its spawn environment, as it did before the
//! daemon).

use crate::hook::parse_endpoint;
use crate::tls::PublicCert;

/// The daemon's directory under the ctxloom home.
pub const DAEMON_DIR: &str = ".ctxloom/daemon";

/// The election lock: whoever holds it serves (D18; the platform's file lock).
pub const DAEMON_LOCK_FILE: &str = "daemon.lock";

/// The discovery file the serving daemon writes, 0600.
pub const DISCOVERY_FILE: &str = "daemon.endpoint";

/// The first line of a discovery file: its format and version.
const HEADER: &str = "ctxloom-daemon 1";

/// How long the daemon's own certificate is valid (D27: about a month), from
/// the moment it is minted; it is minted again at each daemon start.
pub const DAEMON_CERT_LIFETIME_SECONDS: i64 = 30 * 86_400;

/// What a serving daemon advertises.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Discovery {
    /// The ctxloom version that serves (D3: a client of another version
    /// asks it to drain).
    pub version: String,
    /// The daemon's process id.
    pub pid: u32,
    /// Its listener, `https://<host>:<port>`.
    pub endpoint: String,
    /// The certificate the listener presents: a client's sole trust root.
    pub cert: PublicCert,
}

impl Discovery {
    /// The file's text: a header, then one `key value` line each.
    pub fn encode(&self) -> String {
        format!(
            "{HEADER}\nversion {}\npid {}\nendpoint {}\ncert {}\n",
            self.version,
            self.pid,
            self.endpoint,
            self.cert.to_env_value()
        )
    }

    /// The discovery `text` says; `None` when it is not one (another header,
    /// a key missing, an endpoint that is not one, a garbled certificate).
    pub fn decode(text: &str) -> Option<Discovery> {
        let mut lines = text.lines();
        if lines.next()? != HEADER {
            return None;
        }
        let mut version = None;
        let mut pid = None;
        let mut endpoint = None;
        let mut cert = None;
        for line in lines {
            let (key, value) = line.split_once(' ')?;
            match key {
                "version" => version = Some(value.to_string()),
                "pid" => pid = Some(value.parse().ok()?),
                "endpoint" => endpoint = Some(parse_endpoint(value).ok()?.url()),
                "cert" => cert = Some(PublicCert::from_env_value(value).ok()?),
                _ => {}
            }
        }
        Some(Discovery {
            version: version?,
            pid: pid?,
            endpoint: endpoint?,
            cert: cert?,
        })
    }
}

#[cfg(test)]
#[path = "discovery.test.rs"]
mod tests;
