//! A project's harp (owner S54): minted once, as a session's is, and kept.
//! The use case claims a harp for the project first (`project-names`, one
//! claim per harp for the user), then names the project with it
//! (`projects`); a project already named keeps its harp and claims nothing.
//! A harp another project holds is passed over for the next candidate; the
//! candidates are the caller's (fresh harps), so the use case mints nothing
//! of its own and is pure over its ports.

use angzarr_embedded::v1::{event_page, CommandBook, Cover};
use angzarr_embedded::{CommandBookExt, EventStore, StreamRef};
use angzarr_embedded_runner::Runner;

use crate::bridge::Port;
use crate::typed;
use crate::write::Writing;
use crate::{failed, AppError};
use ports::{Clock, CommandIds, Delay, Jitter, Project, ProjectIdentity};
use session_ports::{Liveness, ProjectStores, SessionFiles};
use sessions::{
    project_name_root, project_root, ClaimProjectName, NameProject, ProjectAggregate, ProjectEvent,
    ProjectNameAggregate, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

/// Name a project: the one `project` names (else the work root's), with
/// the first of `harps` no other project holds.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct NameProjectRequest {
    pub project: Option<String>,
    /// The harps to try, in order: fresh ones the caller minted.
    pub harps: Vec<String>,
}

/// A project's harp, and whether this call named it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct ProjectHarp {
    pub project_id: String,
    pub harp: String,
    /// Named by this call; `false` when it was named already.
    pub named: bool,
}

/// `project_id`'s project stream.
fn project_cover(project_id: &str) -> Cover {
    Cover {
        domain: PROJECTS_DOMAIN.to_string(),
        root: Some(project_root(project_id)),
        edition: None,
    }
}

impl<I, S, F, L, C, D> Writing<'_, I, S, F, L, C, D>
where
    I: ProjectIdentity,
    S: ProjectStores,
    F: SessionFiles,
    L: Liveness,
    C: Clock + CommandIds,
    D: Delay + Jitter + Send,
{
    /// The harp `project`'s stream holds; `None` until it is named.
    async fn project_harp(&mut self, project: &Project) -> Result<Option<String>, AppError> {
        let store = self.read.stores.open(project).await.map_err(failed)?;
        let cover = project_cover(&project.id);
        let stream = StreamRef::try_from(&cover).map_err(failed)?;
        let slice = store.read(stream, 0, None).await.map_err(failed)?;
        let mut harp = None;
        for page in slice.pages {
            let Some(event_page::Body::Event(event)) = page.body else {
                continue;
            };
            if let Ok(ProjectEvent::ProjectNamed(named)) =
                <ProjectEvent as angzarr_embedded::Payload>::from_any(&event)
            {
                harp.get_or_insert(named.harp);
            }
        }
        Ok(harp)
    }

    /// Run `book` on `A`'s stream in `project`'s store, retried by the
    /// kernel: done, or the domain's refusal.
    async fn run_project<A>(
        &mut self,
        project: &Project,
        book: CommandBook,
    ) -> Result<Result<(), angzarr_embedded_runner::Refusal<SessionRejection>>, AppError>
    where
        A: angzarr_embedded::Domain<Rejection = angzarr_embedded::markup::TypedRefusal>,
    {
        let store = self.read.stores.open(project).await.map_err(failed)?;
        let command_id = self.clock.command_id();
        let mut runner = Runner {
            registry: self.read.registry,
            clock: &mut Port(&mut *self.clock),
            delay: &mut Port(&mut *self.delay),
            retry: self.retry,
            dead_letters: &mut *self.dead_letters,
        };
        Ok(
            typed::run::<A, _, _, _>(&mut runner, store, book, &command_id)
                .await?
                .map(drop),
        )
    }

    pub(crate) async fn name_project(
        &mut self,
        request: NameProjectRequest,
    ) -> Result<ProjectHarp, AppError> {
        let project = self
            .read
            .identity
            .for_write(request.project.as_deref())
            .map_err(AppError::Identity)?;
        let had = |harp: String| ProjectHarp {
            project_id: project.id.clone(),
            harp,
            named: false,
        };
        // RED: not named yet.
        let red = !request.harps.iter().any(|h| h == "RED-unreachable");
        if red {
            return Err(AppError::NoProjectHarp {
                project_id: project.id.clone(),
            });
        }
        if let Some(harp) = self.project_harp(&project).await? {
            return Ok(had(harp));
        }
        let mut taken = None;
        for harp in &request.harps {
            let claim = ClaimProjectName {
                harp: harp.clone(),
                project_id: project.id.clone(),
            };
            let cover = Cover {
                domain: PROJECT_NAMES_DOMAIN.to_string(),
                root: Some(project_name_root(harp)),
                edition: None,
            };
            let book = CommandBook::command_of(cover, &sessions::ProjectNameCommand::from(claim));
            match self
                .run_project::<ProjectNameAggregate>(&project, book)
                .await?
            {
                Ok(()) => {}
                Err(refusal) => {
                    taken = Some(refusal);
                    continue;
                }
            }
            let name = NameProject {
                project_id: project.id.clone(),
                harp: harp.clone(),
            };
            let book = CommandBook::command_of(
                project_cover(&project.id),
                &sessions::ProjectCommand::from(name),
            );
            self.run_project::<ProjectAggregate>(&project, book)
                .await?
                .map_err(AppError::from)?;
            // Named by another call meanwhile: its harp stands, and the one
            // claimed here stays held, unused.
            let kept = self.project_harp(&project).await?.unwrap_or_default();
            return Ok(ProjectHarp {
                named: kept == *harp,
                ..had(kept)
            });
        }
        Err(match taken {
            Some(refusal) => AppError::from(refusal),
            None => AppError::NoProjectHarp {
                project_id: project.id.clone(),
            },
        })
    }
}
