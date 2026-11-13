//! What the user daemon starts a hosted child with (D18, D-N45, D-N46): the
//! child's plan (its project's dry run, as a one-shot child's run plans it)
//! read for the engine's settings, the context, the model and the effort, and
//! the evidence the child's enqueue is judged on (rulings 53, 73e, 73f), made
//! exactly as a one-shot child's: the engine and the engines driven, the plan's
//! fatal findings, its posture, and the child's credential by the names of the
//! variables in the environment its launcher sent (S6). Names and yes-or-no
//! only leave here: no value of the environment is in the evidence.

use std::collections::BTreeMap;

use library_application::Previewed;
use run_ports::{Credentials, EngineSettings};
use runs_application::Evidence;

use crate::credential::credential_evidence;
use crate::one_shot::settings_of;

/// How a hosted child is started, and what its enqueue is judged on.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HostedChild {
    /// How the plan says the engine is run.
    pub settings: EngineSettings,
    /// The context the plan assembled for the child's project.
    pub context: String,
    /// The agent's model and effort; empty names none.
    pub model: String,
    pub effort: String,
    /// The agent's permissions document for the engine, JSON text as it
    /// was written (rulings 133-137); none: the engine's default document.
    pub permissions: Option<String>,
    /// What the run agent's selection carries into the launch (its
    /// commands, skills, hooks and servers), as every launch's plan does.
    pub surfaces: run_ports::LaunchSurfaces,
    pub evidence: Evidence,
}

/// The hosted child on `engine` that `previewed` plans, `drivable` the
/// engines the daemon hosts, `base` the engine's own credentials and `env`
/// the environment the child's launcher sent. A hosted child's prompts are
/// its messages (D-N25), so none is asked of the plan.
pub fn hosted_child(
    previewed: &Previewed,
    engine: &str,
    drivable: &[String],
    base: Credentials,
    env: &BTreeMap<String, String>,
) -> HostedChild {
    let plan = &previewed.plan;
    let settings = settings_of(plan, engine);
    let policy = settings.credentials(base);
    let evidence = Evidence {
        prompt_given: true,
        engine: engine.to_string(),
        drivable_engines: drivable.to_vec(),
        credential: Some(credential_evidence(
            engine,
            &settings.auth,
            &base,
            &policy,
            env,
        )),
        fatal_findings: u32::try_from(previewed.fatal.len()).unwrap_or(u32::MAX),
    };
    HostedChild {
        context: plan.context.clone(),
        model: plan.model.clone(),
        effort: plan.effort.clone(),
        permissions: plan.permissions.clone(),
        surfaces: run_ports::LaunchSurfaces(plan.surfaces.clone()),
        settings,
        evidence,
    }
}

#[cfg(test)]
#[path = "hosted.test.rs"]
mod tests;
