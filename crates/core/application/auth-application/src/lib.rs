//! `ctxloom auth status` (S12), and the rules the doctor's credentials check
//! shares with it.
//!
//! ctxloom never collects, stores or mints a credential: a run reads the
//! engine's token from the environment it is launched in. This reports, per
//! engine, whether that credential is exported, and never its value: the only
//! thing the [`ExportedVars`] port can say of a variable is whether it is
//! exported, so a value cannot reach any output from here.

use std::fmt;
use std::sync::Arc;

use angzarr_diagnostic::{Arg, Diagnostic};
pub use ctxloom_ports::ExportedVars;
use library::config::{engines, schema};
use serde::Serialize;

/// The mode every row of a credential from the environment is in.
const TOKEN: &str = "token";

/// The mode of a source that is a credentials file alone (owner ruling 53).
const FILE: &str = "file";

/// Who a file alone serves: the human's own primary session.
const PRIMARY: &str = "primary";

/// How the human supplies claude's token. It is minted with claude's own flow
/// and kept in their environment or secret manager: Anthropic does not allow
/// a third party to collect, store or intermediate Claude.ai credentials, so
/// no ctxloom command takes one.
const CLAUDE_REMEDY: &str = "run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN (or store it in your secret manager)";

/// One row of `auth status`: whether an engine's credential is exported, the
/// variable it is read from when it is (never its value), and the fix when it
/// is not.
#[derive(Clone, PartialEq, Eq, Debug, Serialize)]
pub struct Row {
    pub engine: String,
    pub mode: &'static str,
    /// Whether the credential is exported: a variable row's alone. A file
    /// row has none (owner ruling 65): nothing here looks at a file.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub present: Option<bool>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub vars: Vec<String>,
    /// The variable the credential was found in (a name, never a value).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub via: String,
    /// What the row means, when a line of its own says it: a file alone's
    /// is `runs-file-only-credential`'s message (owner ruling 81).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub message: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub remedy: String,
    /// Who the credential serves when it is not every run: `primary` for a
    /// source that is a file alone (owner ruling 53), which the human's own
    /// interactive session may use and no child agent can.
    #[serde(skip_serializing_if = "str::is_empty")]
    pub usable_by: &'static str,
}

/// Whether one configured engine could authenticate a run.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Verdict {
    pub engine: String,
    pub mode: &'static str,
    /// The variables any one of which authenticates the engine.
    pub vars: Vec<String>,
    /// False only where a required credential is not exported.
    pub ready: bool,
    /// What the verdict means, when it says more than its mode: a file
    /// alone's is `runs-file-only-credential`'s message (ruling 81).
    pub message: String,
    pub remedy: String,
    /// The source is a file alone: the primary session may run on it, a
    /// child agent cannot (owner ruling 53).
    pub primary_only: bool,
}

/// The catalog of the runs messages [`FileOnly`] is said in.
pub use runs::EN_US as RUNS_EN_US;

/// A warning that an engine in use authenticates no child: its source is a
/// file alone. Said as the refusal a child's run of it meets
/// (`runs-file-only-credential`), so the words and the fix are one.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct FileOnly(runs::RunRejection);

impl FileOnly {
    fn new(engine: &str, variables: Vec<String>) -> FileOnly {
        FileOnly(runs::RunRejection::FileOnlyCredential {
            engine: engine.to_string(),
            variables: variables.join(", "),
        })
    }
}

impl fmt::Display for FileOnly {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(f)
    }
}

impl Diagnostic for FileOnly {
    fn code(&self) -> &str {
        self.0.code()
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        self.0.args()
    }
}

/// An engine's credential source and whether a run is refused without it.
struct Need {
    engine: String,
    vars: Vec<String>,
    required: bool,
    builtin: bool,
    /// The file of a source that is a file alone, which serves the primary
    /// session only; `vars` is then empty.
    file: Option<String>,
}

/// The use case, over the environment it is launched in.
pub struct Auth {
    env: Arc<dyn ExportedVars + Send + Sync>,
}

impl Auth {
    pub fn new(env: Arc<dyn ExportedVars + Send + Sync>) -> Auth {
        Auth { env }
    }

    /// Per engine whose effective `auth` (the config's `engines.<engine>.auth`,
    /// else the engine's built-in source) reads the environment: present with
    /// the variable found, or missing with the fix. A credential that is
    /// optional (opencode's built-in endpoint key) and not exported has no
    /// row (the run is keyless). A login shared in place (`login`) and `none`
    /// have no row. A source that is a file alone has a `file` row, `via` its
    /// path, usable by the primary session and by no child (ruling 53), with
    /// no `present` (ruling 65: nothing here looks at a file). Engines in
    /// name order. `layers` are the config files' texts, lowest first.
    pub fn status(&self, layers: &[String]) -> Vec<Row> {
        let config = engines::merged(layers);
        needs(
            &config,
            &schema::ENGINES
                .iter()
                .map(ToString::to_string)
                .collect::<Vec<_>>(),
        )
        .into_iter()
        .filter_map(|need| {
            if let Some(file) = &need.file {
                return Some(Row {
                    engine: need.engine.clone(),
                    mode: FILE,
                    present: None,
                    vars: Vec::new(),
                    via: file.clone(),
                    message: String::new(),
                    remedy: remedy(&need),
                    usable_by: PRIMARY,
                });
            }
            let found = need.vars.iter().find(|v| self.env.is_exported(v)).cloned();
            if found.is_none() && !need.required {
                return None;
            }
            Some(Row {
                engine: need.engine.clone(),
                mode: TOKEN,
                present: Some(found.is_some()),
                vars: found.iter().cloned().collect(),
                remedy: if found.is_some() {
                    String::new()
                } else {
                    remedy(&need)
                },
                via: found.unwrap_or_default(),
                message: String::new(),
                usable_by: "",
            })
        })
        .collect()
    }

    /// Whether the credential the delegated agents authenticate with is
    /// exported, by the effective `engines.<engine>.auth` of each engine the
    /// agents use (`layers` as for [`Auth::status`]). Never its value.
    pub fn agent_token_exported(&self, layers: &[String]) -> bool {
        let config = engines::merged(layers);
        needs(&config, &engines::engines_in_use(&config))
            .iter()
            .filter(|need| need.required && need.file.is_none())
            .all(|need| need.vars.iter().any(|v| self.env.is_exported(v)))
    }

    /// The engines the delegated agents use whose effective `auth` is a file
    /// alone (owner ruling 64): no child of theirs can authenticate, which
    /// init warns of, in the words a child's run is refused with
    /// (`runs-file-only-credential` and its fix). `layers` as for
    /// [`Auth::status`].
    pub fn file_only(&self, layers: &[String]) -> Vec<FileOnly> {
        let config = engines::merged(layers);
        needs(&config, &engines::engines_in_use(&config))
            .into_iter()
            .filter(|need| need.file.is_some())
            .map(|need| FileOnly::new(&need.engine, builtin_variables(&need.engine)))
            .collect()
    }

    /// For each of `engines` whose effective `auth` reads the environment
    /// (each once), whether it could authenticate a run. `login` and `none`
    /// read nothing from the environment. `layers` as for [`Auth::status`].
    pub fn needed(&self, engines: &[String], layers: &[String]) -> Vec<Verdict> {
        let config = engines::merged(layers);
        needs(&config, engines)
            .into_iter()
            .map(|need| Verdict {
                ready: need.file.is_some()
                    || !need.required
                    || need.vars.iter().any(|v| self.env.is_exported(v)),
                mode: if need.file.is_some() { FILE } else { TOKEN },
                message: String::new(),
                remedy: remedy(&need),
                primary_only: need.file.is_some(),
                vars: need.vars,
                engine: need.engine,
            })
            .collect()
    }
}

/// The engines among `wanted` (each once, in name order) whose source is
/// variables, with what it takes to satisfy each.
fn needs(config: &angzarr_yaml::Value, wanted: &[String]) -> Vec<Need> {
    let mut names: Vec<&String> = wanted.iter().collect();
    names.sort();
    names.dedup();
    names
        .into_iter()
        .filter_map(|engine| {
            // A source the schema refuses is the loader's to report.
            let source = engines::engine_auth(config, engine).ok()?;
            // A file is only the human's own primary session's credential: a
            // source with no variable has nothing to look for in the
            // environment a child is given.
            let engines::AuthSource::Credential { env, file } = source.value else {
                return None;
            };
            let builtin = source.source == engines::Source::Default;
            // A file alone serves the primary session and no child (ruling
            // 53): its row says so.
            if env.is_empty() {
                return file.map(|file| Need {
                    engine: engine.clone(),
                    required: true,
                    vars: Vec::new(),
                    builtin,
                    file: Some(file),
                });
            }
            Some(Need {
                engine: engine.clone(),
                required: !builtin || engines::AuthSource::required(engine),
                vars: env,
                builtin,
                file: None,
            })
        })
        .collect()
}

/// The variables of `engine`'s built-in source: what a source that is a file
/// alone is told to name instead.
fn builtin_variables(engine: &str) -> Vec<String> {
    match engines::AuthSource::builtin(engine) {
        engines::AuthSource::Credential { env, .. } => env,
        _ => Vec::new(),
    }
}

/// What to do about a credential that is not exported.
fn remedy(need: &Need) -> String {
    if need.file.is_some() {
        // An engine with no built-in variable (a test engine) is told to
        // name one.
        let named = match builtin_variables(&need.engine) {
            env if env.is_empty() => "a variable".to_string(),
            env => format!("one of {}", env.join(", ")),
        };
        return format!(
            "a child agent needs an environment credential: name {named} in engines.{}.auth.env and export it",
            need.engine
        );
    }
    if need.engine == "claude-code" && need.builtin {
        return CLAUDE_REMEDY.to_string();
    }
    format!("export {}", need.vars.join(" or "))
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
