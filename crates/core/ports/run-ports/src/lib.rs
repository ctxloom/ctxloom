//! The ports of the run core (S5): the credential [`Token`] and its
//! [`TokenHash`] (D29), the [`TokenSource`] that mints one, the [`Edge`] the
//! core is served through (the exposure seam, `App::serve_in`, as a port), and
//! the run launch ports (step 1b-i): [`RunnerLauncher`], [`Engine`] and
//! [`SessionRecords`].
//!
//! `Arrival` and `Unserved` are this crate's own, not angzarr-embedded-host's:
//! the host brings a gRPC stack and process supervision, which the core must
//! not link. The driven adapter `embedded-edge` converts (review question 6).

mod bind;
mod edge;
mod engine;
mod hook;
mod launcher;
mod login;
mod permit;
mod records;
mod serving;
mod settings;
mod sink;
mod tls;
mod token;

pub use bind::{bind_for, BindPlan, BindRefusal, ContainerRuntime, PlatformFacts, Probe, Runtime};
pub use edge::{Arrival, Edge, Unserved};
pub use engine::{
    copy_in, Credentials, Engine, EngineError, FailureClass, HomeFile, PrepareRequest, Prepared,
    TurnOutcome, TurnRequest, HOME_VARIABLE,
};
pub use hook::{
    endpoint_url, hook_environment, parse_endpoint, Bearer, BearerError, Dial, EndpointError,
    RedactedEnv, BEARER_VARIABLE, ENDPOINT_VARIABLE, HARP_VARIABLE, HOOK_VARIABLES, RUN_VARIABLE,
    TLS_CERT_VARIABLE,
};
pub use launcher::{
    Exit, Launch, LaunchEvent, LaunchFailure, Limits, RunnerLauncher, Running, TurnCommand,
    TurnOutput,
};
pub use login::{LinkState, LoginLinks};
pub use permit::{apply_permits, permits, Ignored, Permits, PERMIT_PREFIX};
pub use records::{check_rel, RecordsError, SessionRecords};
pub use serving::{BoxFuture, HookServer, Serving};
pub use settings::{AuthSetting, CredentialPolicy, EngineSettings, SessionHome};
pub use sink::{Delivery, FactSink};
pub use tls::{CertError, CertMint, CertNames, HookCertificate, PublicCert};
pub use token::{Token, TokenError, TokenHash, TokenSource, EN_US};

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn angzarr_diagnostic::Diagnostic>> {
    let mut samples = token::diagnostic_samples();
    samples.extend(hook::diagnostic_samples());
    samples.extend(bind::diagnostic_samples());
    samples.extend(tls::diagnostic_samples());
    samples.push(Box::new(EngineError::NotYet {
        engine: "claude-code".to_string(),
        operation: "drive_interactive".to_string(),
    }));
    samples.push(Box::new(EngineError::UnknownEffort {
        engine: "claude-code".to_string(),
        level: "extreme".to_string(),
        known: "low, medium, high".to_string(),
    }));
    samples.push(Box::new(EngineError::NoHostHome {
        engine: "claude-code".to_string(),
    }));
    samples.push(Box::new(RecordsError::BadPath {
        rel: "..".to_string(),
    }));
    samples.push(Box::new(RecordsError::Io {
        harp: "h".to_string(),
        rel: "r".to_string(),
        ext_err_txt: "denied".to_string(),
    }));
    samples
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
