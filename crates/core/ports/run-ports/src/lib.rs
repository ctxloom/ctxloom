//! The ports of the run core (S5): the credential [`Token`] and its
//! [`TokenHash`] (D29), the [`TokenSource`] that mints one, the [`Edge`] the
//! core is served through (the exposure seam, `App::serve_in`, as a port), and
//! the run launch ports (step 1b-i): [`RunnerLauncher`], [`Engine`] and
//! [`SessionRecords`]; and the child-engine port (D15, D23): [`ChildEngine`],
//! a long-lived child conversed with through its [`ChildControl`].
//!
//! `Arrival` and `Unserved` are this crate's own, not angzarr-embedded-host's:
//! the host brings a gRPC stack and process supervision, which the core must
//! not link. The driven adapter `embedded-edge` converts (review question 6).

mod argv;
mod bind;
mod child;
mod closed;
mod discovery;
mod edge;
mod engine;
mod future;
mod git;
mod held;
mod hook;
mod host;
mod infrastructure;
mod launch;
mod launcher;
mod locks;
mod login;
mod permit;
mod program;
mod records;
mod sealing;
mod settings;
mod sink;
mod tls;
mod token;
mod tool_homes;
mod user_files;
mod version;
mod wake;

pub use argv::{check_command_line, CommandTooLong, Measure, MAX_ARG_STRLEN, WINDOWS_COMMAND_LINE};
pub use bind::{bind_for, BindPlan, BindRefusal, ContainerRuntime, PlatformFacts, Probe, Runtime};
pub use child::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildObservation, ChildPermissions,
    ChildSpec, Ended, Queued, StartedChild, Urgency,
};
pub use closed::{is_closed, since_closed};
pub use discovery::{
    Discovery, DAEMON_CERT_LIFETIME_SECONDS, DAEMON_DIR, DAEMON_LOCK_FILE, DISCOVERY_FILE,
    DISCOVERY_FORMAT,
};
pub use edge::{Arrival, Edge, SyncAnswered, Unserved};
pub use engine::{
    copy_in, Advice, ContextDelivery, Credentials, Engine, EngineError, FailureClass, HomeFile,
    LaunchSurfaces, LeftOut, Planned, PrepareRequest, Prepared, TurnOutcome, TurnRequest,
    UserSettings, DAEMON_ROOT_VARIABLE, ROOT_VARIABLE,
};
pub use future::BoxFuture;
pub use git::{git_config, GitUser, GIT_CONFIG_FILE};
pub use held::{
    up_to_date, Batch, Bound, Fate, GapWhy, Held, HeldEntry, HeldFact, HeldFacts, HeldGap,
    HoldError, HoldSummary, Settle, Settled, APPEND_WAIT, GAP_SLACK, HELD_DIR, HOME_BOUND,
    SESSION_BOUND,
};
pub use hook::{
    endpoint_url, hook_environment, name_the_run, parse_endpoint, Bearer, BearerError, Dial,
    EndpointError, RedactedEnv, BEARER_VARIABLE, ENDPOINT_VARIABLE, HOOK_VARIABLES,
    LISTENER_VARIABLES, RUN_VARIABLE, SESSION_VARIABLE, TLS_CERT_VARIABLE,
};
pub use host::{
    is_absolute, search_path_led_by, search_path_separator, session_profile, WINDOWS,
    WINDOWS_PROFILE_VARIABLES, WINDOWS_STARTUP_VARIABLES,
};
pub use infrastructure::{Code, Infrastructure, DEAD_LETTERED, DIAGNOSED};
pub use launch::{Launch, LaunchAt, LaunchKind, Stdio, StopPolicy, STOP_GRACE, STOP_SIGNAL};
pub use launcher::{
    Exit, LaunchEvent, LaunchFailure, Limits, Parks, RunnerLauncher, Running, TurnCommand,
    TurnLaunch, TurnOutput,
};
pub use locks::{HeldLock, LockError, LockProbe, RunLocks};
pub use login::{LinkState, LoginLinks};
pub use permit::{apply_permits, permits, permits_for, Ignored, Permits, PERMIT_PREFIX};
pub use program::{check_batch_arguments, is_batch_file, BatchArguments, ProgramNotFound};
pub use records::{check_rel, RecordsError, SessionRecords};
#[cfg(any(test, feature = "test-support"))]
pub use sealing::TestSealing;
pub use sealing::{
    KeyInfo, KeySet, KeysSeen, ProjectEvidence, ProjectFacts, ProjectSecrets, Sealer, Sealing,
    SealingError,
};
pub use settings::{
    AuthSetting, CredentialPolicy, EngineSettings, SessionHome, APPROVAL_TIMEOUT_DEFAULT,
};
pub use sink::{Delivery, FactSink, NO_SUCH_SESSION};
pub use tls::{CertError, CertMint, CertNames, HookCertificate, PublicCert};
pub use token::{Token, TokenError, TokenHash, TokenSource, EN_US};
pub use tool_homes::{tool_homes, CARGO_HOME, NPM_CACHE, RUSTUP_HOME, TOOL_HOME_VARIABLES};
pub use user_files::{SettingsFile, SettingsFileKind, SettingsFiles};
pub use version::newer;
pub use wake::{wake_line, wake_nonce, WakePost, NONCE_DIGITS};

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    let mut samples = token::diagnostic_samples();
    samples.extend(held::diagnostic_samples());
    samples.extend(sealing::diagnostic_samples());
    samples.extend(hook::diagnostic_samples());
    samples.extend(bind::diagnostic_samples());
    samples.push(Box::new(CommandTooLong {
        os: "windows".to_string(),
        measure: Measure::CommandLine,
        length: 40_000,
        limit: WINDOWS_COMMAND_LINE,
    }));
    samples.push(Box::new(ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude.exe".to_string(), "claude.cmd".to_string()],
        dirs: vec![r"C:\Users\a\bin".to_string()],
    }));
    samples.push(Box::new(ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude".to_string()],
        dirs: Vec::new(),
    }));
    samples.push(Box::new(BatchArguments {
        program: r"C:\Users\a\AppData\Roaming\npm\claude.cmd".to_string(),
    }));
    samples.extend(tls::diagnostic_samples());
    samples.extend(child::diagnostic_samples());
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
    samples.push(Box::new(EngineError::BadPermissions {
        engine: "claude-code".to_string(),
        ext_err_txt: "expected value at line 1".to_string(),
    }));
    samples.push(Box::new(EngineError::CannotCarry {
        engine: "claude-code".to_string(),
        ext_err_txt: "it has no \"browser\" tool".to_string(),
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
