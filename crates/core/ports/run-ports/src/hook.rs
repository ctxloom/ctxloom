//! What a hook process of a run's engine (the terminal session's, or a
//! one-shot child's: owner ruling 51) is told (owner rulings 2026-11-08 item 9
//! and 2026-11-09 item 16): where the run's listener is (a URL) and
//! the bearer that is its hook-only credential, in three environment
//! variables. Pure: the endpoint URL and the variables are computed from what
//! they are given (the bound port is injected, never read here).
//!
//! The bearer is a SECRET. It is a composite `<run-id>.<64 hex>`, so the gate
//! can look the run up without upstream metadata support; it reaches the
//! engine's environment and nowhere else: never a log line or an error
//! message ([`Bearer`]'s `Debug` is redacted, [`BearerError`] does not
//! echo what it was given, and [`RedactedEnv`] prints an environment's names
//! and none of its values).

use std::collections::BTreeMap;
use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::tls::PublicCert;
use crate::token::{Token, TokenError};

/// The variable that names the run's listener: the listener's URL (`https://<host>:<port>`).
pub const ENDPOINT_VARIABLE: &str = "CTXLOOM_ENDPOINT";
/// The variable that holds the hook principal's bearer: the secret.
pub const BEARER_VARIABLE: &str = "CTXLOOM_BEARER";
/// The variable that names the run (UUID text): not a secret.
pub const RUN_VARIABLE: &str = "CTXLOOM_RUN";

/// The variable that holds the run's listener certificate, the child's sole
/// trust root: base64 of the DER on one line. Public, not a secret.
pub const TLS_CERT_VARIABLE: &str = "CTXLOOM_TLS_CERT";

/// The variable that names the session's harp: the address a hook finds its
/// session by. The engine's environment carries it (the terminal session's
/// and a one-shot child's).
pub const HARP_VARIABLE: &str = "CTXLOOM_SESSION_HARP";

/// The variable that names the run's session by its stable id (UUID text):
/// what a hook addresses its facts to (review finding 6, ruling 87: never
/// read from the session's directory). Not a secret; the gate still holds a
/// hook to the session of the run its bearer names.
pub const SESSION_VARIABLE: &str = "CTXLOOM_SESSION";

/// The five variables, which reach a run's engine's environment (the
/// terminal session's and a one-shot child's, ruling 51), always the run's own:
/// an inherited value is replaced.
pub const HOOK_VARIABLES: [&str; 5] = [
    ENDPOINT_VARIABLE,
    BEARER_VARIABLE,
    RUN_VARIABLE,
    TLS_CERT_VARIABLE,
    SESSION_VARIABLE,
];

/// The URL a hook process dials for the listener at `host` and `port`:
/// `https://<host>:<port>`. An IPv6 literal is bracketed. Port 0 is a request
/// to bind, never a place to dial: it is refused.
pub fn endpoint_url(host: &str, port: u16) -> Result<String, EndpointError> {
    let url = match host.contains(':') {
        true => format!("https://[{host}]:{port}"),
        false => format!("https://{host}:{port}"),
    };
    match port {
        0 => Err(EndpointError::PortZero { endpoint: url }),
        _ => Ok(url),
    }
}

/// Where an endpoint URL says to dial.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Dial {
    /// The host, an IPv6 literal without its brackets.
    pub host: String,
    pub port: u16,
}

impl Dial {
    /// The URL a client channel is built from.
    pub fn url(&self) -> String {
        let host = &self.host;
        match host.contains(':') {
            true => format!("https://[{host}]:{}", self.port),
            false => format!("https://{host}:{}", self.port),
        }
    }
}

/// The dial an endpoint URL names: `https://`, a host, a port (not 0) and
/// nothing else. `http://` is [`EndpointError::Plaintext`]: there is no
/// downgrade.
pub fn parse_endpoint(text: &str) -> Result<Dial, EndpointError> {
    let malformed = || EndpointError::Malformed {
        endpoint: text.to_string(),
    };
    let (scheme, rest) = text.split_once("://").ok_or_else(malformed)?;
    let secure = match scheme {
        "http" => false,
        "https" => true,
        "" => return Err(malformed()),
        other
            if other
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || "+-.".contains(c)) =>
        {
            return Err(EndpointError::Scheme {
                scheme: other.to_string(),
            })
        }
        _ => return Err(malformed()),
    };
    let (host, port) = match rest.strip_prefix('[') {
        Some(bracketed) => {
            let (host, after) = bracketed.split_once(']').ok_or_else(malformed)?;
            (host, after.strip_prefix(':').ok_or_else(malformed)?)
        }
        None => rest.rsplit_once(':').ok_or_else(malformed)?,
    };
    let host_ok = !host.is_empty()
        && host
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "-._:".contains(c));
    let port: u16 = port.parse().map_err(|_| malformed())?;
    if !host_ok {
        return Err(malformed());
    }
    match (port, secure) {
        (0, _) => Err(EndpointError::PortZero {
            endpoint: text.to_string(),
        }),
        (_, false) => Err(EndpointError::Plaintext {
            endpoint: text.to_string(),
        }),
        (port, true) => Ok(Dial {
            host: host.to_string(),
            port,
        }),
    }
}

/// Why an endpoint text is not a listener's URL.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EndpointError {
    /// Not `<scheme>://<host>:<port>`.
    Malformed { endpoint: String },
    /// A scheme other than `http` and `https`.
    Scheme { scheme: String },
    /// An `http://` endpoint: the callback is TLS only.
    Plaintext { endpoint: String },
    /// Port 0: a request to bind, never a place to dial.
    PortZero { endpoint: String },
}

impl fmt::Display for EndpointError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for EndpointError {
    fn code(&self) -> &str {
        match self {
            EndpointError::Malformed { .. } => code!("endpoint-malformed"),
            EndpointError::Scheme { .. } => code!("endpoint-scheme"),
            EndpointError::Plaintext { .. } => code!("endpoint-plaintext"),
            EndpointError::PortZero { .. } => code!("endpoint-port-zero"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            EndpointError::Malformed { endpoint }
            | EndpointError::Plaintext { endpoint }
            | EndpointError::PortZero { endpoint } => vec![("endpoint", endpoint.as_str().into())],
            EndpointError::Scheme { scheme } => vec![("scheme", scheme.as_str().into())],
        }
    }
}

/// A bearer a client presents: a run's hook bearer, `<run-id>.<64
/// hexadecimal digits>` (the per-run listener's own authentication), or the
/// user bearer (T12), the daemon's token alone ([`Bearer::user`]). Its
/// `Debug` is redacted; the text comes out only through [`Bearer::expose`],
/// for the one call or environment it is given to.
#[derive(Clone, PartialEq, Eq)]
pub struct Bearer(String);

impl Bearer {
    /// The bearer of `token` for the run named by `run` (UUID text).
    pub fn compose(run: &str, token: &Token) -> Bearer {
        Bearer(format!("{run}.{}", token.to_text()))
    }

    /// The user bearer (T12): `token`'s 64 hexadecimal digits alone, as the
    /// daemon's discovery file holds it. It names no run.
    pub fn user(token: &Token) -> Bearer {
        Bearer(token.to_text())
    }

    /// The run text and token of a bearer's text, or why it holds none. The
    /// refusal does not carry the text.
    pub fn parse(text: &str) -> Result<(String, Token), BearerError> {
        let malformed = || BearerError::Malformed {
            length: text.len() as u64,
        };
        let (run, secret) = text.rsplit_once('.').ok_or_else(malformed)?;
        if run.is_empty() {
            return Err(malformed());
        }
        let token = Token::from_text(secret).map_err(|_: TokenError| malformed())?;
        Ok((run.to_string(), token))
    }

    /// The secret text.
    pub fn expose(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for Bearer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Bearer(redacted)")
    }
}

/// A bearer text that is not `<run>.<token>`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BearerError {
    Malformed { length: u64 },
}

impl fmt::Display for BearerError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for BearerError {
    fn code(&self) -> &str {
        code!("bearer-malformed")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        let BearerError::Malformed { length } = self;
        vec![("length", (*length).into())]
    }
}

/// The variables a run's engine is given for its hooks: the endpoint, the
/// bearer, the run, its certificate and its session (the stable id a hook
/// addresses its facts to).
pub fn hook_environment(
    endpoint: &str,
    run: &str,
    session: &str,
    token: &Token,
    cert: &PublicCert,
) -> BTreeMap<String, String> {
    BTreeMap::from([
        (SESSION_VARIABLE.to_string(), session.to_string()),
        (ENDPOINT_VARIABLE.to_string(), endpoint.to_string()),
        (
            BEARER_VARIABLE.to_string(),
            Bearer::compose(run, token).expose().to_string(),
        ),
        (RUN_VARIABLE.to_string(), run.to_string()),
        (TLS_CERT_VARIABLE.to_string(), cert.to_env_value()),
    ])
}

/// An environment as `Debug` shows it: every variable's name, no value.
pub struct RedactedEnv<'a>(pub &'a BTreeMap<String, String>);

impl fmt::Debug for RedactedEnv<'_> {
    /// The names alone: every value is withheld, whatever its name
    /// (credential values never render; no list of sensitive names to keep).
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut map = f.debug_map();
        for name in self.0.keys() {
            map.entry(name, &"<redacted>");
        }
        map.finish()
    }
}

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(EndpointError::Malformed {
            endpoint: "127.0.0.1".to_string(),
        }),
        Box::new(EndpointError::Scheme {
            scheme: "ftp".to_string(),
        }),
        Box::new(EndpointError::Plaintext {
            endpoint: "http://127.0.0.1:9".to_string(),
        }),
        Box::new(EndpointError::PortZero {
            endpoint: "https://127.0.0.1:0".to_string(),
        }),
        Box::new(BearerError::Malformed { length: 3 }),
    ]
}

#[cfg(test)]
#[path = "hook.test.rs"]
mod tests;
