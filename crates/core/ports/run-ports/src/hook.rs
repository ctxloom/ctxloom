//! What a hook process of the interactive engine is told (owner rulings
//! 2026-11-08 item 9 and 2026-11-09 item 16): where the run's listener is and
//! the bearer that is its hook-only credential, in three environment
//! variables. Pure: the socket path and the variables are computed from what
//! they are given (the runtime directory is injected, never read here).
//!
//! The bearer is a SECRET. It is a composite `<run-id>.<64 hex>`, so the gate
//! can look the run up without upstream metadata support; it never reaches a
//! child's turn (the engines' turn allowlists do not name it), a log line or
//! an error message ([`Bearer`]'s `Debug` is redacted, [`BearerError`] does not
//! echo what it was given, and [`RedactedEnv`] prints a map without its value).

use std::collections::BTreeMap;
use std::fmt;
use std::path::Path;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::token::{Token, TokenError};

/// The variable that names the run's listener: a unix socket path.
pub const ENDPOINT_VARIABLE: &str = "CTXLOOM_ENDPOINT";
/// The variable that holds the hook principal's bearer: the secret.
pub const BEARER_VARIABLE: &str = "CTXLOOM_BEARER";
/// The variable that names the run (UUID text): not a secret.
pub const RUN_VARIABLE: &str = "CTXLOOM_RUN";

/// The three variables, which reach the INTERACTIVE engine's environment and
/// no other.
pub const HOOK_VARIABLES: [&str; 3] = [ENDPOINT_VARIABLE, BEARER_VARIABLE, RUN_VARIABLE];

/// The longest socket path the kernel takes: `sun_path` is 108 bytes on Linux,
/// one of them the terminator.
pub const SOCKET_PATH_LIMIT: usize = 107;

/// How many bytes of the run's root name its socket: 16 hexadecimal digits.
const SHORT_BYTES: usize = 8;

/// The run's listener socket: `<runtime_dir>/ctxloom/<short>.sock` when the
/// runtime directory (`XDG_RUNTIME_DIR`) is set and absolute, else
/// `<home>/.ctxloom/run/<short>.sock` under ctxloom's home. `<short>` is the
/// first 8 bytes of the run's root in hex: a run's root is unique, so two runs
/// never share a socket. A path longer than [`SOCKET_PATH_LIMIT`] is refused,
/// not truncated.
pub fn endpoint_path(
    runtime_dir: Option<&str>,
    home: &str,
    run: &[u8],
) -> Result<String, EndpointError> {
    let Some(root) = run.get(..SHORT_BYTES) else {
        return Err(EndpointError::RunTooShort {
            length: run.len() as u64,
        });
    };
    let short: String = root.iter().map(|b| format!("{b:02x}")).collect();
    let file = format!("{short}.sock");
    let dir = match runtime_dir.filter(|dir| Path::new(dir).is_absolute()) {
        Some(runtime) => Path::new(runtime).join("ctxloom"),
        None if Path::new(home).is_absolute() => Path::new(home).join(".ctxloom").join("run"),
        None => return Err(EndpointError::NoDirectory),
    };
    let path = dir.join(file).to_string_lossy().into_owned();
    match path.len() > SOCKET_PATH_LIMIT {
        true => Err(EndpointError::TooLong {
            path,
            limit: SOCKET_PATH_LIMIT as u64,
        }),
        false => Ok(path),
    }
}

/// Why a run has no socket path.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EndpointError {
    /// The path would not fit `sun_path`.
    TooLong { path: String, limit: u64 },
    /// The run's root is too short to name a socket.
    RunTooShort { length: u64 },
    /// No runtime directory and no ctxloom home to put the socket under.
    NoDirectory,
}

impl fmt::Display for EndpointError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for EndpointError {
    fn code(&self) -> &str {
        match self {
            EndpointError::TooLong { .. } => code!("endpoint-too-long"),
            EndpointError::RunTooShort { .. } => code!("endpoint-run-too-short"),
            EndpointError::NoDirectory => code!("endpoint-no-directory"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            EndpointError::TooLong { path, limit } => vec![
                ("length", (path.len() as u64).into()),
                ("limit", (*limit).into()),
                ("path", path.as_str().into()),
            ],
            EndpointError::RunTooShort { length } => vec![("length", (*length).into())],
            EndpointError::NoDirectory => Vec::new(),
        }
    }
}

/// The hook principal's bearer: `<run-id>.<64 hexadecimal digits>`. Its
/// `Debug` is redacted; the text comes out only through [`Bearer::expose`],
/// for the environment of the one process that is given it.
#[derive(Clone, PartialEq, Eq)]
pub struct Bearer(String);

impl Bearer {
    /// The bearer of `token` for the run named by `run` (UUID text).
    pub fn compose(run: &str, token: &Token) -> Bearer {
        Bearer(format!("{run}.{}", token.to_text()))
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

/// The variables the interactive engine is given for its hooks: the endpoint,
/// the bearer and the run.
pub fn hook_environment(endpoint: &str, run: &str, token: &Token) -> BTreeMap<String, String> {
    BTreeMap::from([
        (ENDPOINT_VARIABLE.to_string(), endpoint.to_string()),
        (
            BEARER_VARIABLE.to_string(),
            Bearer::compose(run, token).expose().to_string(),
        ),
        (RUN_VARIABLE.to_string(), run.to_string()),
    ])
}

/// An environment as `Debug` shows it: every variable, the bearer's value
/// withheld.
pub struct RedactedEnv<'a>(pub &'a BTreeMap<String, String>);

impl fmt::Debug for RedactedEnv<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut map = f.debug_map();
        for (name, value) in self.0 {
            match name == BEARER_VARIABLE {
                true => map.entry(name, &"<redacted>"),
                false => map.entry(name, value),
            };
        }
        map.finish()
    }
}

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(EndpointError::TooLong {
            path: "/run/user/1000/ctxloom/0011223344556677.sock".to_string(),
            limit: SOCKET_PATH_LIMIT as u64,
        }),
        Box::new(EndpointError::RunTooShort { length: 3 }),
        Box::new(EndpointError::NoDirectory),
        Box::new(BearerError::Malformed { length: 3 }),
    ]
}

#[cfg(test)]
#[path = "hook.test.rs"]
mod tests;
