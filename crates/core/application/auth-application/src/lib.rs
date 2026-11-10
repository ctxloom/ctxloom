//! `ctxloom auth status` (S12), and the rules the doctor's credentials check
//! shares with it.
//!
//! ctxloom never collects, stores or mints a credential: a run reads the
//! engine's token from the environment it is launched in. This reports, per
//! engine, whether that credential is exported, and never its value: the only
//! thing the [`ExportedVars`] port can say of a variable is whether it is
//! exported, so a value cannot reach any output from here.

use std::sync::Arc;

pub use ctxloom_ports::ExportedVars;
use library::config::{engines, schema};
use serde::Serialize;

/// The mode every row of a credential from the environment is in.
const TOKEN: &str = "token";

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
    pub present: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub vars: Vec<String>,
    /// The variable the credential was found in (a name, never a value).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub via: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub remedy: String,
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
    pub remedy: String,
}

/// An engine's credential source and whether a run is refused without it.
struct Need {
    engine: String,
    vars: Vec<String>,
    required: bool,
    builtin: bool,
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
    /// have no row. Engines in name order. `layers` are the config files'
    /// texts, lowest first.
    pub fn status(&self, layers: &[String]) -> Vec<Row> {
        let config = engines::merged(layers);
        needs(&config, &schema::ENGINES.map(ToString::to_string))
            .into_iter()
            .filter_map(|need| {
                let found = need.vars.iter().find(|v| self.env.is_exported(v)).cloned();
                if found.is_none() && !need.required {
                    return None;
                }
                Some(Row {
                    engine: need.engine.clone(),
                    mode: TOKEN,
                    present: found.is_some(),
                    vars: found.iter().cloned().collect(),
                    remedy: if found.is_some() {
                        String::new()
                    } else {
                        remedy(&need)
                    },
                    via: found.unwrap_or_default(),
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
            .filter(|need| need.required)
            .all(|need| need.vars.iter().any(|v| self.env.is_exported(v)))
    }

    /// For each of `engines` whose effective `auth` reads the environment
    /// (each once), whether it could authenticate a run. `login` and `none`
    /// read nothing from the environment. `layers` as for [`Auth::status`].
    pub fn needed(&self, engines: &[String], layers: &[String]) -> Vec<Verdict> {
        let config = engines::merged(layers);
        needs(&config, engines)
            .into_iter()
            .map(|need| Verdict {
                ready: !need.required || need.vars.iter().any(|v| self.env.is_exported(v)),
                mode: TOKEN,
                remedy: remedy(&need),
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
            let engines::AuthSource::Credential { env, .. } = source.value else {
                return None;
            };
            if env.is_empty() {
                return None;
            }
            let builtin = source.source == engines::Source::Default;
            Some(Need {
                engine: engine.clone(),
                required: !builtin || engines::AuthSource::required(engine),
                vars: env,
                builtin,
            })
        })
        .collect()
}

/// What to do about a credential that is not exported.
fn remedy(need: &Need) -> String {
    if need.engine == "claude-code" && need.builtin {
        return CLAUDE_REMEDY.to_string();
    }
    format!("export {}", need.vars.join(" or "))
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
