//! Why a library command did not complete, and what it noticed on the way.
//! Every message is in `locales/en-US/messages.ftl`; a library or adapter
//! error passes through as itself.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use exit_code::{Exit, ExitClass};
use library::{BundleError, ProfileError, RefError};

/// This crate's en-US messages (`locales/en-US/messages.ftl`, ids under the
/// crate's name).
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// A command that did not complete.
#[derive(Debug)]
pub enum AppError {
    /// An adapter's error, as itself: something broke (I/O, a store).
    Cause(Box<dyn Diagnostic>),
    /// A refusal of the read model or of a write, as itself: the request or
    /// the content forbids it (ruling 152: a decline, exit 20).
    Declined(Box<dyn Diagnostic>),
    /// A config layer that is not YAML.
    ConfigUnparsable {
        path: String,
        cause: Box<angzarr_yaml::Error>,
    },
    /// A config layer written by a newer format than this build reads.
    ConfigNewer {
        path: String,
        found: u64,
        current: u64,
    },
    /// A config layer older than the oldest this build migrates (unstamped is 0).
    ConfigOlder {
        path: String,
        found: u64,
        oldest: u64,
    },
    /// A config layer that registers companions by bare name, as the
    /// generations before 9 wrote them; `found` is its generation.
    ConfigCompanionsBare {
        path: String,
        found: u64,
        names: String,
    },
    /// A config layer whose `schema_version` is not an integer.
    ConfigVersionUnreadable { path: String },
    /// A config with a key the schema does not know: each violation.
    ConfigUnknownKey { violations: String },
    /// A config that breaks the schema otherwise: each violation.
    ConfigBreaksSchema { violations: String },
    /// `config show <section>` of a section there is not.
    UnknownSection { section: String, available: String },
    /// A bundle that is there and cannot be read.
    BundleUnreadable {
        ask: String,
        cause: Box<BundleError>,
    },
    /// A project with no bundles directory.
    NoBundlesDir,
    /// A `bundle view` path that is not `<kind>/<name>`.
    ViewPath { cause: Box<RefError> },
    /// An item a `bundle view` path names that the bundle lacks.
    ViewMissing { kind: String, name: String },
    /// A kind a bundle cannot show.
    NotViewable { dir: String, path: String },
    /// An item not in its bundle: what is.
    ItemMissing {
        kind: String,
        name: String,
        available: String,
    },
    /// An operational prompt (owner ruling 128) the project's own bundle of
    /// the built-in one's name lacks: it overrides the built-in one whole.
    OperationalPromptMissing {
        bundle: String,
        prompt: String,
        dir: String,
    },
    /// A reference that is not `bundle#<kind>/name`.
    BadReference { expected: String, got: String },
    /// A reference of the wrong kind for the command.
    WrongKind {
        ask: String,
        got: String,
        want: String,
        expected: String,
    },
    /// An explicit `--format` the command does not honour.
    UnsupportedFormat { command: String, format: String },
    /// A skill a bundle does not ship.
    SkillNotFound { name: String, bundle: String },
    /// A skill no bundle ships.
    SkillNameNotFound { name: String },
    /// A `--bundle` that names no bundle.
    NoBundleNamed { name: String, known: String },
    /// A search with neither a query nor a tag.
    SearchNothing,
    /// A search `--type` nobody recognizes.
    SearchType { value: String },
    /// `search --remote`, until the remotes arrive.
    SearchRemote,
    /// An agent nobody defined.
    AgentNotFound { name: String },
    /// A launch naming an agent nobody defined.
    LaunchNoAgent { name: String },
    /// A launch with no project, so no default agent to bind.
    LaunchNoProject,
    /// A launch with agents but no default among them.
    LaunchNoDefault,
    /// A launch with no agent configured at all.
    LaunchNoAgents,
    /// An MCP server nobody ships.
    McpNotFound { name: String },
    /// An engine ctxloom does not drive.
    UnknownEngine { engine: String, known: String },
    /// An engine's `auth` the schema refuses (the text it holds is never
    /// repeated).
    EngineAuth { engine: String, why: String },
    /// A profile that cannot be composed.
    Compose {
        profile: String,
        cause: Box<ProfileError>,
    },
    /// A selection that loaded no fragment.
    NoFragments { detail: String },
    /// `--agent` with a flag it excludes.
    AgentExclusive { other: String },
    /// A saved command that cannot be loaded.
    SavedCommand { name: String },
    /// A `--workspace` that is neither none nor worktree.
    Workspace { value: String },
    /// A run that is not a dry run.
    RunNeedsDryRun,
    /// Fatal findings, after the plan was shown.
    StartupAborted { count: u64, findings: String },
}

impl AppError {
    /// A refusal of another crate (the read model's, a write's), as itself:
    /// a decline. An adapter's error is [`AppError::Cause`].
    pub fn cause(error: impl Diagnostic + 'static) -> AppError {
        AppError::Declined(Box::new(error))
    }

    /// Fatal findings, after the plan was shown: the run is refused.
    pub fn aborted(findings: &[crate::views::FindingRow]) -> AppError {
        let lines: String = findings
            .iter()
            .map(|f| {
                let fix = if f.remedy.is_empty() {
                    String::new()
                } else {
                    format!("\n    fix: {}", f.remedy)
                };
                format!("\n  - [{}] {}{fix}", f.kind, f.text)
            })
            .collect();
        AppError::StartupAborted {
            count: findings.len() as u64,
            findings: lines,
        }
    }
}

impl ExitClass for AppError {
    /// How a command this ends exits (rulings 149, 152, 153): failed (10)
    /// when an adapter broke, fatal findings (21) for the run plan's, and
    /// refused (20) for every other: each declines the request against the
    /// line, the config or the content (a name nobody has, an unreadable
    /// config or bundle, a reference that is no reference).
    fn exit(&self) -> Exit {
        match self {
            AppError::Cause(_) => Exit::FAILED,
            AppError::StartupAborted { .. } => Exit::FATAL_FINDINGS,
            _ => Exit::REFUSED,
        }
    }
}

impl fmt::Display for AppError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for AppError {}

impl Diagnostic for AppError {
    fn code(&self) -> &str {
        match self {
            AppError::Cause(cause) | AppError::Declined(cause) => cause.code(),
            AppError::ConfigUnparsable { .. } => code!("config-unparsable"),
            AppError::ConfigNewer { .. } => code!("config-newer"),
            AppError::ConfigOlder { .. } => code!("config-older"),
            AppError::ConfigCompanionsBare { .. } => code!("config-companions-bare"),
            AppError::ConfigVersionUnreadable { .. } => code!("config-version-unreadable"),
            AppError::ConfigUnknownKey { .. } => code!("config-unknown-key"),
            AppError::ConfigBreaksSchema { .. } => code!("config-breaks-schema"),
            AppError::UnknownSection { .. } => code!("unknown-section"),
            AppError::BundleUnreadable { .. } => code!("bundle-unreadable"),
            AppError::NoBundlesDir => code!("no-bundles-dir"),
            AppError::ViewPath { .. } => code!("view-path"),
            AppError::ViewMissing { .. } => code!("view-missing"),
            AppError::NotViewable { .. } => code!("not-viewable"),
            AppError::ItemMissing { .. } => code!("item-missing"),
            AppError::OperationalPromptMissing { .. } => code!("operational-prompt-missing"),
            AppError::BadReference { .. } => code!("bad-reference"),
            AppError::WrongKind { .. } => code!("wrong-kind"),
            AppError::UnsupportedFormat { .. } => code!("unsupported-format"),
            AppError::SkillNotFound { .. } => code!("skill-not-found"),
            AppError::SkillNameNotFound { .. } => code!("skill-name-not-found"),
            AppError::NoBundleNamed { .. } => code!("no-bundle-named"),
            AppError::SearchNothing => code!("search-nothing"),
            AppError::SearchType { .. } => code!("search-type"),
            AppError::SearchRemote => code!("search-remote"),
            AppError::AgentNotFound { .. } => code!("agent-not-found"),
            AppError::LaunchNoAgent { .. } => code!("launch-no-agent"),
            AppError::LaunchNoProject => code!("launch-no-project"),
            AppError::LaunchNoDefault => code!("launch-no-default"),
            AppError::LaunchNoAgents => code!("launch-no-agents"),
            AppError::McpNotFound { .. } => code!("mcp-not-found"),
            AppError::UnknownEngine { .. } => code!("unknown-engine"),
            AppError::EngineAuth { .. } => code!("engine-auth"),
            AppError::Compose { .. } => code!("compose"),
            AppError::NoFragments { .. } => code!("no-fragments"),
            AppError::AgentExclusive { .. } => code!("agent-exclusive"),
            AppError::SavedCommand { .. } => code!("saved-command"),
            AppError::Workspace { .. } => code!("workspace"),
            AppError::RunNeedsDryRun => code!("run-needs-dry-run"),
            AppError::StartupAborted { .. } => code!("startup-aborted"),
        }
    }

    // A table of one arm per error: its length is its variant count.
    #[allow(clippy::too_many_lines)]
    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            AppError::Cause(cause) | AppError::Declined(cause) => cause.args(),
            AppError::ConfigUnparsable { path, cause } => vec![
                ("path", path.as_str().into()),
                ("cause", Arg::Nested(cause.clone())),
            ],
            AppError::ConfigNewer { .. }
            | AppError::ConfigOlder { .. }
            | AppError::ConfigCompanionsBare { .. }
            | AppError::ConfigVersionUnreadable { .. } => version_args(self),
            AppError::ConfigUnknownKey { violations }
            | AppError::ConfigBreaksSchema { violations } => {
                vec![("violations", violations.as_str().into())]
            }
            AppError::UnknownSection { section, available } => vec![
                ("section", section.as_str().into()),
                ("available", available.as_str().into()),
            ],
            AppError::BundleUnreadable { ask, cause } => vec![
                ("ask", ask.as_str().into()),
                ("cause", Arg::Nested(cause.clone())),
            ],
            AppError::ViewPath { cause } => {
                vec![("cause", Arg::Nested(cause.clone()))]
            }
            AppError::ViewMissing { kind, name } => vec![
                ("kind", kind.as_str().into()),
                ("name", name.as_str().into()),
            ],
            AppError::NotViewable { dir, path } => {
                vec![("dir", dir.as_str().into()), ("path", path.as_str().into())]
            }
            AppError::ItemMissing {
                kind,
                name,
                available,
            } => vec![
                ("kind", kind.as_str().into()),
                ("name", name.as_str().into()),
                ("available", available.as_str().into()),
            ],
            AppError::OperationalPromptMissing {
                bundle,
                prompt,
                dir,
            } => vec![
                ("bundle", bundle.as_str().into()),
                ("prompt", prompt.as_str().into()),
                ("dir", dir.as_str().into()),
            ],
            AppError::BadReference { expected, got } => vec![
                ("expected", expected.as_str().into()),
                ("got", got.as_str().into()),
            ],
            AppError::WrongKind {
                ask,
                got,
                want,
                expected,
            } => vec![
                ("ask", ask.as_str().into()),
                ("got", got.as_str().into()),
                ("want", want.as_str().into()),
                ("expected", expected.as_str().into()),
            ],
            AppError::UnsupportedFormat { command, format } => vec![
                ("command", command.as_str().into()),
                ("format", format.as_str().into()),
            ],
            AppError::SkillNotFound { name, bundle } => vec![
                ("name", name.as_str().into()),
                ("bundle", bundle.as_str().into()),
            ],
            AppError::SkillNameNotFound { name } => vec![("name", name.as_str().into())],
            AppError::NoBundleNamed { name, known } => vec![
                ("name", name.as_str().into()),
                ("known", known.as_str().into()),
            ],
            AppError::SearchType { value } => vec![("value", value.as_str().into())],
            AppError::AgentNotFound { name }
            | AppError::LaunchNoAgent { name }
            | AppError::McpNotFound { name } => vec![("name", name.as_str().into())],
            AppError::UnknownEngine { engine, known } => vec![
                ("engine", engine.as_str().into()),
                ("known", known.as_str().into()),
            ],
            AppError::EngineAuth { engine, why } => vec![
                ("engine", engine.as_str().into()),
                ("why", why.as_str().into()),
            ],
            AppError::Compose { profile, cause } => vec![
                ("profile", profile.as_str().into()),
                ("cause", Arg::Nested(cause.clone())),
            ],
            AppError::NoFragments { detail } => vec![("detail", detail.as_str().into())],
            AppError::AgentExclusive { other } => vec![("other", other.as_str().into())],
            AppError::SavedCommand { name } => vec![("name", name.as_str().into())],
            AppError::Workspace { value } => vec![("value", value.as_str().into())],
            AppError::StartupAborted { count, findings } => vec![
                ("count", (*count).into()),
                ("findings", findings.as_str().into()),
            ],
            AppError::NoBundlesDir
            | AppError::LaunchNoProject
            | AppError::LaunchNoDefault
            | AppError::LaunchNoAgents
            | AppError::SearchNothing
            | AppError::SearchRemote
            | AppError::RunNeedsDryRun => Vec::new(),
        }
    }
}

/// The args of a refusal of a config layer's generation.
fn version_args(error: &AppError) -> Vec<(&str, Arg)> {
    match error {
        AppError::ConfigNewer {
            path,
            found,
            current,
        } => vec![
            ("path", path.as_str().into()),
            ("found", (*found).into()),
            ("current", (*current).into()),
        ],
        AppError::ConfigCompanionsBare { path, found, names } => vec![
            ("path", path.as_str().into()),
            ("found", (*found).into()),
            ("names", names.as_str().into()),
        ],
        AppError::ConfigOlder {
            path,
            found,
            oldest,
        } => vec![
            ("path", path.as_str().into()),
            ("found", (*found).into()),
            ("oldest", (*oldest).into()),
        ],
        AppError::ConfigVersionUnreadable { path } => vec![("path", path.as_str().into())],
        _ => Vec::new(),
    }
}

/// Something a command noticed and did not stop for: said on the error
/// stream as a warning, with what to do about it.
#[derive(Debug)]
pub enum Notice {
    /// A finding, in its own words.
    Finding { text: String, remedy: String },
    /// What an engine says of a run's plan (owner ruling 168), in the
    /// engine's own words: its code, args and catalog.
    Engine(run_ports::Advice),
    /// A bundle that was skipped, and why.
    BundleSkipped {
        manifest: String,
        dir: String,
        cause: BundleError,
    },
    /// A config file with no keys.
    ConfigKeyless { path: String },
    /// A `CTXLOOM_ROOT` that names no directory.
    RootIgnored { root: String },
    /// The dependencies installed from remotes could not be read.
    RemotesUnreadable { cause: angzarr_diagnostic::Report },
    /// A registered companion whose binary is not on the `PATH`.
    CompanionNotOnPath { name: String },
    /// A registered companion that could not be asked for its loadout.
    LoadoutProbeFailed { name: String, detail: String },
    /// A registered companion whose loadout does not parse.
    LoadoutUnparseable { name: String, cause: BundleError },
    /// An agent's runtime that replaces another layer's: never silent.
    /// `replaced_key` is the key that would have stood.
    RuntimeOverride {
        runtime: String,
        replaced_key: String,
        replaced: String,
        change: library::config::engines::Change,
    },
    /// An agent that names no engine had settings written into the default
    /// engine's block (owner ruling 68): `block` is `engines.<engine>`.
    DefaultEngineBlock { agent: String, block: String },
    /// The user's own settings of `engine` at `path` are not a JSON object
    /// (or could not be read), so a session home started without them
    /// (owner ruling 169): by path, never their content.
    UserSettingsLeftOut { engine: String, path: String },
    /// A worktree was asked for and none could be had, so the engine worked
    /// in the project instead: `reason` is the workspaces port's.
    WorkspaceUnavailable { reason: String },
    /// A launch on `engines.<engine>.session_home: host`: the engine runs on
    /// the user's own setup, with no session home of its own, which is
    /// unsafe as hygiene, not as security (j002200, step 6a: a session home
    /// is a relocation, not confinement).
    SessionHomeHost { engine: String },
    /// A run's checkout was left in place at its end (work in it, or one
    /// that would not go): by path and branch, and why.
    CheckoutLeft {
        path: String,
        branch: String,
        reason: String,
    },
}

impl Notice {
    /// A finding of the read model, as a notice.
    pub fn finding(finding: &library::findings::Finding) -> Notice {
        Notice::Finding {
            text: finding.text.clone(),
            remedy: finding.remedy.clone(),
        }
    }

    /// A warning in plain words.
    pub fn text(text: impl Into<String>) -> Notice {
        Notice::Finding {
            text: text.into(),
            remedy: String::new(),
        }
    }
}

impl fmt::Display for Notice {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for Notice {
    fn code(&self) -> &str {
        match self {
            Notice::Finding { .. } => code!("notice-finding"),
            Notice::Engine(advice) => advice.code(),
            Notice::BundleSkipped { .. } => code!("notice-bundle-skipped"),
            Notice::ConfigKeyless { .. } => code!("notice-config-keyless"),
            Notice::RootIgnored { .. } => code!("notice-root-ignored"),
            Notice::RemotesUnreadable { .. } => code!("notice-remotes-unreadable"),
            Notice::CompanionNotOnPath { .. } => code!("companion-not-on-path"),
            Notice::LoadoutProbeFailed { .. } => code!("loadout-probe-failed"),
            Notice::LoadoutUnparseable { .. } => code!("loadout-unparseable"),
            Notice::DefaultEngineBlock { .. } => code!("notice-default-engine-block"),
            Notice::UserSettingsLeftOut { .. } => code!("notice-user-settings-left-out"),
            Notice::WorkspaceUnavailable { .. } => code!("notice-workspace-unavailable"),
            Notice::SessionHomeHost { .. } => code!("notice-session-home-host"),
            Notice::CheckoutLeft { .. } => code!("notice-checkout-left"),
            Notice::RuntimeOverride { change, .. } => match change {
                library::config::engines::Change::Weakens => code!("notice-runtime-weakens"),
                library::config::engines::Change::Strengthens => {
                    code!("notice-runtime-strengthens")
                }
                library::config::engines::Change::OwnershipSwap => {
                    code!("notice-runtime-ownership-swap")
                }
            },
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            Notice::Finding { text, remedy } => vec![
                ("text", text.as_str().into()),
                ("remedy", remedy.as_str().into()),
            ],
            Notice::Engine(advice) => advice.args(),
            Notice::BundleSkipped {
                manifest,
                dir,
                cause,
            } => vec![
                ("manifest", manifest.as_str().into()),
                ("dir", dir.as_str().into()),
                ("cause", Arg::Nested(Box::new(cause.clone()))),
            ],
            Notice::ConfigKeyless { path } => {
                vec![("path", path.as_str().into())]
            }
            Notice::RootIgnored { root } => vec![("root", root.as_str().into())],
            Notice::RemotesUnreadable { cause } => {
                vec![("cause", Arg::Nested(Box::new(cause.clone())))]
            }
            Notice::CompanionNotOnPath { name } => vec![("name", name.as_str().into())],
            Notice::LoadoutProbeFailed { name, detail } => vec![
                ("name", name.as_str().into()),
                ("detail", detail.as_str().into()),
            ],
            Notice::LoadoutUnparseable { name, cause } => vec![
                ("name", name.as_str().into()),
                ("cause", Arg::Nested(Box::new(cause.clone()))),
            ],
            Notice::RuntimeOverride {
                runtime,
                replaced_key,
                replaced,
                ..
            } => vec![
                ("runtime", runtime.as_str().into()),
                ("key", replaced_key.as_str().into()),
                ("replaced", replaced.as_str().into()),
            ],
            Notice::DefaultEngineBlock { agent, block } => vec![
                ("agent", agent.as_str().into()),
                ("block", block.as_str().into()),
            ],
            Notice::UserSettingsLeftOut { engine, path } => vec![
                ("engine", engine.as_str().into()),
                ("path", path.as_str().into()),
            ],
            Notice::WorkspaceUnavailable { reason } => vec![("reason", reason.as_str().into())],
            Notice::SessionHomeHost { engine } => vec![("engine", engine.as_str().into())],
            Notice::CheckoutLeft {
                path,
                branch,
                reason,
            } => vec![
                ("path", path.as_str().into()),
                ("branch", branch.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
        }
    }
}

/// One value of every error and notice here, for a catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let mut all = error_samples();
    all.extend(crate::write::error::samples());
    all.extend(version_samples());
    all.extend(notice_samples());
    all
}

#[cfg(any(test, feature = "test-support"))]
fn version_samples() -> Vec<Box<dyn Diagnostic>> {
    let path = || "/p/config.yaml".to_string();
    vec![
        Box::new(AppError::ConfigNewer {
            path: path(),
            found: 9,
            current: 8,
        }),
        Box::new(AppError::ConfigCompanionsBare {
            path: path(),
            found: 8,
            names: "acme, ltk".to_string(),
        }),
        Box::new(AppError::ConfigOlder {
            path: path(),
            found: 6,
            oldest: 7,
        }),
        Box::new(AppError::ConfigVersionUnreadable { path: path() }),
    ]
}

// A table of one sample per error: its length is its variant count.
#[allow(clippy::too_many_lines)]
#[cfg(any(test, feature = "test-support"))]
fn error_samples() -> Vec<Box<dyn Diagnostic>> {
    let s = |text: &str| text.to_string();
    let yaml = angzarr_yaml::parse("a: [b\n").expect_err("an unterminated flow sequence");
    let bundle = BundleError::NoManifest { bundle: s("demo") };
    vec![
        Box::new(AppError::ConfigUnparsable {
            path: s("/p/.ctxloom/config.yaml"),
            cause: Box::new(yaml),
        }),
        Box::new(AppError::ConfigUnknownKey {
            violations: s("\n  - [config] unknown key"),
        }),
        Box::new(AppError::ConfigBreaksSchema {
            violations: s("\n  - [config] value"),
        }),
        Box::new(AppError::UnknownSection {
            section: s("nonsense"),
            available: s("editor, agents"),
        }),
        Box::new(AppError::BundleUnreadable {
            ask: s("demo"),
            cause: Box::new(bundle.clone()),
        }),
        Box::new(AppError::NoBundlesDir),
        Box::new(AppError::ViewPath {
            cause: Box::new(RefError::Selector {
                selector: s("fragments"),
            }),
        }),
        Box::new(AppError::ViewMissing {
            kind: s("fragment"),
            name: s("nope"),
        }),
        Box::new(AppError::NotViewable {
            dir: s("hooks"),
            path: s("hooks/a"),
        }),
        Box::new(AppError::ItemMissing {
            kind: s("fragment"),
            name: s("nope"),
            available: s("example"),
        }),
        Box::new(AppError::OperationalPromptMissing {
            bundle: s("ctxloom"),
            prompt: s("distill"),
            dir: s("/p/.ctxloom/content/bundles/v2/ctxloom"),
        }),
        Box::new(AppError::BadReference {
            expected: s("bundle#fragments/name"),
            got: s("x"),
        }),
        Box::new(AppError::WrongKind {
            ask: s("a#commands/b"),
            got: s("commands"),
            want: s("fragments"),
            expected: s("bundle#fragments/name"),
        }),
        Box::new(AppError::UnsupportedFormat {
            command: s("fragment show"),
            format: s("json"),
        }),
        Box::new(AppError::SkillNotFound {
            name: s("x"),
            bundle: s("demo"),
        }),
        Box::new(AppError::SkillNameNotFound { name: s("x") }),
        Box::new(AppError::NoBundleNamed {
            name: s("x"),
            known: s("demo"),
        }),
        Box::new(AppError::SearchNothing),
        Box::new(AppError::SearchType { value: s("widget") }),
        Box::new(AppError::SearchRemote),
        Box::new(AppError::AgentNotFound { name: s("x") }),
        Box::new(AppError::LaunchNoAgent { name: s("x") }),
        Box::new(AppError::LaunchNoProject),
        Box::new(AppError::LaunchNoDefault),
        Box::new(AppError::LaunchNoAgents),
        Box::new(AppError::McpNotFound { name: s("x") }),
        Box::new(AppError::UnknownEngine {
            engine: s("gpt-9"),
            known: s("claude-code, opencode"),
        }),
        Box::new(AppError::EngineAuth {
            engine: s("claude-code"),
            why: s("needs env or file"),
        }),
        Box::new(AppError::Compose {
            profile: s("dev"),
            cause: Box::new(ProfileError::Circular { name: s("dev") }),
        }),
        Box::new(AppError::NoFragments { detail: s("none") }),
        Box::new(AppError::AgentExclusive {
            other: s("profile"),
        }),
        Box::new(AppError::SavedCommand { name: s("review") }),
        Box::new(AppError::Workspace {
            value: s("elsewhere"),
        }),
        Box::new(AppError::RunNeedsDryRun),
        Box::new(AppError::StartupAborted {
            count: 1,
            findings: s("\n  - [ref] x"),
        }),
    ]
}

#[cfg(any(test, feature = "test-support"))]
fn runtime_override(change: library::config::engines::Change) -> Notice {
    Notice::RuntimeOverride {
        runtime: "host".to_string(),
        replaced_key: "engines.claude-code.runtime".to_string(),
        replaced: "container-rootless".to_string(),
        change,
    }
}

/// One value of every notice.
#[cfg(any(test, feature = "test-support"))]
fn notice_samples() -> Vec<Box<dyn Diagnostic>> {
    let s = |text: &str| text.to_string();
    let bundle = BundleError::NoManifest { bundle: s("demo") };
    vec![
        Box::new(Notice::Finding {
            text: s("a thing"),
            remedy: s("do a thing"),
        }),
        Box::new(Notice::BundleSkipped {
            manifest: s("/p/bundle.yaml"),
            dir: s("/p"),
            cause: bundle,
        }),
        Box::new(Notice::ConfigKeyless {
            path: s("/p/config.yaml"),
        }),
        Box::new(Notice::RootIgnored {
            root: s("/nowhere"),
        }),
        Box::new(Notice::RemotesUnreadable {
            cause: angzarr_diagnostic::Report::of(&BundleError::NoManifest { bundle: s("demo") }),
        }),
        Box::new(Notice::CompanionNotOnPath { name: s("reprise") }),
        Box::new(Notice::DefaultEngineBlock {
            agent: s("p"),
            block: s("engines.claude-code"),
        }),
        Box::new(Notice::UserSettingsLeftOut {
            engine: s("claude-code"),
            path: s("/home/u/.claude/settings.json"),
        }),
        Box::new(Notice::WorkspaceUnavailable {
            reason: s("worktree isolation: /work/proj is not a git repository"),
        }),
        Box::new(Notice::SessionHomeHost {
            engine: s("claude-code"),
        }),
        Box::new(Notice::CheckoutLeft {
            path: s("/h/.ctxloom/worktrees/calm-otter/amber-swift-owl/reviewer"),
            branch: s("ctxloom/amber-swift-owl/reviewer"),
            reason: s("it has uncommitted changes; left in place to preserve WIP"),
        }),
        Box::new(runtime_override(library::config::engines::Change::Weakens)),
        Box::new(runtime_override(
            library::config::engines::Change::Strengthens,
        )),
        Box::new(runtime_override(
            library::config::engines::Change::OwnershipSwap,
        )),
        Box::new(Notice::LoadoutProbeFailed {
            name: s("reprise"),
            detail: s("exit status: 3"),
        }),
        Box::new(Notice::LoadoutUnparseable {
            name: s("reprise"),
            cause: BundleError::LoadoutEmpty { bytes: 0 },
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
