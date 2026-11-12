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
    ProjectHarpMismatch, ProjectIdentityAdopted, ProjectIdentityChanged, ProjectNameClaimed,
    ProjectNameCommand, ProjectNameEvent, ProjectNameState, ProjectNameTaken, ProjectNamed,
    ProjectRegistered, ProjectState, RegisterProject, SessionRejection, UnnamedProject,
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

    /// Register the project's root and VCS identity on first sight, or
    /// recheck them (owner, 2026-11-12, project registration): `None` when
    /// it is registered and they match. Refused for a project with no harp
    /// yet, for a client that expected another harp, and for a root commit
    /// that is not the one registered (another repository at its place),
    /// each naming the registered project. Only the root commit is
    /// rechecked: a worktree's root is another directory of the same
    /// repository, and its common directory is its primary checkout's.
    #[handles_cmd(
        RegisterProject,
        refuses(UnnamedProject, ProjectIdentityChanged, ProjectHarpMismatch)
    )]
    fn register(
        &self,
        cmd: RegisterProject,
        ctx: &Context,
    ) -> Result<Option<ProjectRegistered>, Failed<Refuses<Self, RegisterProject>>> {
        let _ = ctx;
        let state = &self.project;
        let fail = |rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, RegisterProject>,
                [
                    ProjectNotNamed{project_id} => UnnamedProject,
                    ProjectIdentityChanged{project, root, registered_commit, found_commit} => ProjectIdentityChanged,
                    ProjectHarpMismatch{project, expected} => ProjectHarpMismatch,
                ]
            )
        };
        if state.harp.is_empty() {
            return Err(fail(SessionRejection::ProjectNotNamed {
                project_id: cmd.project_id,
            }));
        }
        let project = format!("projects/{}", state.harp);
        if !cmd.expected_harp.is_empty() && cmd.expected_harp != state.harp {
            return Err(fail(SessionRejection::ProjectHarpMismatch {
                project,
                expected: cmd.expected_harp,
            }));
        }
        if state.root.is_empty() {
            return Ok(Some(ProjectRegistered {
                project_id: cmd.project_id,
                root: cmd.root,
                git_common_dir: cmd.git_common_dir,
                root_commit: cmd.root_commit,
            }));
        }
        if cmd.root_commit != state.root_commit {
            return Err(fail(SessionRejection::ProjectIdentityChanged {
                project,
                root: cmd.root,
                registered_commit: state.root_commit.clone(),
                found_commit: cmd.root_commit,
            }));
        }
        Ok(None)
    }

    #[applies(ProjectNamed)]
    fn named(&mut self, event: ProjectNamed, page: &PageContext) {
        let _ = page;
        self.project.project_id = event.project_id;
        self.project.harp = event.harp;
    }

    #[applies(ProjectRegistered)]
    fn registered(&mut self, event: ProjectRegistered, page: &PageContext) {
        let _ = page;
        self.project.root = event.root;
        self.project.git_common_dir = event.git_common_dir;
        self.project.root_commit = event.root_commit;
    }

    #[applies(ProjectIdentityAdopted)]
    fn adopted(&mut self, event: ProjectIdentityAdopted, page: &PageContext) {
        let _ = page;
        self.project.git_common_dir = event.git_common_dir;
        self.project.root_commit = event.root_commit;
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
