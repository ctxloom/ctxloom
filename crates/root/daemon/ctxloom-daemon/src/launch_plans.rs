//! The daemon's launch plans, as its API makes them: each child planned by
//! its project's dry run and judged on the evidence the plan gives. Nothing
//! is kept: what the launch is given rides the events that record it.

use std::path::{Path, PathBuf};
use std::sync::Arc;

/// The daemon's launch plans, as its API makes them (`CreateHandOff`, S10):
/// each child planned by its project's dry run in the environment its
/// launch sent, judged on the evidence the plan gives (by name only). What a child may be given
/// of that environment is its engine's allowlist, its engine block's `env`,
/// its credential source's variables and the permits
/// (`engine_claude::launcher_environment`); where its launcher finds its
/// user's settings is the launch's [`crate::SETTINGS_PLACES`].
pub struct ApiLaunchPlans {
    home: PathBuf,
    temp: PathBuf,
    /// The engines a child is hosted on: their names and credentials.
    engines: Vec<daemon_application::HostedEngine>,
}

/// The daemon's launch plans, planning with `temp` and the
/// `engines` children are hosted on.
pub fn launch_plans(
    (home, temp): (&Path, &Path),
    engines: &[daemon_application::HostedEngine],
) -> Arc<dyn daemon_api::LaunchPlans> {
    Arc::new(ApiLaunchPlans {
        home: home.to_path_buf(),
        temp: temp.to_path_buf(),
        engines: engines.to_vec(),
    })
}

impl daemon_api::LaunchPlans for ApiLaunchPlans {
    fn plan(&self, request: &daemon_api::HandingOff) -> Result<daemon_api::Planned, String> {
        // Keyed as the platform compares names (uppercase on Windows), as a
        // run's environment is (`os::Flavor::env_key`).
        let flavor = os::host().flavor();
        let caller: std::collections::BTreeMap<String, String> = request
            .env
            .iter()
            .map(|(name, value)| (flavor.env_key(name), value.clone()))
            .collect();
        let previewed = crate::child_plan(
            &self.home,
            &self.temp,
            &request.engine,
            &request.agent,
            &request.root,
            &caller,
            request.trusted,
        )?;
        // None asked: the engine the plan resolved for the agent (its own,
        // else the config's default engine).
        let engine = match request.engine.is_empty() {
            true => previewed.plan.engine.clone(),
            false => request.engine.clone(),
        };
        let drivable: Vec<String> = self
            .engines
            .iter()
            .map(|engine| engine.name().to_string())
            .collect();
        let base = self
            .engines
            .iter()
            .find(|hosted| hosted.name() == engine)
            .map_or(run_ports::Credentials::NONE, |hosted| {
                hosted.engine.credentials()
            });
        let child = run_application::hosted_child(&previewed, &engine, &drivable, base, &caller);
        let credential = child.evidence.credential.clone().unwrap_or_default();
        Ok(daemon_api::Planned {
            engine,
            drivable_engines: child.evidence.drivable_engines.clone(),
            credential: runs::CredentialEvidence {
                file_only: credential.file_only,
                builtin_variables: credential.builtin_variables,
                accepted_variables: credential.accepted_variables,
                accepted_set: credential.accepted_set,
            },
            fatal_findings: child.evidence.fatal_findings,
            fatal: child
                .evidence
                .fatal
                .iter()
                .map(|finding| daemon_api::FatalFinding {
                    text: finding.text.clone(),
                    remedy: finding.remedy.clone(),
                })
                .collect(),
            untrusted_settings: child.evidence.untrusted_settings.clone(),
            content: daemon_api::HandOffContent {
                env: engine_claude::launcher_environment(&child.settings, &caller),
                launcher: crate::settings_places(&caller),
                agent: request.agent.clone(),
            },
        })
    }
}
