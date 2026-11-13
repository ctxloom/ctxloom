//! A project's harp (owner S54): minted once, as a session's is, and kept.
//! The use case claims a harp for the project first (`project-names`, one
//! claim per harp for the user), then names the project with it
//! (`projects`); a project already named keeps its harp and claims nothing.
//! A harp another project holds is passed over for the next candidate; the
//! candidates are the caller's (fresh harps), so the use case mints nothing
//! of its own and is pure over its ports. The importer names a project it
//! imports into the same way ([`name_in`]): a project is named at its first
//! resolution, before any session of it is minted (slice 2 M1).

use angzarr_embedded::v1::{event_page, CommandBook, Cover, Uuid};
use angzarr_embedded::{CommandBookExt, EventStore, Store, StreamRef};
use angzarr_embedded_runner::Runner;

use crate::bridge::Port;
use crate::typed;
use crate::write::Writing;
use crate::{failed, AppError};
use ports::{Clock, CommandIds, Delay, Jitter, Project, ProjectIdentity};
use session_ports::{Liveness, ProjectStores};
use sessions::{
    project_id_root, ClaimProjectName, NameProject, ProjectAggregate, ProjectEvent, ProjectName,
    ProjectNameAggregate, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
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

/// `project_id`'s project stream, rooted on the id (it mints the name).
fn project_cover(project_id: &str) -> Cover {
    Cover {
        domain: PROJECTS_DOMAIN.to_string(),
        root: Some(project_id_root(project_id)),
        edition: None,
    }
}

/// The harp `project_id`'s stream in `store` holds; `None` until it is
/// named.
pub(crate) async fn harp_of<S: EventStore>(
    store: &S,
    project_id: &str,
) -> Result<Option<String>, AppError> {
    let cover = project_cover(project_id);
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

/// A command id for `step` of naming `project_id`, derived (so a rerun of
/// the same step is a DUPLICATE, whoever runs it).
fn step_id(project_id: &str, step: &str) -> Uuid {
    let root = uuid::Uuid::from_slice(&project_id_root(project_id).value).unwrap_or_default();
    Uuid::from_bytes(*uuid::Uuid::new_v5(&root, step.as_bytes()).as_bytes())
}

/// Name the project `project_id` in `store` with the first of `harps` no
/// other project holds, or answer the harp it has: its harp's claim first
/// (`project-names`, on the project's resource name), then the name
/// (`projects`, on its id).
pub(crate) async fn name_in<C, D, S>(
    runner: &mut Runner<'_, C, D>,
    store: &S,
    project_id: &str,
    harps: &[String],
) -> Result<ProjectHarp, AppError>
where
    C: angzarr_embedded_runner::Clock,
    D: angzarr_embedded_runner::Delay + angzarr_embedded_runner::Jitter + Send,
    S: Store,
{
    let had = |harp: String| ProjectHarp {
        project_id: project_id.to_string(),
        harp,
        named: false,
    };
    if let Some(harp) = harp_of(store, project_id).await? {
        return Ok(had(harp));
    }
    let mut taken = None;
    for harp in harps {
        let Ok(name) = ProjectName::new(harp) else {
            continue;
        };
        let claim = ClaimProjectName {
            harp: harp.clone(),
            project_id: project_id.to_string(),
        };
        let cover = Cover {
            domain: PROJECT_NAMES_DOMAIN.to_string(),
            root: Some(name.root()),
            edition: None,
        };
        let book = CommandBook::command_of(cover, &sessions::ProjectNameCommand::from(claim));
        let claimed = typed::run::<ProjectNameAggregate, _, _, _>(
            runner,
            store,
            book,
            &step_id(project_id, &format!("claim:{harp}")),
        )
        .await?;
        if let Err(refusal) = claimed {
            taken = Some(refusal);
            continue;
        }
        let named = NameProject {
            project_id: project_id.to_string(),
            harp: harp.clone(),
        };
        let book = CommandBook::command_of(
            project_cover(project_id),
            &sessions::ProjectCommand::from(named),
        );
        typed::run::<ProjectAggregate, _, _, _>(
            runner,
            store,
            book,
            &step_id(project_id, &format!("name:{harp}")),
        )
        .await?
        .map_err(AppError::from)?;
        // Named by another call meanwhile: its harp stands, and the one
        // claimed here stays held, unused.
        let kept = harp_of(store, project_id).await?.unwrap_or_default();
        return Ok(ProjectHarp {
            named: kept == *harp,
            ..had(kept)
        });
    }
    Err(match taken {
        Some(refusal) => AppError::from(refusal),
        None => AppError::NoProjectHarp {
            project_id: project_id.to_string(),
        },
    })
}

impl<I, S, L, C, D> Writing<'_, I, S, L, C, D>
where
    I: ProjectIdentity,
    S: ProjectStores,
    L: Liveness,
    C: Clock + CommandIds,
    D: Delay + Jitter + Send,
{
    /// The harp `project` was named with; `None` until it is named.
    pub(crate) async fn project_harp(
        &mut self,
        project: &Project,
    ) -> Result<Option<String>, AppError> {
        let store = self.read.stores.open(project).await.map_err(failed)?;
        harp_of(store, &project.id).await
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
        let store = self.read.stores.open(&project).await.map_err(failed)?;
        let mut runner = Runner {
            registry: self.read.registry,
            clock: &mut Port(&mut *self.clock),
            delay: &mut Port(&mut *self.delay),
            retry: self.retry,
            dead_letters: &mut *self.dead_letters,
        };
        name_in(&mut runner, store, &project.id, &request.harps).await
    }
}
