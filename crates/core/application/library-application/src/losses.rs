//! What an engine cannot carry of what an agent's profiles and the
//! registered companions configure: the
//! capability-loss report `agent show`, `agent edit --engine`'s swap preview
//! and doctor give.
//!
//! The engines' word on it is data the composition root hands the library
//! ([`Library::with_hook_losses`]): each engine's unified hook events it has
//! no native form for, with the reason (an engine's surface half,
//! `surface_ports::EngineSurfaces::hook_losses`). A profile's hook on such an
//! event is lost on that engine, and so is a registered companion's, for every
//! agent (OD-22); ctxloom's own callbacks are not the profile's, so an engine
//! that cannot fire one is not a loss the team made.

use std::collections::{BTreeMap, BTreeSet};

use library::config::engines::DEFAULT_ENGINE;
use serde::Serialize;

use crate::project::{Library, Outcome, Project};
use crate::Flags;

/// Each engine's hook events it cannot carry, by engine name: `(unified
/// event, reason)`, in event order.
pub type HookLosses = BTreeMap<String, Vec<(String, String)>>;

/// One thing an agent's profiles configure that its engine cannot carry.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct NotCarried {
    /// The engine that cannot carry it.
    pub engine: String,
    /// The kind of surface: `hooks`.
    pub surface: String,
    /// The unified hook event.
    pub event: String,
    /// Why the engine cannot.
    pub reason: String,
}

impl NotCarried {
    /// The one line a text report gives it.
    pub fn line(&self) -> String {
        format!("{}: {} ({})", self.surface, self.event, self.reason)
    }
}

/// An agent's losses on its own engine (doctor's rows).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentLosses {
    pub agent: String,
    pub engine: String,
    pub not_carried: Vec<NotCarried>,
}

impl Library {
    /// The library told what each engine cannot carry (the composition root's
    /// engines' word, data and nothing else).
    pub fn with_hook_losses(mut self, losses: HookLosses) -> Library {
        self.hook_losses = losses;
        self
    }

    /// Every agent of the project whose engine cannot carry something its
    /// profiles configure, with what (doctor's capability-loss check).
    pub fn agents_losses(&self, flags: &Flags) -> Outcome<Vec<AgentLosses>> {
        self.run(flags, |p| {
            let mut found = Vec::new();
            for agent in p.config.agents() {
                let engine = engine_or_default(&agent.engine);
                let not_carried = p.losses_on(&agent.profiles, &engine);
                if !not_carried.is_empty() {
                    found.push(AgentLosses {
                        agent: agent.name,
                        engine,
                        not_carried,
                    });
                }
            }
            Ok(found)
        })
    }
}

/// The engine a binding's empty engine is.
pub(crate) fn engine_or_default(engine: &str) -> String {
    if engine.is_empty() {
        DEFAULT_ENGINE.to_string()
    } else {
        engine.to_string()
    }
}

impl Project {
    /// What `engine` cannot carry of what the `profiles` and the registered
    /// companions configure, one entry per event they hook that the engine
    /// has no form for. A companion's hooks ride every agent's launch, so they
    /// count for an agent with no profiles too (OD-22). Profiles that do not
    /// resolve configure nothing here (their refusal is the launch's and the
    /// write's own).
    pub(crate) fn losses_on(&mut self, profiles: &[String], engine: &str) -> Vec<NotCarried> {
        let Some(lost) = self.hook_losses.get(engine).cloned() else {
            return Vec::new();
        };
        if lost.is_empty() {
            return Vec::new();
        }
        let events: BTreeSet<String> = self
            .profile_hooks(profiles)
            .into_iter()
            .filter(|h| h.source_kind != "context")
            .map(|h| h.event)
            .collect();
        lost.into_iter()
            .filter(|(event, _)| events.contains(event))
            .map(|(event, reason)| NotCarried {
                engine: engine.to_string(),
                surface: "hooks".to_string(),
                event,
                reason,
            })
            .collect()
    }

    /// What `to` cannot carry of the profiles that `from` could: the swap's
    /// losses.
    pub(crate) fn swap_losses(
        &mut self,
        profiles: &[String],
        from: &str,
        to: &str,
    ) -> Vec<NotCarried> {
        let before: BTreeSet<String> = self
            .losses_on(profiles, from)
            .into_iter()
            .map(|l| l.event)
            .collect();
        self.losses_on(profiles, to)
            .into_iter()
            .filter(|l| !before.contains(&l.event))
            .collect()
    }
}

#[cfg(test)]
#[path = "losses.test.rs"]
mod tests;
