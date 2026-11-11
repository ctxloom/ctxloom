//! The engine port: what an agent engine contributes to running a child.
//!
//! Pure. An engine says what to put in a session home ([`Engine::prepare`]),
//! the command of ONE turn ([`Engine::drive_turn`]) and what a finished
//! turn's output means ([`Engine::finish_turn`]); a driven adapter
//! (`engine-claude`, `engine-mock`) implements it, `host-launcher` runs the
//! command, and nothing here touches a file or a process.
//!
//! A turn is one command and a later turn a new one that resumes the engine's
//! own session (`TurnRequest::resume`); there is no engine process between
//! turns.
//! `drive_interactive` is the human's terminal run: its command is given her
//! terminal ([`crate::RunnerLauncher::launch_terminal`]).

use std::collections::BTreeMap;
use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::launcher::{TurnCommand, TurnOutput};

/// The variable that names ctxloom's home (the directory ctxloom keeps
/// `.ctxloom` under) in an engine's environment (the terminal session's and a
/// one-shot child's). The engine's own
/// children, hook processes among them, run without `HOME`, so they find
/// ctxloom's home through this one explicit variable.
pub const HOME_VARIABLE: &str = "CTXLOOM_HOME";

/// A file to write under a session home.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HomeFile {
    /// Slash-separated, relative to the home.
    pub rel: String,
    pub bytes: Vec<u8>,
}

/// What [`Engine::prepare`] is asked.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PrepareRequest {
    /// The session home's absolute path.
    pub home: String,
    /// The assembled context the engine is handed.
    pub context: String,
    /// The names found in the user's own engine configuration directory: the
    /// only ones an engine may copy in are those on its allowlist.
    pub offered: Vec<String>,
    /// The home is the human's terminal session's, not a child's turn: the
    /// engine installs its hooks in it.
    pub interactive: bool,
    /// The directory the engine runs in (the project's), which an engine may
    /// mark trusted in its own state (owner ruling 113); none, none is.
    pub cwd: Option<String>,
    /// What the home holds now of the files [`Engine::merges`] names, by
    /// their path relative to it; a file not there is absent. The engine
    /// merges into these instead of replacing them.
    pub existing: BTreeMap<String, Vec<u8>>,
}

/// What [`Engine::prepare`] answers: the session home's surfaces.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Prepared {
    /// Files to write under the home.
    pub files: Vec<HomeFile>,
    /// Names to copy from the user's engine configuration into the home: the
    /// allowlist met with what was offered.
    pub copy_in: Vec<String>,
    /// The context file's absolute path.
    pub context_path: String,
}

/// One turn to drive.
#[derive(Clone, PartialEq, Eq)]
pub struct TurnRequest {
    pub prompt: String,
    /// The session home's absolute path.
    pub home: String,
    /// The context file [`Engine::prepare`] named.
    pub context_path: String,
    /// The engine's own session id from an earlier turn: this turn resumes it.
    pub resume: Option<String>,
    /// The caller's environment: the engine passes on only what it allows.
    pub env: BTreeMap<String, String>,
    pub cwd: Option<String>,
    /// The model to run; empty names none, and the engine passes no flag.
    pub model: String,
    /// The effort level to run at; empty names none, and the engine passes no
    /// flag.
    pub effort: String,
    /// How the config says the engine is run (program, home, credential
    /// source, environment passthrough).
    pub settings: crate::settings::EngineSettings,
}

impl fmt::Debug for TurnRequest {
    /// The environment's names alone: a request is logged, no value of it is.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("TurnRequest")
            .field("prompt", &self.prompt)
            .field("home", &self.home)
            .field("context_path", &self.context_path)
            .field("resume", &self.resume)
            .field("env", &crate::hook::RedactedEnv(&self.env))
            .field("cwd", &self.cwd)
            .field("model", &self.model)
            .field("effort", &self.effort)
            .field("settings", &self.settings)
            .finish()
    }
}

/// Why a turn failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FailureClass {
    RateLimited,
    CredentialRefused,
    EngineError,
    Interrupted,
}

impl FailureClass {
    /// The class's name, as the plan spells it.
    pub fn name(self) -> &'static str {
        match self {
            FailureClass::RateLimited => "rate_limited",
            FailureClass::CredentialRefused => "credential_refused",
            FailureClass::EngineError => "engine_error",
            FailureClass::Interrupted => "interrupted",
        }
    }
}

/// What a finished turn meant.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TurnOutcome {
    Answered {
        answer: String,
        /// The engine's own session id, if its output named one.
        native_id: Option<String>,
    },
    Failed {
        class: FailureClass,
        detail: String,
        native_id: Option<String>,
    },
}

/// How an engine is handed a credential (owner ruling 2026-11-08, item 8).
/// What the engine does not authenticate with is left empty, which is the
/// default: an engine that needs none (the mock) asks for none.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Credentials {
    /// The environment variables any one of which authenticates a CHILD
    /// agent's run. A child is given nothing else: no shared file.
    pub child_env: &'static [&'static str],
    /// The environment variables any one of which authenticates the human's
    /// PRIMARY session; with one set, it shares no login file.
    pub primary_env: &'static [&'static str],
    /// The user's own directory for this engine, relative to their home
    /// (claude: `.claude`; opencode: `.local/share/opencode`, its data
    /// directory), where its login file is.
    pub user_dir: &'static str,
    /// A variable that, set to an absolute path, moves the user's directory
    /// to `<its value>/<the second name>` (opencode: `XDG_DATA_HOME`,
    /// `opencode`); none, or unset, is [`Credentials::user_dir`].
    pub user_dir_variable: Option<(&'static str, &'static str)>,
    /// The name of the login file, in the user's directory and in the session
    /// home's [`Credentials::home_dir`] alike, that the primary may share by a
    /// link. Never read.
    pub login_file: Option<&'static str>,
    /// Where under the session home the engine looks for its login file:
    /// empty is the home itself (claude's config dir), opencode's is its
    /// relocated data directory (`xdg/data/opencode`).
    pub home_dir: &'static str,
    /// What the human is told to do when her primary session is handed no
    /// login (none there, a platform that cannot link, a link that failed):
    /// the engine's own way to sign in, in its own words.
    pub sign_in: &'static str,
    /// The engine runs without any login (opencode's free models): a login
    /// missing from its default place is nothing to say.
    pub login_optional: bool,
    /// The operating systems (`std::env::consts::OS` names) where the login
    /// is never shared by a link, and the human is told how to sign in
    /// instead (owner ruling 80: claude's macOS keychain item, a Windows
    /// symlink's privilege).
    pub no_link_on: &'static [&'static str],
    /// The operating systems where a link that cannot be made is replaced by
    /// a COPY of the login (owner ruling 80: opencode on Windows). The cost:
    /// a token the session refreshes is written to the copy, and the
    /// rotation can sign the human's own login out.
    pub copy_on: &'static [&'static str],
}

impl Credentials {
    /// An engine that is handed no credential: what [`Default`] gives, as a
    /// constant, for the struct update of another constant.
    pub const NONE: Credentials = Credentials {
        child_env: &[],
        primary_env: &[],
        user_dir: "",
        user_dir_variable: None,
        login_file: None,
        home_dir: "",
        sign_in: "",
        login_optional: false,
        no_link_on: &[],
        copy_on: &[],
    };
}

/// An agent engine's contribution to running a child.
pub trait Engine: Send + Sync {
    /// The engine's name.
    fn name(&self) -> &str;

    /// The session home's surfaces.
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError>;

    /// The command of ONE turn.
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError>;

    /// What a turn's finished command meant.
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome;

    /// How this engine is handed a credential.
    // `Default::default()` is the same value as `Credentials::default()`:
    // the mutant is equivalent.
    #[cfg_attr(test, mutants::skip)]
    fn credentials(&self) -> Credentials {
        Credentials::default()
    }

    /// The command of the human's terminal run.
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError>;

    /// Whether the human's terminal session ends when the engine's command
    /// exits, with no hook of the engine's to say so (opencode, owner ruling
    /// 58): the launcher then records the session's end itself, at the exit.
    /// An engine whose hooks report it (claude's SessionEnd) leaves it to them.
    fn session_ends_with_exit(&self) -> bool {
        false
    }

    /// The home files (relative to it) the engine merges its own keys into
    /// rather than writes whole, because the engine itself keeps state in
    /// them (claude's `.claude.json`, owner ruling 113): the launcher reads
    /// each before [`Engine::prepare`] and hands it in
    /// [`PrepareRequest::existing`].
    fn merges(&self) -> &'static [&'static str] {
        &[]
    }
}

/// The names on `allowed` that `offered` holds, sorted, once each. A name that is not exactly on the
/// allowlist is never copied in.
pub fn copy_in(allowed: &[&str], offered: &[String]) -> Vec<String> {
    let mut names: Vec<String> = offered
        .iter()
        .filter(|name| allowed.contains(&name.as_str()))
        .cloned()
        .collect();
    names.sort();
    names.dedup();
    names
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EngineError {
    /// The engine has not built this operation.
    NotYet { engine: String, operation: String },
    /// An effort level the engine does not take.
    UnknownEffort {
        engine: String,
        level: String,
        known: String,
    },
    /// `session_home: host` is configured but the caller's environment names
    /// no home to use.
    NoHostHome { engine: String },
}

impl fmt::Display for EngineError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for EngineError {
    fn code(&self) -> &str {
        match self {
            EngineError::NotYet { .. } => code!("engine-not-yet"),
            EngineError::UnknownEffort { .. } => code!("engine-unknown-effort"),
            EngineError::NoHostHome { .. } => code!("engine-no-host-home"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            EngineError::NotYet { engine, operation } => vec![
                ("engine", engine.as_str().into()),
                ("operation", operation.as_str().into()),
            ],
            EngineError::UnknownEffort {
                engine,
                level,
                known,
            } => vec![
                ("engine", engine.as_str().into()),
                ("level", level.as_str().into()),
                ("known", known.as_str().into()),
            ],
            EngineError::NoHostHome { engine } => vec![("engine", engine.as_str().into())],
        }
    }
}

#[cfg(test)]
#[path = "engine.test.rs"]
mod tests;
