//! `CTXLOOM_PERMIT_<NAME>` (owner ruling 78): the human's escape hatch for a
//! variable an engine's environment withholds.
//!
//! A variable `CTXLOOM_PERMIT_<NAME>=v` in the caller's environment reaches
//! the engine's command as `<NAME>=v`, whatever the engine's allowlist, its
//! blocks (opencode's `OPENCODE_*`) or the config say, for a child's turn and
//! the human's terminal alike. The `CTXLOOM_PERMIT_*` variable itself is never
//! passed. A permit never brings in a `CTXLOOM_*` name (the run's own hook
//! variables and home are the run's), nor any other name ctxloom sets for the
//! engine (owner ruling 78b: a denylist DERIVED from what was set, see
//! [`owned_names`], never an allowlist of permittable names). It may bring a child the primary's own
//! `ANTHROPIC_AUTH_TOKEN` (owner ruling 78a: the explicit permit wins over the
//! primary-only rule; the cost is that such a child holds the primary's token,
//! no credential of its own). A value is never shown: [`Permits`]' `Debug`
//! names the variables alone, and what was ignored is said by name.
//!
//! Pure: the launcher applies it to the command the engine built
//! ([`apply_permits`]); the run says what it ignored ([`permits`]).

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;

/// The prefix of a permit's name.
pub const PERMIT_PREFIX: &str = "CTXLOOM_PERMIT_";

/// The prefix of the names a permit never brings in: the run's own.
const RUNS_OWN: &str = "CTXLOOM_";

/// A permit that was not followed, by the permit's name (never its value).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Ignored {
    /// It names a `CTXLOOM_*` variable: the run's own are never replaced.
    RunsOwn { permit: String },
    /// What follows the prefix is no variable name.
    NoName { permit: String },
    /// It names a variable ctxloom itself sets for the engine (owner ruling
    /// 78b): one the adapter computes (`HOME`, an XDG base, a config dir) or
    /// the run builds (`PATH` with the program's directory first).
    Owned { permit: String },
}

/// What the caller's permits bring into an engine's command.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct Permits {
    /// The variables to set, by their own names.
    pub forwarded: BTreeMap<String, String>,
    /// The permits not followed, in name order.
    pub ignored: Vec<Ignored>,
}

impl fmt::Debug for Permits {
    /// The names alone: a permit's value is a credential as often as not.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Permits")
            .field("forwarded", &self.forwarded.keys().collect::<Vec<_>>())
            .field("ignored", &self.ignored)
            .finish()
    }
}

/// Whether `name` is an environment variable's name.
fn is_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

/// The names ctxloom itself set in `command`, a command's whole environment
/// built from `caller`'s (owner ruling 78b): every name whose value is not the
/// caller's as it was passed through (the adapter computed it), and `also`,
/// the names the run set in the caller's environment before the engine saw
/// it. Derived from what was set, so it cannot drift from the adapters.
pub fn owned_names(
    caller: &BTreeMap<String, String>,
    command: &BTreeMap<String, String>,
    also: &[String],
) -> BTreeSet<String> {
    command
        .iter()
        .filter(|(name, value)| caller.get(*name) != Some(*value))
        .map(|(name, _)| name.clone())
        .chain(also.iter().cloned())
        .collect()
}

/// The permits of `caller`, `owned` the names ctxloom sets for the engine
/// ([`owned_names`]): the same for a child's turn and the human's terminal
/// session.
pub fn permits(caller: &BTreeMap<String, String>, owned: &BTreeSet<String>) -> Permits {
    let mut got = Permits::default();
    for (permit, value) in caller {
        let Some(name) = permit.strip_prefix(PERMIT_PREFIX) else {
            continue;
        };
        let permit = permit.clone();
        if !is_name(name) {
            got.ignored.push(Ignored::NoName { permit });
        } else if name.starts_with(RUNS_OWN) {
            got.ignored.push(Ignored::RunsOwn { permit });
        } else if owned.contains(name) {
            got.ignored.push(Ignored::Owned { permit });
        } else {
            got.forwarded.insert(name.to_string(), value.clone());
        }
    }
    got
}

/// `env`, a command's whole environment built from `caller`'s, with
/// `caller`'s permits applied: no `CTXLOOM_PERMIT_*` variable left in it, each
/// permit followed set by its own name over what the engine passed through,
/// never over a name ctxloom set ([`owned_names`], `also` the run's own).
/// The permits it did not follow. (A command's `Debug` shows no value of its
/// environment, so none of these renders.)
pub fn apply_permits(
    caller: &BTreeMap<String, String>,
    env: &mut BTreeMap<String, String>,
    also: &[String],
) -> Vec<Ignored> {
    let owned = owned_names(caller, env, also);
    env.retain(|name, _| !name.starts_with(PERMIT_PREFIX));
    let got = permits(caller, &owned);
    env.extend(got.forwarded);
    got.ignored
}

#[cfg(test)]
#[path = "permit.test.rs"]
mod tests;
