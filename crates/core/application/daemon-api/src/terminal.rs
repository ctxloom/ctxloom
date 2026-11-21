//! The TERMINAL runs (U5: T9, S3, S20): `CreateRun{mode: TERMINAL}` is
//! answered with the session's one `Launch` once the daemon has planned,
//! enqueued, minted and set it up (`daemon_application::Terminals`), and
//! `WatchRun` of it streams what its frontend must hear: a `stop` (the
//! session closed, S20: ask the TUI to stop, kill it after the grace), what
//! the run noticed, and its end once the frontend reported the TUI's exit.
//!
//! The launch carries the user's environment (her credentials among it): it
//! crosses the wire to the client that asked, in this answer alone, and is
//! never logged or kept past the run.

use std::sync::Arc;

use daemon_application::{TerminalCreated, Terminals};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use run_ports::Edge;

use crate::refusal::ApiError;
use crate::server::Api;
use crate::RunStream;

/// The run's resource name under the session's project:
/// `projects/{project}/runs/{run}`.
fn run_name(session: &str, run: &str) -> String {
    let project = session.split("/sessions/").next().unwrap_or_default();
    format!("{project}/runs/{run}")
}

/// The run's id a run's resource name ends with.
fn run_of(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or_default()
}

impl<E: Edge> Api<E> {
    /// The daemon's terminal runs, or the failure of a daemon that prepares
    /// none.
    fn terminals(&self) -> Result<&Arc<Terminals>, ApiError> {
        self.terminals.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon prepares no interactive session".to_string(),
        })
    }

    /// `CreateRun{mode: TERMINAL}`: the session planned and prepared in its
    /// client's directory and environment, answered with its launch; one
    /// that ended before its launch was ready answers its end (its name
    /// `<parent>/runs/-`, nothing to watch).
    pub async fn create_terminal(
        &self,
        request: v1::CreateRunRequest,
    ) -> Result<v1::Run, ApiError> {
        let terminals = Arc::clone(self.terminals()?);
        let project = self.host_project(&request.parent).await?;
        // Planned outside the daemon's reads: its trees observed first.
        self.observe_for(&request).await?;
        let (asked, mut flags) = run_application::client::asked_of(&request);
        // The project's trust is the daemon's to read, never the client's
        // to say (step 8 U2).
        flags.trusted = project.as_ref().is_some_and(|p| self.trusts(&p.id));
        let env = request
            .caller_env()
            .cloned()
            .unwrap_or_default()
            .vars
            .into_iter()
            .map(|var| (var.name, var.value))
            .collect();
        let created = terminals.create(daemon_application::HostRequest {
            project,
            cwd: request.caller_cwd().to_string(),
            env,
            program: crate::program_of(request.caller_client()),
            flags,
            request: asked,
            launcher_lock: Some(request.launcher_lock.clone()).filter(|lock| !lock.is_empty()),
        });
        let run = v1::Run {
            mode: v1::run::Mode::Terminal as i32,
            agent: request.agent.clone(),
            prompt: request.prompt.clone(),
            ..v1::Run::default()
        };
        Ok(match created {
            TerminalCreated::Prepared(placed, launch) => v1::Run {
                name: run_name(&placed.name, &placed.run),
                engine: crate::runs::engine_name(&request.parent, &placed.engine),
                session: placed.name,
                launch: Some(crate::launch::to_wire(&launch)),
                ..run
            },
            TerminalCreated::Ended(events) => {
                let mut ended = v1::Run {
                    name: format!("{}/runs/-", request.parent),
                    ..run
                };
                for event in events {
                    match event.event {
                        Some(v1::run_event::Event::Notice(notice)) => ended.notices.push(notice),
                        Some(v1::run_event::Event::End(end)) => ended.end = Some(end),
                        _ => {}
                    }
                }
                ended
            }
        })
    }

    /// The terminal run `name` names, as `GetRun` answers it; none when the
    /// daemon prepared no such run (or prepares none).
    pub fn get_terminal(&self, name: &str) -> Option<v1::Run> {
        let placed = self.terminals.as_ref()?.get(run_of(name))?;
        Some(v1::Run {
            name: run_name(&placed.name, &placed.run),
            mode: v1::run::Mode::Terminal as i32,
            session: placed.name,
            engine: placed.engine,
            ..v1::Run::default()
        })
    }

    /// `WatchRun` of a terminal run: its events from its first, until its
    /// end; none when the daemon prepared no such run (or prepares none).
    pub fn watch_terminal(&self, name: &str) -> Option<RunStream> {
        let watch = self.terminals.as_ref()?.watch(run_of(name))?;
        Some(Box::new(watch))
    }
}

#[cfg(test)]
#[path = "terminal.test.rs"]
mod tests;
