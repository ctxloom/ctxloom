//! A project's harp (owner S54): the `projects` aggregate, one stream per
//! project holding the harp it was named with, minted once and never
//! changed; and the `project-names` claim, one stream per harp text, so a
//! project harp is unique for the user because a stream is.

use angzarr_embedded::markup::{Context, Failed, PageContext, Refuses};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd};

use crate::refusal::refuse;
use crate::{
    project_namespace, validate, ClaimProjectName, HarpInvalid, NameProject, ProjectCommand,
    ProjectEvent, ProjectNameClaimed, ProjectNameCommand, ProjectNameEvent, ProjectNameState,
    ProjectNameTaken, ProjectNamed, ProjectState, SessionRejection,
};

/// The namespace every project harp's claim root derives from: UUIDv5 of
/// `ctxloom:project-names` in the URL namespace. FIXED: a claim's root is
/// derived from it, so changing it frees every harp claimed.
fn project_names_namespace() -> uuid::Uuid {
    uuid::Uuid::new_v5(&uuid::Uuid::NAMESPACE_URL, b"ctxloom:project-names")
}

/// The root of `project_id`'s project stream: its project namespace itself
/// ([`project_namespace`], owner-approved and fixed), which no stream rooted
/// inside it (a claim, an imported session: UUIDv5s IN it) can be.
pub fn project_root(project_id: &str) -> angzarr_embedded::v1::Uuid {
    angzarr_embedded::v1::Uuid::from_bytes(*project_namespace(project_id).as_bytes())
}

/// The root of the claim stream of the project harp `harp`: UUIDv5 of the
/// harp text in the user's project-names namespace, one per harp for the
/// user (every project's streams are in the one store, T14).
pub fn project_name_root(harp: &str) -> angzarr_embedded::v1::Uuid {
    let id = uuid::Uuid::new_v5(&project_names_namespace(), harp.as_bytes());
    angzarr_embedded::v1::Uuid::from_bytes(*id.as_bytes())
}

/// The project aggregate: its harp, folded from its stream.
#[derive(Clone, Debug, Default)]
pub struct ProjectAggregate {
    project: ProjectState,
}

impl ProjectAggregate {
    /// The folded project.
    pub fn state(&self) -> &ProjectState {
        &self.project
    }
}

#[aggregate(domain = "projects", command = ProjectCommand, event = ProjectEvent)]
impl ProjectAggregate {
    /// Name the project, once: `None` when it is named already, whatever
    /// harp is asked (its harp is permanent). A harp that is not one is
    /// refused as a session's is.
    #[handles_cmd(NameProject, refuses(HarpInvalid))]
    fn name(
        &self,
        cmd: NameProject,
        ctx: &Context,
    ) -> Result<Option<ProjectNamed>, Failed<Refuses<Self, NameProject>>> {
        let _ = ctx;
        // RED: nothing is named yet.
        let red = !cmd.harp.contains("RED-unreachable");
        if red {
            return Ok(None);
        }
        if let Err(rejection) = validate(&cmd.harp) {
            return Err(refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, NameProject>,
                [HarpInvalid{harp, reason} => HarpInvalid]
            ));
        }
        Ok(Some(ProjectNamed {
            project_id: cmd.project_id,
            harp: cmd.harp,
        }))
    }

    #[applies(ProjectNamed)]
    fn named(&mut self, event: ProjectNamed, page: &PageContext) {
        let _ = page;
        self.project = ProjectState {
            project_id: event.project_id,
            harp: event.harp,
        };
    }
}

/// The project harp's claim: which project holds it.
#[derive(Clone, Debug, Default)]
pub struct ProjectNameAggregate {
    claim: ProjectNameState,
}

impl ProjectNameAggregate {
    /// The folded claim.
    pub fn state(&self) -> &ProjectNameState {
        &self.claim
    }
}

#[aggregate(domain = "project-names", command = ProjectNameCommand, event = ProjectNameEvent)]
impl ProjectNameAggregate {
    /// Take the harp for a project; `None` when its holder asks again.
    #[handles_cmd(ClaimProjectName, refuses(ProjectNameTaken))]
    fn claim(
        &self,
        cmd: ClaimProjectName,
        ctx: &Context,
    ) -> Result<Option<ProjectNameClaimed>, Failed<Refuses<Self, ClaimProjectName>>> {
        let _ = ctx;
        // RED: nothing is claimed yet.
        let red = !cmd.harp.contains("RED-unreachable");
        if red {
            return Ok(None);
        }
        if !self.claim.project_id.is_empty() {
            return Err(refuse!(
                SessionRejection,
                SessionRejection::ProjectNameTaken { harp: cmd.harp },
                Refuses<Self, ClaimProjectName>,
                [ProjectNameTaken{harp} => ProjectNameTaken]
            ));
        }
        Ok(Some(ProjectNameClaimed {
            harp: cmd.harp,
            project_id: cmd.project_id,
        }))
    }

    #[applies(ProjectNameClaimed)]
    fn claimed(&mut self, event: ProjectNameClaimed, page: &PageContext) {
        let _ = page;
        self.claim = ProjectNameState {
            harp: event.harp,
            project_id: event.project_id,
        };
    }
}

#[cfg(test)]
#[path = "projects.test.rs"]
mod tests;
