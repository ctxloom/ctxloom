//! `config set <key> <value> [--home]` (step 10b's spec; T29: one key per
//! call): the key's value, as typed, coerced as an override is and judged
//! by the schema at its path before anything is written; then the key's
//! top-level section of the layer rewritten (the rest of the file as it
//! was), and the result judged again as a whole.

use angzarr_yaml::Value;
use ctxloom_ports::Scope;
use library::config::{overrides, schema};

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::write::WriteError;
use crate::Flags;

/// What `config set` did.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfigSet {
    /// The config file written, as shown.
    pub path: String,
    /// The dotted key.
    pub key: String,
    /// The value, as typed.
    pub value: String,
    /// Which layer: the project's, or the home's.
    pub scope: Scope,
}

impl Library {
    /// `config set <key> <value> [--home]`: the project's layer (the home's
    /// with `--home`, or outside a project).
    pub fn config_set(
        &self,
        flags: &Flags,
        key: &str,
        value: &str,
        home: bool,
    ) -> Outcome<ConfigSet> {
        self.run(flags, |p| p.config_set(key, value, home))
    }
}

/// `value` set at `path` below `section` (a map made where none is).
fn set_at(section: Option<Value>, path: &[String], value: Value) -> Value {
    let Some((first, rest)) = path.split_first() else {
        return value;
    };
    let mut here = match section {
        Some(map @ Value::Map(_)) => map,
        _ => Value::Map(Vec::new()),
    };
    let below = here.get(first).cloned();
    *here.entry(first) = set_at(below, rest, value);
    here
}

impl Project {
    fn config_set(&self, key: &str, value: &str, home: bool) -> Result<ConfigSet, AppError> {
        let path: Vec<String> = key.split('.').map(str::to_string).collect();
        let breaks = |detail: String| -> AppError {
            WriteError::ConfigBreaks {
                violations: format!("\n  - [config] {key}: {detail}"),
            }
            .into()
        };
        if path.iter().any(String::is_empty) {
            return Err(breaks("a key is dotted names, none empty".to_string()));
        }
        if !schema::known_path(&path) {
            return Err(breaks("the configuration has no such key".to_string()));
        }
        let coerced = overrides::coerce(value);
        schema::validate_at(&path, &coerced).map_err(breaks)?;
        let scope = if home || self.no_project() {
            Scope::Home
        } else {
            Scope::Project
        };
        let written = self.update_config(scope, &path[0], |current| {
            Ok(Some(set_at(current, &path[1..], coerced)))
        })?;
        Ok(ConfigSet {
            path: written,
            key: key.to_string(),
            value: value.to_string(),
            scope,
        })
    }
}

#[cfg(test)]
#[path = "config_set.test.rs"]
mod tests;
