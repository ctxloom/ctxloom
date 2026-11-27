//! The runs the daemon hosts (U4, T7): `CreateRun`, `GetRun` and
//! `WatchRun`. A ONE_SHOT run is hosted by the daemon's one-shots
//! (`daemon_application::OneShots`: the run use case's one-shot in the
//! daemon, over its one store, in the environment the client sent, S7), and
//! answered once it is placed, or with its `end` when it ended before that;
//! its notices, reply, stop and end are then `WatchRun`'s. A TERMINAL run is
//! U5's (`terminal.rs`): prepared, answered with its launch, and watched.

use std::sync::Arc;

use daemon_application::{Created, HostProject, HostRequest, OneShots};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use run_ports::Edge;
use sessions::ProjectName;

use crate::server::{invalid, Api};
use daemon_api_contract::{ApiError, ApiRefusal};

/// A hosted run's events, as `WatchRun` streams them: each in order, waited
/// for, until its end (the API's edge runs it on a blocking thread).
pub type RunStream = Box<dyn Iterator<Item = v1::RunEvent> + Send>;

/// The run's resource name a hosted run is known by, under the session's
/// project: `projects/{project}/runs/{run}`.
fn run_name(session: &str, run: &str) -> String {
    let project = session.split("/sessions/").next().unwrap_or_default();
    format!("{project}/runs/{run}")
}

/// The run's root a run's resource name ends with.
fn run_of(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or_default()
}

/// The engine's resource name under `parent` (its word, outside a
/// project), as a dry run's `Run` names it.
pub(crate) fn engine_name(parent: &str, engine: &str) -> String {
    match parent.starts_with("projects/") {
        true => format!("{parent}/engines/{engine}"),
        false => engine.to_string(),
    }
}

/// The `Run` a ONE_SHOT `request` is answered with, as it was `created`:
/// placed (to watch), or ended before it was, with its notices and end.
pub(crate) fn run_created(request: &v1::CreateRunRequest, created: Created) -> v1::Run {
    let run = v1::Run {
        mode: v1::run::Mode::OneShot as i32,
        agent: request.agent.clone(),
        prompt: request.prompt.clone(),
        ..v1::Run::default()
    };
    match created {
        Created::Placed(placed) => v1::Run {
            name: run_name(&placed.name, &placed.run),
            engine: engine_name(&request.parent, &placed.engine),
            session: placed.name,
            ..run
        },
        Created::Ended(events) => {
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
    }
}

/// A call this daemon does not serve yet.
fn unimplemented(method: &str) -> ApiError {
    ApiError::Unimplemented {
        method: method.to_string(),
    }
}

fn not_found(name: &str) -> ApiError {
    ApiRefusal::of(&v1::RunNotFound {
        name: name.to_string(),
    })
    .into()
}

impl<E: Edge> Api<E> {
    /// `CreateRun`, by its mode: a ONE_SHOT hosted; a TERMINAL run is U5's
    /// (its arm below is where it is built); a preview (`validate_only`) is
    /// the dry run's, planned here as a library read (step 10a U2).
    pub async fn create_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, ApiError> {
        if request.validate_only {
            return self.preview_run(request).await;
        }
        match v1::run::Mode::try_from(request.mode).unwrap_or(v1::run::Mode::Unspecified) {
            v1::run::Mode::OneShot => self.create_one_shot(request).await,
            // U5: a TERMINAL run, planned and prepared, its `Launch` for the
            // frontend to start (daemon-api's terminal.rs).
            v1::run::Mode::Terminal => self.create_terminal(request).await,
            v1::run::Mode::Unspecified => Err(unimplemented("CreateRun(MODE_UNSPECIFIED)")),
        }
    }

    /// The daemon's one-shots, or the failure of a daemon that hosts none.
    pub(crate) fn one_shots(&self) -> Result<&Arc<OneShots>, ApiError> {
        self.one_shots.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon hosts no one-shot".to_string(),
        })
    }

    /// A ONE_SHOT run hosted: its project the one its parent names (none
    /// under `users/me`), in the client's working directory and
    /// environment. Answered once placed, or with its end.
    async fn create_one_shot(&self, request: v1::CreateRunRequest) -> Result<v1::Run, ApiError> {
        let one_shots = Arc::clone(self.one_shots()?);
        let host = self.one_shot_request(&request).await?;
        Ok(run_created(&request, one_shots.create(host)))
    }

    /// What hosting the ONE_SHOT `request` asks of the daemon's one-shots:
    /// its project (none under `users/me`), the client's working directory
    /// and environment, and the project's trust, which is the daemon's to
    /// read, never the client's to say (step 8 U2).
    pub(crate) async fn one_shot_request(
        &self,
        request: &v1::CreateRunRequest,
    ) -> Result<HostRequest, ApiError> {
        let project = self.host_project(&request.parent).await?;
        // Planned outside the daemon's reads: its trees observed first.
        self.observe_for(request).await?;
        let (one_shot, mut flags) = run_application::client::asked_of(request);
        flags.trusted = project.as_ref().is_some_and(|p| self.trusts(&p.id));
        let env = request
            .caller_env()
            .cloned()
            .unwrap_or_default()
            .vars
            .into_iter()
            .map(|var| (var.name, var.value))
            .collect();
        Ok(HostRequest {
            project,
            cwd: request.caller_cwd().to_string(),
            env,
            program: crate::program_of(request.caller_client()),
            flags,
            request: one_shot,
            launcher_lock: None,
        })
    }

    /// The project `parent` names, as the warm index holds it (read fresh:
    /// the client's `ResolveProject` just registered it); none under
    /// `users/me`. A project nobody named is `ProjectNotFound`.
    pub(crate) async fn host_project(&self, parent: &str) -> Result<Option<HostProject>, ApiError> {
        if !parent.starts_with("projects/") {
            return Ok(None);
        }
        let name = ProjectName::parse(parent).map_err(|rejection| invalid(parent, &rejection))?;
        self.parts.hosting.fresh().await;
        let row = self.parts.index.index().project_named_by(name.harp());
        let row = row.ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                name: parent.to_string(),
            }))
        })?;
        Ok(Some(HostProject {
            id: row.project_id,
            harp: name.harp().to_string(),
            root: row.root,
        }))
    }

    /// `GetRun`: a run the daemon hosts (or keeps, ended), by its name.
    pub async fn get_run(&self, request: v1::GetRunRequest) -> Result<v1::Run, ApiError> {
        if let Some(run) = self.get_terminal(&request.name) {
            return Ok(run);
        }
        let hosted = self
            .one_shots()?
            .get(run_of(&request.name))
            .ok_or_else(|| not_found(&request.name))?;
        Ok(v1::Run {
            name: run_name(&hosted.placement.name, &hosted.placement.run),
            mode: v1::run::Mode::OneShot as i32,
            session: hosted.placement.name.clone(),
            engine: hosted.placement.engine,
            ..v1::Run::default()
        })
    }

    /// `WatchRun`: a hosted run's events from its first, until its end.
    pub async fn watch_run(&self, request: v1::WatchRunRequest) -> Result<RunStream, ApiError> {
        if let Some(watch) = self.watch_terminal(&request.name) {
            return Ok(watch);
        }
        let watch = self
            .one_shots()?
            .watch(run_of(&request.name))
            .ok_or_else(|| not_found(&request.name))?;
        Ok(Box::new(watch))
    }
}

#[cfg(test)]
#[path = "runs.test.rs"]
mod tests;
