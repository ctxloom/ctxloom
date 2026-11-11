//! `agent list|show`, `companion list` and `mcp server list|show`.

use std::collections::{BTreeMap, BTreeSet};

use angzarr_diagnostic::Diagnostic;
use angzarr_yaml::Value;
use library::config::engines::{self, AuthError, Effective, EngineError};
use library::model::McpServer;
use library::profile::ResolvedProfile;

use crate::config::{mcp_row, Agent};
use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::views::{
    AgentRow, AgentShow, CompanionRow, McpList, McpListRow, McpRow, McpShow, Requirement,
    RequirementSource, ResolvedAgent,
};
use crate::Flags;

/// The engines this build knows by name.
pub const ENGINES: &[&str] = library::config::schema::ENGINES;

/// The runtimes an engine can be given.
pub(crate) const RUNTIMES: [&str; 3] = ["host", "container-rootless", "container-rootful"];

/// Commands the container image stages itself (and so are not required of
/// the host).
const STAGED: [&str; 4] = ["ctxloom", "ltk", "taskloom", "reprise"];

impl Library {
    /// `agent list`.
    pub fn agent_list(&self, flags: &Flags) -> Outcome<Vec<AgentRow>> {
        self.run(flags, |p| {
            Ok(p.config.agents().iter().map(Agent::row).collect())
        })
    }

    /// `agent show <name>`; `say` words a diagnostic.
    pub fn agent_show(
        &self,
        flags: &Flags,
        name: &str,
        say: &dyn Fn(&dyn Diagnostic) -> String,
    ) -> Outcome<AgentShow> {
        self.run(flags, |p| p.agent_show(name, say))
    }

    /// The engine a bare command means: the default agent's, else the
    /// engine the project's agents are bound to first, else the default.
    pub fn default_engine(&self, flags: &Flags) -> Outcome<String> {
        self.run(flags, |p| {
            let default = p.config.agent(&p.config.default_agent());
            match default {
                Some(agent) => Ok(p.effective_of(&agent)?.engine.value),
                None => Ok(p.config.configured_engines().remove(0)),
            }
        })
    }

    /// `companion list`.
    pub fn companion_list(&self, flags: &Flags) -> Outcome<Vec<CompanionRow>> {
        self.run(flags, |p| Ok(p.companion_list()))
    }

    /// `mcp server list`.
    pub fn mcp_list(&self, flags: &Flags) -> Outcome<McpList> {
        self.run(flags, |p| Ok(p.mcp_list()))
    }

    /// `mcp server show <name>`.
    pub fn mcp_show(&self, flags: &Flags, name: &str) -> Outcome<McpShow> {
        self.run(flags, |p| p.mcp_show(name))
    }
}

/// A server a bundle ships and where from.
pub(crate) struct Served {
    pub server: McpServer,
    /// The canonical reference of the shipping bundle.
    pub source: String,
}

impl Project {
    /// The registered companions, each with whether its program exists. Only
    /// that is checked: a companion is never run to list it.
    fn companion_list(&self) -> Vec<CompanionRow> {
        self.config
            .companions()
            .into_iter()
            .map(|companion| CompanionRow {
                exists: companion
                    .command
                    .first()
                    .is_some_and(|program| self.binaries.find(program).is_some()),
                name: companion.name,
                command: companion.command,
            })
            .collect()
    }

    /// The servers the profiles' bundles ship, a name claimed by two
    /// different bundles withheld from the second; those the profiles
    /// exclude left out. A bundle that cannot be read is a finding.
    pub(crate) fn served(
        &self,
        set: &[ResolvedProfile],
        findings: &mut Vec<library::findings::Finding>,
    ) -> BTreeMap<String, Served> {
        self.served_from(&self.catalog, set, findings)
    }

    /// As [`Self::served`], from `catalog`: the companions it holds claim
    /// their servers first (Go's `ResolveBundleMCPServersFor`), so a profile
    /// bundle declaring a name a companion holds is the contest's loser.
    pub(crate) fn served_from(
        &self,
        catalog: &library::catalog::Catalog,
        set: &[ResolvedProfile],
        findings: &mut Vec<library::findings::Finding>,
    ) -> BTreeMap<String, Served> {
        let excluded: BTreeSet<&str> = set
            .iter()
            .flat_map(|p| p.exclude_mcp.iter().map(String::as_str))
            .collect();
        let mut out: BTreeMap<String, Served> = BTreeMap::new();
        let mut note = |finding: library::findings::Finding| {
            if !findings.contains(&finding) {
                findings.push(finding);
            }
        };
        let companions = catalog.companion_entries();
        let profile_bundles = set
            .iter()
            .flat_map(|p| &p.bundles)
            .map(|reference| (reference.as_str(), catalog.lookup(reference)));
        let companions = companions
            .into_iter()
            .map(|(_, entry)| (entry.name.as_str(), Ok(entry)));
        for (reference, entry) in companions.chain(profile_bundles) {
            let Ok(entry) = entry else {
                note(library::findings::Finding::fail(
                    "bundle",
                    format!(
                        "failed to load bundle \"{reference}\"; the MCP servers and hooks it ships are not applied: bundle not found: {reference}"
                    ),
                    "run `ctxloom deps pull` or fix the bundle ref, or pass --degraded",
                ).about(reference));
                continue;
            };
            let source = catalog.source_of(entry);
            for (name, server) in &entry.bundle.mcp {
                if excluded.contains(name.as_str()) {
                    continue;
                }
                match out.get(name) {
                    Some(held) if held.source != source => {
                        note(contest(name, &held.source, &source))
                    }
                    Some(_) => {}
                    None => {
                        out.insert(
                            name.clone(),
                            Served {
                                server: server.clone(),
                                source: source.clone(),
                            },
                        );
                    }
                }
            }
        }
        out
    }

    /// The servers of the default agent's profiles, as the project registers them.
    pub(crate) fn registered(&self) -> BTreeMap<String, Served> {
        let mut findings = Vec::new();
        let names = self.config.default_agent_profiles();
        let set = self
            .resolve_profiles(&names, &mut findings)
            .unwrap_or_default();
        self.served(&set, &mut findings)
    }

    fn mcp_list(&self) -> McpList {
        let mut servers: Vec<McpListRow> = self
            .registered()
            .into_iter()
            .map(|(name, s)| McpListRow {
                name,
                command: s.server.command,
                args: s.server.args,
                url: s.server.url,
                served_by: s.server.served_by,
                source: s.source,
            })
            .collect();
        servers.sort_by_key(|row| row.name.to_lowercase());
        McpList { servers }
    }

    fn mcp_show(&self, name: &str) -> Result<McpShow, AppError> {
        let registered = self.registered();
        let Some(found) = registered.get(name) else {
            return Err(AppError::McpNotFound {
                name: name.to_string(),
            });
        };
        let entries: Vec<McpRow> = vec![mcp_row(name, &found.server, &found.source)];
        Ok(McpShow {
            name: name.to_string(),
            found: true,
            entries,
        })
    }

    /// What `agent` launches with: its engine, model, effort, permissions,
    /// runtime, env and the engine block's shared settings, each with the
    /// layer that said it.
    pub(crate) fn effective_of(&self, agent: &Agent) -> Result<Effective, AppError> {
        engines::effective(&self.config.value, &agent.name).map_err(engine_error)
    }

    fn agent_show(
        &mut self,
        name: &str,
        say: &dyn Fn(&dyn Diagnostic) -> String,
    ) -> Result<AgentShow, AppError> {
        let Some(agent) = self.config.agent(name) else {
            return Err(AppError::AgentNotFound {
                name: name.to_string(),
            });
        };
        let definition = agent.row();
        match self.resolve_agent(&agent) {
            Ok((resolved, requirements, requirements_error)) => Ok(AgentShow {
                definition,
                resolved: Some(resolved),
                resolution_error: String::new(),
                requirements,
                requirements_error,
            }),
            Err(error) => Ok(AgentShow {
                definition,
                resolved: None,
                resolution_error: format!("agent {name:?}: {}", why(&error, say)),
                requirements: Vec::new(),
                requirements_error: String::new(),
            }),
        }
    }

    /// The engine and context an agent resolves to, and what it requires.
    fn resolve_agent(
        &mut self,
        agent: &Agent,
    ) -> Result<(ResolvedAgent, Vec<Requirement>, String), AppError> {
        let mut findings = Vec::new();
        let names = if agent.profiles.is_empty() {
            self.config.default_agent_profiles()
        } else {
            agent.profiles.clone()
        };
        let set = self.resolve_profiles(&names, &mut findings)?;
        let assembled = self.assemble_set(&set, &[], &[], &mut findings);
        let effective = self.effective_of(agent)?;
        let approver = approver_of(&agent.permissions, &self.config.value);
        let resolved = ResolvedAgent {
            name: agent.name.clone(),
            profiles: agent.profiles.clone(),
            engine: effective.engine.value.clone(),
            settings: crate::engine_noun::settings_of(&effective),
            context: assembled.context.clone(),
            fragments: assembled.loaded.clone(),
            effective_permissions: effective.permissions.value.clone(),
            approver,
            driving: agent.driving.clone(),
            session_home: effective.session_home.value.clone(),
            command: effective.command.value.clone(),
        };
        let (requirements, requirements_error) = self.requirements(&set);
        Ok((resolved, requirements, requirements_error))
    }

    /// The commands an agent needs on its PATH and who asks for each.
    fn requirements(&self, set: &[ResolvedProfile]) -> (Vec<Requirement>, String) {
        let mut found: BTreeMap<String, BTreeSet<(String, String)>> = BTreeMap::new();
        for profile in set {
            for reference in &profile.bundles {
                match self.catalog.lookup(reference) {
                    Ok(entry) => {
                        for command in &entry.bundle.requires_commands {
                            found
                                .entry(command.clone())
                                .or_default()
                                .insert(("bundle".to_string(), reference.clone()));
                        }
                    }
                    Err(_) => {
                        return (
                            Vec::new(),
                            format!(
                                "reading bundle {reference} for its requirements: bundle not found: {reference}"
                            ),
                        )
                    }
                }
            }
        }
        for (name, served) in self.served(set, &mut Vec::new()) {
            if !served.server.url.is_empty() || !served.server.served_by.is_empty() {
                continue;
            }
            let Some(first) = served.server.command.split_whitespace().next() else {
                continue;
            };
            if STAGED.contains(&first) {
                continue;
            }
            found
                .entry(first.to_string())
                .or_default()
                .insert(("mcp".to_string(), name));
        }
        let requirements = found
            .into_iter()
            .map(|(command, sources)| Requirement {
                command,
                sources: sources
                    .into_iter()
                    .map(|(kind, reference)| RequirementSource { kind, reference })
                    .collect(),
            })
            .collect();
        (requirements, String::new())
    }
}

/// Who answers what the posture leaves open in a headless run: the binding's
/// declaration, then the project's, else the human.
fn approver_of(binding: &Value, config: &Value) -> String {
    let declared = |block: Option<&Value>| -> Option<String> {
        block
            .and_then(|b| b.get("approver"))
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
            .map(ToString::to_string)
    };
    declared(Some(binding))
        .or_else(|| declared(config.get("permissions")))
        .unwrap_or_else(|| "human".to_string())
}

/// Why an agent did not resolve, in the words Go gives.
fn why(error: &AppError, say: &dyn Fn(&dyn Diagnostic) -> String) -> String {
    match error {
        AppError::Compose { profile, cause } => {
            format!(
                "compose profiles: profile {profile}: {}",
                say(cause.as_ref())
            )
        }
        other => say(other),
    }
}

/// A server name two different bundles declare: the second is withheld.
fn contest(name: &str, first: &str, second: &str) -> library::findings::Finding {
    library::findings::Finding::fail(
        "bundle",
        format!(
            "MCP server name {name:?} is claimed by two different sources: {first:?} claimed it first and {second:?} also declares it; {second:?}'s server is withheld so one bundle's server cannot silently answer for another's declared name"
        ),
        "rename the server in one of the two bundles, or exclude it with `exclude_mcp:` in your profile",
    )
}

/// An agent's settings that cannot be resolved, as the refusal that says why.
pub(crate) fn engine_error(error: EngineError) -> AppError {
    match error {
        EngineError::AgentNotFound { name } => AppError::AgentNotFound { name },
        EngineError::UnknownEngine { engine, known } => AppError::UnknownEngine {
            engine,
            known: known.join(", "),
        },
        EngineError::Auth { engine, why } => AppError::EngineAuth {
            engine,
            why: match why {
                AuthError::Shape => "must be none, login, token, or a mapping of env and/or file",
                AuthError::UnknownKey { .. } => "takes only env and file",
                AuthError::Empty => "needs env (variable names) or file",
                AuthError::NotAName { .. } => "env holds an entry that is not a variable name",
                AuthError::EmptyFile => "file must not be empty",
            }
            .to_string(),
        },
    }
}
