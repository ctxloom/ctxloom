//! An ask's `allow-always` (step 8 U4, owner Q3, and its ruling after U4):
//! the rule the ask implies (`Bash(rm -rf build/)`) written into the asking
//! agent's `agents.<agent>.engines.<engine>.permissions` `allow`, so every
//! later launch carries it (T21 regenerates the session home's settings from
//! the config at each). Written WHERE IT TAKES EFFECT: an agent defined by
//! the project replaces the home's of the same name whole, so the rule goes
//! into the project's definition when the project defines the agent, else
//! the home's (else, defined by neither, the project's). It starts from that
//! definition's own document for the engine, else the effective one, which
//! is then that definition's engine default (`defaultMode: acceptEdits`), so
//! nothing a launch had is lost. The preview names the file.
//! Without `yes` it is the preview: nothing written.

use angzarr_yaml::Value;
use ctxloom_ports::Scope;

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::write::WriteError;
use crate::Flags;
use library::config::engines as config_engines;

/// What an `allow-always` writes, or would.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AlwaysAllowed {
    /// The config file written (or that would be).
    pub path: String,
    /// `agents.<agent>.engines.<engine>.permissions`.
    pub key: String,
    pub rule: String,
    /// The permissions document as written, JSON text in its own order.
    pub document: String,
    /// Whether this call wrote it (false: the preview, or the rule was
    /// there already).
    pub applied: bool,
}

/// `document` with `rule` in its `allow` list (once); a document that is
/// no mapping becomes one.
fn allowing(document: Option<Value>, rule: &str) -> Value {
    let mut document = match document {
        Some(map @ Value::Map(_)) => map,
        _ => Value::Map(Vec::new()),
    };
    let mut allow = match document.get("allow") {
        Some(Value::Seq(items)) => items.clone(),
        _ => Vec::new(),
    };
    if !allow.iter().any(|item| item.as_str() == Some(rule)) {
        allow.push(Value::Scalar(rule.to_string()));
    }
    *document.entry("allow") = Value::Seq(allow);
    document
}

/// Whether the agents section `agents` defines `agent`.
fn defines(agents: &Option<Value>, agent: &str) -> bool {
    agents.as_ref().and_then(|a| a.get(agent)).is_some()
}

impl Library {
    /// `session approvals answer <ask> allow-always [--yes]`: `rule`
    /// written into agent `agent`'s permissions for `engine`, or (without
    /// `yes`) what would be.
    pub fn allow_always(
        &self,
        flags: &Flags,
        agent: &str,
        engine: &str,
        rule: &str,
        yes: bool,
    ) -> Outcome<AlwaysAllowed> {
        self.run(flags, |p| p.allow_always(agent, engine, rule, yes))
    }
}

impl Project {
    /// The agents section the `scope` layer holds.
    fn agents_of(&self, scope: Scope) -> Result<Option<Value>, AppError> {
        let text = self
            .files
            .layer(scope)
            .map_err(AppError::Cause)?
            .map(|layer| layer.text)
            .unwrap_or_default();
        library::write::section::section(&text, "agents").map_err(|e| {
            WriteError::ConfigCorrupt {
                path: self.files.layer_path(scope),
                cause: e.to_string(),
            }
            .into()
        })
    }

    fn allow_always(
        &mut self,
        agent: &str,
        engine: &str,
        rule: &str,
        yes: bool,
    ) -> Result<AlwaysAllowed, AppError> {
        self.require_project()?;
        let effective = config_engines::effective_on(&self.config.value, agent, engine).map_err(
            |_| -> AppError {
                WriteError::AgentMissing {
                    name: agent.to_string(),
                }
                .into()
            },
        )?;
        let (project, home) = (
            self.agents_of(Scope::Project)?,
            self.agents_of(Scope::Home)?,
        );
        // The definition a launch uses: the project's replaces the home's.
        let (scope, layer) = match (defines(&project, agent), defines(&home, agent)) {
            (false, true) => (Scope::Home, home),
            _ => (Scope::Project, project),
        };
        // That definition's own document for the engine, when it holds one;
        // else the effective one, which is then its engine's default.
        let held = layer
            .as_ref()
            .and_then(|a| a.get(agent))
            .and_then(|entry| entry.get("engines"))
            .and_then(|blocks| blocks.get(engine))
            .and_then(|block| block.get("permissions"))
            .cloned();
        let document = allowing(held.or(effective.permissions.map(|p| p.value)), rule);
        let mut allowed = AlwaysAllowed {
            path: self.files.layer_path(scope),
            key: format!("agents.{agent}.engines.{engine}.permissions"),
            rule: rule.to_string(),
            document: crate::json::document_text(&document),
            applied: false,
        };
        if !yes {
            return Ok(allowed);
        }
        let mut changed = false;
        self.update_config(scope, "agents", |current| {
            let mut agents = match current {
                Some(map @ Value::Map(_)) => map,
                _ => Value::Map(Vec::new()),
            };
            let entry = agents.entry(agent);
            if !matches!(entry, Value::Map(_)) {
                *entry = Value::Map(Vec::new());
            }
            let blocks = entry.entry("engines");
            if !matches!(blocks, Value::Map(_)) {
                *blocks = Value::Map(Vec::new());
            }
            let block = blocks.entry(engine);
            if !matches!(block, Value::Map(_)) {
                *block = Value::Map(Vec::new());
            }
            let held = block.entry("permissions");
            changed = *held != document;
            *held = document.clone();
            Ok(Some(agents))
        })?;
        allowed.applied = changed;
        Ok(allowed)
    }
}

#[cfg(test)]
#[path = "always.test.rs"]
mod tests;
