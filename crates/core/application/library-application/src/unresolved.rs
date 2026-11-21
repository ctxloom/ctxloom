//! Whether every configured agent resolves (doctor's `AGENTS-b2`, smoke
//! 2026-11-19): each agent planned as `ctxloom run --agent <name>` would
//! plan it, and what would refuse that run named: a fatal finding of the
//! plan (a bundle its profiles include that is not there), or the plan's
//! own refusal (a profile nobody has).

use crate::project::{Library, Outcome};
use crate::run::RunRequest;
use crate::Flags;

/// An agent whose run would be refused, and why.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentUnresolved {
    pub agent: String,
    /// What would refuse its run, each in the plan's words.
    pub problems: Vec<String>,
    /// The first problem's remedy; empty for none.
    pub fix: String,
}

impl Library {
    /// Every agent of the project whose run would be refused as planned
    /// (without `--degraded`), with why; an agent that resolves is not named.
    pub fn agents_unresolved(&self, flags: &Flags) -> Outcome<Vec<AgentUnresolved>> {
        let flags = Flags {
            degraded: false,
            ..flags.clone()
        };
        self.run(&flags, |p| {
            let mut found = Vec::new();
            for agent in p.config.agents() {
                let request = RunRequest {
                    agent: agent.name.clone(),
                    ..RunRequest::default()
                };
                let (problems, fix) = match p.dry_run(&request) {
                    Ok(planned) => (
                        planned.fatal.iter().map(|f| f.text.clone()).collect(),
                        planned
                            .fatal
                            .first()
                            .map(|f| f.remedy.clone())
                            .unwrap_or_default(),
                    ),
                    Err(error) => (vec![error.to_string()], String::new()),
                };
                if !problems.is_empty() {
                    found.push(AgentUnresolved {
                        agent: agent.name,
                        problems,
                        fix,
                    });
                }
            }
            Ok(found)
        })
    }
}

#[cfg(test)]
#[path = "unresolved.test.rs"]
mod tests;
