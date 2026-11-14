//! `companion add|remove|enable|disable` (Go's `operations.AddCompanion`,
//! `RemoveCompanion`). A companion is registered in the user's home config by
//! its NAME alone (OD-10): never a path or a command, so the registration
//! holds wherever its program is installed, and the program is found where
//! the agent runs, on that place's `PATH`, each time. Adding one asks its
//! program for its loadout once, as Go's add does, before anything is
//! recorded. Its state is a word (OD-9): `companions.<n>.state: enabled`
//! (the default) or `disabled`, which keeps it registered and contributing
//! nothing. Each write replaces the one section it owns, in one atomic write,
//! and a write that changes nothing writes nothing (D3).

use angzarr_yaml::Value;
use ctxloom_ports::{CompanionLoadout, Scope};
use library::companion::{program, State};

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::write::views::{CompanionAdded, CompanionRemoved, CompanionStateSet, Preview, Removal};
use crate::write::WriteError;
use crate::Flags;

impl Library {
    /// `companion add <name>`: its program is found on the `PATH` and asked
    /// for its loadout before anything is recorded.
    pub fn companion_add(&self, flags: &Flags, name: &str) -> Outcome<CompanionAdded> {
        self.run(flags, |p| p.companion_add(name))
    }

    /// `companion remove <name> [--yes]`.
    pub fn companion_remove(
        &self,
        flags: &Flags,
        name: &str,
        yes: bool,
    ) -> Outcome<Removal<CompanionRemoved>> {
        self.run(flags, |p| p.companion_remove(name, yes))
    }

    /// Whether the companion `name`'s program is on the `PATH` of the
    /// caller's site: looked for, never run (init's offer of the shipped
    /// companions).
    pub fn companion_available(&self, flags: &Flags, name: &str) -> bool {
        self.run(flags, |p| {
            Ok(p.companions
                .as_ref()
                .is_some_and(|probe| probe.find(&program(name), &p.probe_site).is_some()))
        })
        .result
        .unwrap_or(false)
    }

    /// `companion enable <name>` and `companion disable <name>`: the
    /// registration's state set in the home config (OD-9).
    pub fn companion_set_state(
        &self,
        flags: &Flags,
        name: &str,
        state: State,
    ) -> Outcome<CompanionStateSet> {
        self.run(flags, |p| p.companion_set_state(name, state))
    }
}

/// Whether `name` can be a companion's: it starts with a letter or digit and
/// goes on in letters, digits, dots, hyphens and underscores.
pub(crate) fn valid_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphanumeric())
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-'))
}

/// A registration in `state`, as the config holds it.
fn registration(state: State) -> Value {
    Value::Map(vec![(
        "state".to_string(),
        Value::Scalar(state.word().to_string()),
    )])
}

/// The companions a `companions` value registers, as `(name, registration)`:
/// a mapping's entries, or the names of generation 8's list (each enabled).
fn registered(current: Option<&Value>) -> Vec<(String, Value)> {
    match current {
        Some(Value::Map(entries)) => entries.to_vec(),
        Some(Value::Seq(names)) => names
            .iter()
            .filter_map(Value::as_str)
            .map(|n| (n.to_string(), Value::Map(Vec::new())))
            .collect(),
        _ => Vec::new(),
    }
}

/// The state a registration's body says.
fn state_of(body: &Value) -> State {
    State::of(body.get("state").and_then(Value::as_str).unwrap_or(""))
}

impl Project {
    fn companion_add(&self, name: &str) -> Result<CompanionAdded, AppError> {
        if !valid_name(name) {
            return Err(WriteError::CompanionName {
                name: name.to_string(),
            }
            .into());
        }
        self.write_ports()?;
        let Some(probe) = self.companions.clone() else {
            return Err(WriteError::NotWired {}.into());
        };
        let bin = program(name);
        match probe.loadout(&bin, &self.probe_site) {
            CompanionLoadout::Loaded(_) => {}
            CompanionLoadout::Absent => {
                return Err(WriteError::CompanionProgramMissing {
                    program: bin,
                    name: name.to_string(),
                }
                .into())
            }
            CompanionLoadout::NoLoadout(detail) | CompanionLoadout::ProbeFailed(detail) => {
                return Err(WriteError::NotACompanion {
                    command: bin,
                    detail,
                }
                .into())
            }
            CompanionLoadout::Silent => {
                return Err(WriteError::CompanionSilent { command: bin }.into())
            }
        }
        let mut added = false;
        self.update_config(Scope::Home, "companions", |current| {
            let mut held = registered(current.as_ref());
            if !held.iter().any(|(n, _)| n == name) {
                held.push((name.to_string(), registration(State::Enabled)));
                added = true;
            }
            Ok(Some(Value::Map(held)))
        })?;
        Ok(CompanionAdded {
            name: name.to_string(),
            program: bin,
            added,
        })
    }

    fn companion_remove(
        &self,
        name: &str,
        yes: bool,
    ) -> Result<Removal<CompanionRemoved>, AppError> {
        let missing = || WriteError::CompanionNotRegistered {
            name: name.to_string(),
        };
        if !yes {
            if !self.config.companions().iter().any(|c| c.name == name) {
                return Err(missing().into());
            }
            return Ok(Removal::Preview(Preview::of(
                format!("companion \"{name}\""),
                Vec::new(),
                format!("ctxloom companion remove {name} --yes"),
            )));
        }
        self.update_config(Scope::Home, "companions", |current| {
            let mut held = registered(current.as_ref());
            let Some(at) = held.iter().position(|(n, _)| n == name) else {
                return Err(missing().into());
            };
            held.remove(at);
            let any = !held.is_empty();
            Ok(any.then_some(Value::Map(held)))
        })?;
        Ok(Removal::Done(CompanionRemoved {
            name: name.to_string(),
            removed: true,
        }))
    }

    /// The home registration of `name` set to `state`; one already so
    /// writes nothing. A name the home does not register is refused (a
    /// project's companion is the project's to change).
    fn companion_set_state(&self, name: &str, state: State) -> Result<CompanionStateSet, AppError> {
        let mut changed = false;
        self.update_config(Scope::Home, "companions", |current| {
            let mut held = registered(current.as_ref());
            let Some((_, body)) = held.iter_mut().find(|(n, _)| n == name) else {
                return Err(WriteError::CompanionNotRegistered {
                    name: name.to_string(),
                }
                .into());
            };
            if state_of(body) != state || body.get("state").is_none() {
                changed = state_of(body) != state;
                *body = registration(state);
            }
            Ok(Some(Value::Map(held)))
        })?;
        Ok(CompanionStateSet {
            name: name.to_string(),
            program: program(name),
            state,
            changed,
        })
    }
}

#[cfg(test)]
#[path = "companions.test.rs"]
mod tests;
