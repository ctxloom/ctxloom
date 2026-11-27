//! A project's harp (owner S54): minted once, as a session's is, and kept.
//! The use case claims a harp for the project first (`project-names`, one
//! claim per harp for the user), then names the project with it
//! (`projects`); a project already named keeps its harp and claims nothing.
//! A harp another project holds is passed over for the next candidate; the
//! candidates are the caller's (fresh harps), so the use case mints nothing
//! of its own and is pure over its ports. The importer names a project it
//! imports into the same way: a project is named at its first
//! resolution, before any session of it is minted (slice 2 M1).

use angzarr_embedded::v1::{event_page, CommandBook, Cover, Uuid};
use angzarr_embedded::CommandBookExt;

use crate::dispatch::Dispatch;
use crate::{failed, AppError};
use ports::Project;
use sessions::{
    project_id_root, ClaimProjectName, NameProject, ProjectEvent, ProjectName, PROJECTS_DOMAIN,
    PROJECT_NAMES_DOMAIN,
};

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

/// The harp a project stream's `pages` name it with; `None` until it is named.
fn harp_from(pages: Vec<angzarr_embedded::v1::EventPage>) -> Option<String> {
    let mut harp = None;
    for page in pages {
        let Some(event_page::Body::Event(event)) = page.body else {
            continue;
        };
        if let Ok(ProjectEvent::ProjectNamed(named)) =
            <ProjectEvent as angzarr_embedded::Payload>::from_any(&event)
        {
            harp.get_or_insert(named.harp);
        }
    }
    harp
}

/// A command id for `step` of naming `project_id`, derived (so a rerun of
/// the same step is a DUPLICATE, whoever runs it).
fn step_id(project_id: &str, step: &str) -> Uuid {
    let root = uuid::Uuid::from_slice(&project_id_root(project_id).value).unwrap_or_default();
    Uuid::from_bytes(*uuid::Uuid::new_v5(&root, step.as_bytes()).as_bytes())
}

/// Name a project through the owner of the store (ONE WRITER): the project's
/// harp claimed and then named by commands SENT, each under the id derived
/// from the project and the harp (so a rerun, or another sender, is a
/// DUPLICATE), the stream read back from the owner.
pub async fn name_through<K: Dispatch>(
    sink: &mut K,
    project: &Project,
    harps: &[String],
) -> Result<ProjectHarp, AppError> {
    let project_id = project.id.as_str();
    let had = |harp: String| ProjectHarp {
        project_id: project_id.to_string(),
        harp,
        named: false,
    };
    if let Some(harp) = held_by(sink, project).await? {
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
        let id = step_id(project_id, &format!("claim:{harp}"));
        if let Err(refusal) = sink.send(project, book, &id).await? {
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
        let id = step_id(project_id, &format!("name:{harp}"));
        sink.send(project, book, &id)
            .await?
            .map_err(AppError::from)?;
        // Named by another call meanwhile: its harp stands, and the one
        // claimed here stays held, unused.
        let kept = held_by(sink, project).await?.unwrap_or_default();
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

/// The harp `project`'s stream, read from the owner of the store, names it with.
async fn held_by<K: Dispatch>(sink: &mut K, project: &Project) -> Result<Option<String>, AppError> {
    let cover = project_cover(&project.id);
    let pages = sink.events(project, &cover).await.map_err(failed)?;
    Ok(harp_from(pages))
}
