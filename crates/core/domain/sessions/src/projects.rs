//! A project's harp (owner S54): the `projects` aggregate, one stream per
//! project holding the harp it was named with, minted once and never
//! changed, rooted on the project id ([`crate::project_id_root`]: it mints
//! the name, so it cannot be rooted on it); and the `project-names` claim,
//! rooted on the project's resource name ([`crate::ProjectName::root`],
//! owner 2026-11-12, slice 2 B3/re-root), so a project harp is unique for
//! the user because a stream is.

use angzarr_embedded::markup::{Context, Failed, PageContext, Refuses};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd};

use crate::refusal::refuse;
use crate::{
    validate, ClaimProjectName, HarpInvalid, NameProject, ProjectCommand, ProjectEvent,
    ProjectNameClaimed, ProjectNameCommand, ProjectNameEvent, ProjectNameState, ProjectNameTaken,
    ProjectNamed, ProjectState, SessionRejection,
};

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
        if !self.project.harp.is_empty() {
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
        if self.claim.project_id == cmd.project_id {
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
