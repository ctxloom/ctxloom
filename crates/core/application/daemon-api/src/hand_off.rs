//! A child's launch, checked and planned (T6, S10; daemon slice 4 U3):
//! `CreateHandOff`, served by the daemon.
//!
//! A launch hands the daemon the environment its child is launched with
//! BEFORE the owner's `LaunchChild` is recorded. The daemon checks, in
//! order, and keeps NOTHING, so a refused launch leaves nothing:
//!
//! 1. the owner: a session its warm index holds in the project, else the
//!    sessions domain's `SessionUnknown` ("there is no session named ...");
//! 2. the project the child works in: the owner's, or the one the launch
//!    names (step 8 Q1), which must be registered and, when it is another
//!    project, trusted (`sessions-untrusted-target`, D8);
//! 3. the child's plan: its project's dry run on its engine (none asked: its
//!    agent's configured one, the config's default engine for an agent
//!    naming none, which the answer names) and agent, in the environment
//!    the launch sent ([`LaunchPlans::plan`], the root's);
//! 4. its credential and its project's untrusted settings, as the child's
//!    enqueue will judge them: the runs
//!    aggregate's own pre-launch checks (`runs::pre_launch_refusal`, rulings
//!    53, 73e), refused with the typed refusal the enqueue would answer
//!    (`runs-no-credential`, `runs-file-only-credential`, ...).
//!
//! Then it answers the launch's inputs back, the secrets among them sealed to
//! its own key (owner ruling 2026-11-25: the inputs of a launch ride the
//! event that records it): the caller puts them on the owner's `LaunchChild`,
//! which records them, and every start of the child reads them from the
//! stream. No file, no table of the daemon's memory.

use std::collections::BTreeMap;

use daemon_proto::v1;
use daemon_proto::Carries as _;
use sessions::{ProjectName, SessionName};

use crate::server::{invalid, Api};
use daemon_api_contract::{ApiError, ApiRefusal};

/// What a hand-off is made for: the child of a project.
#[derive(Clone, PartialEq, Eq)]
pub struct HandingOff {
    /// The registered root of the project the child works in (its owner's,
    /// or the one its launch named, step 8 Q1).
    pub root: String,
    /// Whether the user trusts that project (step 8 U2): only then do its
    /// own project-only settings apply to the child's plan.
    pub trusted: bool,
    /// The child's stable id.
    pub child: String,
    pub engine: String,
    /// The agent it is planned for; empty, the project's default.
    pub agent: String,
    /// The launch's environment (S7), whole.
    pub env: BTreeMap<String, String>,
}

/// What the child may be given: the environment it may be given and where
/// its launcher finds its user's settings.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct HandOffContent {
    pub env: BTreeMap<String, String>,
    pub launcher: BTreeMap<String, String>,
    pub agent: String,
}

impl std::fmt::Debug for HandingOff {
    /// The environment by name alone: a value may be a credential.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("HandingOff")
            .field("root", &self.root)
            .field("trusted", &self.trusted)
            .field("child", &self.child)
            .field("engine", &self.engine)
            .field("agent", &self.agent)
            .field("env", &run_ports::RedactedEnv(&self.env))
            .finish()
    }
}

impl std::fmt::Debug for HandOffContent {
    /// Both maps by name alone: a value may be a credential.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("HandOffContent")
            .field("env", &run_ports::RedactedEnv(&self.env))
            .field("launcher", &run_ports::RedactedEnv(&self.launcher))
            .field("agent", &self.agent)
            .finish()
    }
}

/// One fatal finding of the child's plan, as the library said it: its text
/// and its remedy (empty: none said). A launch refused for it says it, for
/// nothing else will.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FatalFinding {
    pub text: String,
    pub remedy: String,
}

/// The plan's fatal findings as the enqueue command carries them.
fn wire_findings(findings: &[FatalFinding]) -> Vec<runs::FatalFinding> {
    findings
        .iter()
        .map(|finding| runs::FatalFinding {
            text: finding.text.clone(),
            remedy: finding.remedy.clone(),
        })
        .collect()
}

/// What the child's plan says, as its start's enqueue is judged on it.
#[derive(Clone, Debug, PartialEq)]
pub struct Planned {
    /// The engine it is planned on: the one asked, else its agent's.
    pub engine: String,
    /// Every engine the daemon hosts a child on.
    pub drivable_engines: Vec<String>,
    /// Its credential evidence (ruling 53), by name only.
    pub credential: runs::CredentialEvidence,
    /// How many fatal findings its plan has.
    pub fatal_findings: u32,
    /// Those findings themselves, in the plan's order.
    pub fatal: Vec<FatalFinding>,
    /// The untrusted project-only settings its launch reads (step 8 U2).
    pub untrusted_settings: Vec<String>,
    /// What it is to be handed.
    pub content: HandOffContent,
}

/// The child's plan, outside the core: the root's.
pub trait LaunchPlans: Send + Sync {
    /// The child's plan (its project's dry run); the plan's error code where
    /// it cannot be made.
    fn plan(&self, request: &HandingOff) -> Result<Planned, String>;
}

/// The project a child works in: the one its launch named (`project`),
/// registered, else its owner's (`own`, `own_row`; step 8, owner Q1).
fn target_of(
    project: &str,
    own: &ProjectName,
    own_row: &session_index::ProjectRow,
    index: &session_index::SessionIndex,
) -> Result<(ProjectName, session_index::ProjectRow), ApiError> {
    if project.is_empty() {
        return Ok((own.clone(), own_row.clone()));
    }
    let target = ProjectName::parse(project).map_err(|rejection| invalid(project, &rejection))?;
    let row = index.project_named_by(target.harp()).ok_or_else(|| {
        ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
            name: project.to_string(),
        }))
    })?;
    Ok((target, row))
}

/// The collection a hand-off's name has under its project.
const HAND_OFFS: &str = "/handOffs/";

/// The child a hand-off id names, hyphenated; refused `NameInvalid` when
/// it is no UUID.
fn child_id(id: &str) -> Result<String, ApiError> {
    uuid::Uuid::parse_str(id)
        .map(|child| child.hyphenated().to_string())
        .map_err(|_| {
            ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: id.to_string(),
                reason: "shape".to_string(),
            }))
        })
}

/// The inputs of a launch, back to the caller for the owner's `LaunchChild`:
/// each value of the environment sealed to the target project's key and bound to its
/// variable; where her settings are is no secret.
fn answered(
    content: &HandOffContent,
    (sealing, project): (&dyn run_ports::Sealing, &str),
) -> Result<(Vec<v1::HandOffValue>, Vec<v1::HandOffValue>), ApiError> {
    let env = content
        .env
        .iter()
        .map(|(name, value)| {
            sealing
                .seal(
                    project,
                    &daemon_application::child_env_field(name),
                    value.as_bytes(),
                )
                .map(|sealed| v1::HandOffValue {
                    name: name.clone(),
                    sealed,
                    plain: String::new(),
                })
                .map_err(ApiError::failed)
        })
        .collect::<Result<Vec<_>, _>>()?;
    let launcher = content
        .launcher
        .iter()
        .map(|(name, value)| v1::HandOffValue {
            name: name.clone(),
            sealed: Vec::new(),
            plain: value.clone(),
        })
        .collect();
    Ok((env, launcher))
}

fn failed(detail: String) -> ApiError {
    ApiError::Failed { detail }
}

impl<E: run_ports::Edge> Api<E> {
    /// `CreateHandOff`: the owner checked, the child planned and judged,
    /// then its hand-off written; refused, nothing is written.
    pub async fn create_hand_off(
        &self,
        request: v1::CreateHandOffRequest,
    ) -> Result<v1::HandOff, ApiError> {
        let project = ProjectName::parse(&request.parent)
            .map_err(|rejection| invalid(&request.parent, &rejection))?;
        let child = child_id(&request.hand_off_id)?;
        let plans = self
            .launch_plans
            .as_ref()
            .ok_or_else(|| failed("this daemon plans no launch".to_string()))?;
        let sealing = self
            .sealing
            .as_ref()
            .ok_or_else(|| failed("this daemon seals nothing".to_string()))?;
        self.parts.hosting.fresh().await;
        let (row, target, target_row, owner) = {
            let index = self.parts.index.index();
            let row = index.project_named_by(project.harp()).ok_or_else(|| {
                ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                    name: request.parent.clone(),
                }))
            })?;
            let (target, target_row) = target_of(&request.project, &project, &row, &index)?;
            let owner = SessionName::new(project.harp(), &request.owner)
                .ok()
                .and_then(|owner| index.find_by_id(&owner.uid()));
            (row, target, target_row, owner)
        };
        if owner.is_none() {
            let unknown = sessions::SessionUnknown {
                harp: request.owner.clone(),
            };
            return Err(ApiRefusal::typed(
                &angzarr_embedded::markup::TypedRefusal::of(&unknown),
                <sessions::SessionUnknown as sessions::Refusal>::REFUSAL_STATUS,
            )
            .into());
        }
        // A child in another project works there only once the user trusts
        // it (D8): refused before anything is planned or written.
        let trusted = self.trusts(&target_row.project_id);
        if target_row.project_id != row.project_id && !trusted {
            let untrusted = sessions::UntrustedTarget {
                root: target_row.root.clone(),
            };
            return Err(ApiRefusal::typed(
                &angzarr_embedded::markup::TypedRefusal::of(&untrusted),
                <sessions::UntrustedTarget as sessions::Refusal>::REFUSAL_STATUS,
            )
            .into());
        }
        // Its tree observed before it is planned (step 10 U11).
        self.observed(Some((target.clone(), target_row.root.as_str())))
            .await?;
        let handing = HandingOff {
            root: target_row.root.clone(),
            trusted,
            child: child.clone(),
            engine: request.engine.clone(),
            agent: request.agent.clone(),
            env: request
                .caller_env()
                .cloned()
                .unwrap_or_default()
                .vars
                .into_iter()
                .map(|var| (var.name, var.value))
                .collect(),
        };
        let planned = plans.plan(&handing).map_err(|code| {
            ApiError::from(ApiRefusal::of(&v1::ConfigUnreadable {
                root: target_row.root.clone(),
                detail: code,
            }))
        })?;
        let checks = runs::EnqueueRun {
            session: child.clone(),
            incarnation: 1,
            mode: runs::RunMode::Structured,
            prompt_given: true,
            engine: planned.engine.clone(),
            drivable_engines: planned.drivable_engines,
            credential: planned.credential,
            fatal_findings: planned.fatal_findings,
            fatal: wire_findings(&planned.fatal),
            launcher_lock: String::new(),
            grant: None,
            untrusted_settings: planned.untrusted_settings,
            spec: None,
        };
        if let Some((refusal, status)) = runs::pre_launch_refusal(&checks) {
            return Err(ApiRefusal::typed(&refusal, status).into());
        }
        let (env, launcher) =
            answered(&planned.content, (sealing.as_ref(), &target_row.project_id))?;
        Ok(v1::HandOff {
            name: format!("{project}{HAND_OFFS}{child}"),
            engine: planned.engine,
            project: target.to_string(),
            env,
            launcher,
        })
    }
}

#[cfg(test)]
#[path = "hand_off.test.rs"]
mod tests;
