//! The run's listener certificate: what the child is told of it (owner
//! rulings 2026-11-09, items 35-37) and what a mint is asked for.
//!
//! TLS authenticates the SERVER to the child; the per-run bearer authenticates
//! the child. The certificate is self-signed and per run, and the child's SOLE
//! trust root. It is PUBLIC (its DER travels in an environment variable as one
//! line of base64, [`PublicCert::to_env_value`]); the private key stays in the
//! process that serves the listener, is never journaled, logged or put in an
//! environment, and [`HookCertificate`]'s `Debug` does not render it.

use std::collections::BTreeSet;
use std::fmt;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use base64::engine::general_purpose::STANDARD;
use base64::Engine;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};

use crate::bind::BindPlan;

/// Every address a child may dial for one run: the ips and the dns names the
/// certificate must cover.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CertNames {
    ips: Vec<IpAddr>,
    dns: Vec<String>,
}

/// A DER certificate is a SEQUENCE.
const DER_SEQUENCE: u8 = 0x30;
/// PEM's line width.
const PEM_COLUMNS: usize = 64;
const PEM_BEGIN: &str = "-----BEGIN CERTIFICATE-----";
const PEM_END: &str = "-----END CERTIFICATE-----";

impl CertNames {
    /// These names, sorted, none twice.
    pub fn new(ips: Vec<IpAddr>, dns: Vec<String>) -> CertNames {
        CertNames {
            ips: ips
                .into_iter()
                .collect::<BTreeSet<_>>()
                .into_iter()
                .collect(),
            dns: dns
                .into_iter()
                .collect::<BTreeSet<_>>()
                .into_iter()
                .collect(),
        }
    }

    /// The loopback addresses, the bind and the told host of `plan`.
    pub fn for_plan(plan: &BindPlan) -> CertNames {
        let mut ips = BTreeSet::from([
            IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
            plan.bind_addr,
        ]);
        let mut dns = BTreeSet::new();
        match plan.told_host.parse::<IpAddr>() {
            Ok(told) => ips.insert(told),
            Err(_) => dns.insert(plan.told_host.clone()),
        };
        CertNames {
            ips: ips.into_iter().collect(),
            dns: dns.into_iter().collect(),
        }
    }

    /// The IP addresses, sorted, none twice.
    pub fn ips(&self) -> Vec<IpAddr> {
        self.ips.clone()
    }

    /// The dns names, sorted, none twice.
    pub fn dns(&self) -> Vec<String> {
        self.dns.clone()
    }
}

/// The listener's certificate as a child holds it: DER, public.
#[derive(Clone, PartialEq, Eq)]
pub struct PublicCert(Vec<u8>);

impl PublicCert {
    /// A certificate from its DER (an X.509 certificate is a DER SEQUENCE).
    pub fn from_der(der: Vec<u8>) -> Result<PublicCert, CertError> {
        match der.first() {
            Some(&DER_SEQUENCE) => Ok(PublicCert(der)),
            _ => Err(CertError::Malformed {
                length: der.len() as u64,
            }),
        }
    }

    /// The DER.
    pub fn der(&self) -> &[u8] {
        &self.0
    }

    /// The value of the environment variable: base64, one line.
    pub fn to_env_value(&self) -> String {
        STANDARD.encode(&self.0)
    }

    /// The certificate of an environment value.
    pub fn from_env_value(text: &str) -> Result<PublicCert, CertError> {
        STANDARD
            .decode(text)
            .ok()
            .and_then(|der| PublicCert::from_der(der).ok())
            .ok_or(CertError::Malformed {
                length: text.len() as u64,
            })
    }

    /// The certificate of an optional environment value: none or empty is
    /// [`CertError::Missing`].
    pub fn from_environment(value: Option<&str>) -> Result<PublicCert, CertError> {
        match value {
            None | Some("") => Err(CertError::Missing),
            Some(text) => PublicCert::from_env_value(text),
        }
    }

    /// The PEM, 64 columns.
    pub fn to_pem(&self) -> String {
        let body = self.to_env_value();
        let mut pem = format!("{PEM_BEGIN}\n");
        // Base64 is ASCII, so a byte chunk is a character chunk.
        for line in body.as_bytes().chunks(PEM_COLUMNS) {
            pem.push_str(&String::from_utf8_lossy(line));
            pem.push('\n');
        }
        pem.push_str(PEM_END);
        pem.push('\n');
        pem
    }
}

impl fmt::Debug for PublicCert {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "PublicCert({} bytes)", self.0.len())
    }
}

/// A minted certificate with its private key.
#[derive(Clone, PartialEq, Eq)]
pub struct HookCertificate {
    public: PublicCert,
    key_pem: String,
}

impl HookCertificate {
    /// A certificate and the PKCS#8 PEM of its key.
    pub fn new(public: PublicCert, key_pem: String) -> HookCertificate {
        HookCertificate { public, key_pem }
    }

    /// The part a child is told.
    pub fn public(&self) -> &PublicCert {
        &self.public
    }

    /// The private key's PEM: for the one process that serves the listener.
    pub fn key_pem(&self) -> &str {
        &self.key_pem
    }
}

impl fmt::Debug for HookCertificate {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "HookCertificate({:?}, key redacted)", self.public)
    }
}

/// What mints a run's certificate.
pub trait CertMint: Send + Sync {
    /// A fresh self-signed certificate for `names`, valid for a run.
    fn mint(&self, names: &CertNames) -> Result<HookCertificate, Report>;
}

/// Why a certificate value is not usable.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CertError {
    /// There is none.
    Missing,
    /// It is not base64 of a DER certificate. The text is not echoed.
    Malformed { length: u64 },
}

impl fmt::Display for CertError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for CertError {
    fn code(&self) -> &str {
        match self {
            CertError::Missing => code!("cert-missing"),
            CertError::Malformed { .. } => code!("cert-malformed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            CertError::Missing => Vec::new(),
            CertError::Malformed { length } => vec![("length", (*length).into())],
        }
    }
}

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(CertError::Missing),
        Box::new(CertError::Malformed { length: 3 }),
    ]
}

#[cfg(test)]
#[path = "tls.test.rs"]
mod tests;
