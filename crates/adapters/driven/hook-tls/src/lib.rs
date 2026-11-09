//! The run's listener certificate (owner rulings 2026-11-09, items 35-37):
//! minted here, per run, self-signed, ECDSA P-256 on ring, before the engine is
//! spawned. The child pins it as its SOLE trust root; the private key stays in
//! the listener's process.
//!
//! The window is explicit (rcgen's default starts in 1975 and ends in 4096):
//! it starts [`BACKDATE_SECONDS`] before the clock, for the skew between this
//! clock and the child's, and ends [`LIFETIME_SECONDS`] after it, a day: a
//! cap that bounds a certificate that leaks and outlasts any run.
//!
//! The certificate is not a CA, is for server authentication only, and
//! names every address the child may dial for the run ([`CertNames`]).

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};
use run_ports::{CertMint, CertNames, HookCertificate};

/// The seconds before now a certificate is valid from.
pub const BACKDATE_SECONDS: i64 = 0;
/// The seconds after now a certificate is valid until.
pub const LIFETIME_SECONDS: i64 = 0;

/// What the mint reads the time from.
pub trait Clock: Send + Sync {
    /// Seconds since the Unix epoch.
    fn unix_seconds(&self) -> i64;
}

/// The system's clock.
#[derive(Debug, Default)]
pub struct SystemClock;

impl Clock for SystemClock {
    fn unix_seconds(&self) -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::SystemTime::UNIX_EPOCH)
            .map_or(0, |since| {
                i64::try_from(since.as_secs()).unwrap_or(i64::MAX)
            })
    }
}

/// The window a certificate is valid in, in seconds since the epoch.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Validity {
    pub not_before: i64,
    pub not_after: i64,
}

/// The window of a certificate minted at `now`.
pub fn validity(now: i64) -> Validity {
    Validity {
        not_before: now,
        not_after: now,
    }
}

/// The mint: a [`CertMint`] over a [`Clock`].
#[derive(Debug)]
pub struct Minter<C> {
    clock: C,
}

impl<C: Clock> Minter<C> {
    pub fn new(clock: C) -> Minter<C> {
        Minter { clock }
    }
}

impl<C: Clock> CertMint for Minter<C> {
    fn mint(&self, names: &CertNames) -> Result<HookCertificate, Report> {
        let _ = (names, self.clock.unix_seconds());
        Err(Report::of(&MintError::Generate {
            ext_err_txt: "not built".to_string(),
        }))
    }
}

/// Why a certificate could not be minted. None of it holds a key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum MintError {
    /// The clock reads a time no certificate date can be made from.
    Clock { seconds: i64 },
    /// A name the certificate cannot carry.
    Name { name: String, ext_err_txt: String },
    /// The generator failed.
    Generate { ext_err_txt: String },
}

impl fmt::Display for MintError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for MintError {
    fn code(&self) -> &str {
        match self {
            MintError::Clock { .. } => code!("clock"),
            MintError::Name { .. } => code!("name"),
            MintError::Generate { .. } => code!("generate"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            MintError::Clock { seconds } => vec![("seconds", (*seconds).into())],
            MintError::Name { name, ext_err_txt } => vec![
                ("name", name.as_str().into()),
                ("ext_err_txt", ext_err_txt.as_str().into()),
            ],
            MintError::Generate { ext_err_txt } => {
                vec![("ext_err_txt", ext_err_txt.as_str().into())]
            }
        }
    }
}

impl std::error::Error for MintError {}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(MintError::Clock { seconds: i64::MAX }),
        Box::new(MintError::Name {
            name: "a b".to_string(),
            ext_err_txt: "not a dns name".to_string(),
        }),
        Box::new(MintError::Generate {
            ext_err_txt: "the random source failed".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
