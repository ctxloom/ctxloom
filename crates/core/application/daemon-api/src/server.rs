//! The use cases the daemon's `Ctxloom` service serves (slice 2 step 5).
//! Each answers the service's own message, read from the warm session
//! index the daemon's kernel keeps (S32): nothing replays the store per
//! call. `ResolveProject` writes through the core's edge (its `projects`
//! commands, keyed, so a retry is a DUPLICATE) and reads the project's own
//! stream.
//!
//! What the daemon's loop alone knows (what it hosts, how long it has been
//! idle, how to stop it) is the [`Hosting`] port; a session's state is its
//! lock's ([`SessionStates`]); a project's harp candidates are minted
//! outside the core ([`Harps`]).

use std::sync::Arc;

use angzarr_embedded::v1::{event_page, Cover};
use angzarr_embedded::{Failure, Payload, Timestamp};
use daemon_proto::v1;
use run_ports::Edge;
use session_index::{title_of, Captured, SessionIndex, SessionIndexes, SessionRecord, State, View};
use sessions::{
    keyed_book, project_id_root, ClaimProjectName, NameProject, ProjectEvent, ProjectName,
    RegisterProject, SessionName, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

use crate::refusal::{ApiError, ApiRefusal};

/// The daemon's singleton name (AIP-156).
const DAEMON: &str = "daemon";

/// A `ListSessions` parent naming every project.
const EVERY_PROJECT: &str = "projects/-";

/// What the daemon's loop knows of itself.
pub trait Hosting: Send + Sync {
    /// The runs it hosts now (live), each with its session's name.
    fn hosted(&self) -> Vec<v1::HostedRun>;
    /// How long it has had nothing to do.
    fn idle_seconds(&self) -> u64;
    /// Ask it to stop: drain, or end what it hosts first (`end_hosted`).
    fn stop(&self, end_hosted: bool);
}

/// A session's state now, from its liveness lock in its project.
pub trait SessionStates: Send + Sync {
    fn state(&self, project_dir: &str, harp: &str) -> State;
}

/// Fresh project harps to try, in order (minted outside the core).
pub trait Harps: Send + Sync {
    fn fresh(&self) -> Vec<String>;
}

/// What the daemon says of itself, fixed at its start.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct About {
    pub pid: u32,
    pub endpoint: String,
    /// The binary's full versionator stamp (M3).
    pub version: String,
    pub start_time: Timestamp,
    pub idle_limit_seconds: u64,
}

/// The API's ports, composed by the root.
pub struct Parts<E> {
    pub edge: Arc<E>,
    /// The session index the daemon's kernel keeps warm.
    pub index: SessionIndexes,
    pub hosting: Arc<dyn Hosting>,
    pub states: Arc<dyn SessionStates>,
    pub harps: Arc<dyn Harps>,
    pub about: About,
}

/// The use cases of the daemon's API.
pub struct Api<E> {
    parts: Parts<E>,
}

impl<E: Edge> Api<E> {
    pub fn new(parts: Parts<E>) -> Api<E> {
        Api { parts }
    }

    /// `GetDaemon`: the daemon, what it hosts and its idle time. Never its
    /// bearer.
    pub async fn get_daemon(&self) -> Result<v1::Daemon, ApiError> {
        Ok(self.daemon())
    }

    fn daemon(&self) -> v1::Daemon {
        let about = &self.parts.about;
        v1::Daemon {
            name: DAEMON.to_string(),
            pid: about.pid,
            endpoint: about.endpoint.clone(),
            start_time: Some(about.start_time),
            version: about.version.clone(),
            runs: self.parts.hosting.hosted(),
            clients: 0,
            idle_seconds: self.parts.hosting.idle_seconds(),
            idle_limit_seconds: about.idle_limit_seconds,
        }
    }

    /// `StopDaemon`: refused `DaemonHosting` while it hosts live runs,
    /// unless `even_if_live`, which ends them first (S12). Answers the
    /// daemon as it was asked to stop.
    pub async fn stop_daemon(
        &self,
        request: v1::StopDaemonRequest,
    ) -> Result<v1::Daemon, ApiError> {
        let daemon = self.daemon();
        if !daemon.runs.is_empty() && !request.even_if_live {
            return Err(ApiRefusal::of(&v1::DaemonHosting {
                runs: daemon.runs.len() as u64,
                harps: daemon.runs.iter().map(|run| run.harp.clone()).collect(),
            })
            .into());
        }
        self.parts.hosting.stop(request.even_if_live);
        Ok(daemon)
    }

    /// `ResolveProject`: register-or-recheck (owner, 2026-11-12). The
    /// project the client resolved is named on first sight (the first free
    /// harp offered, S54), then registered with its root and VCS identity,
    /// or rechecked against the identity registered; the project it is.
    pub async fn resolve_project(
        &self,
        request: v1::ResolveProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        if request.project_id.is_empty() {
            return Err(ApiRefusal::of(&v1::ProjectUnknown { cwd: request.cwd }).into());
        }
        let expected = match request.name.is_empty() {
            true => String::new(),
            false => ProjectName::parse(&request.name)
                .map_err(|rejection| invalid(&request.name, &rejection))?
                .harp()
                .to_string(),
        };
        let id = request.project_id.as_str();
        if self.project(id).await?.harp.is_empty() {
            self.name(id).await?;
        }
        let register = RegisterProject {
            project_id: id.to_string(),
            root: request.root,
            git_common_dir: request.git_common_dir,
            root_commit: request.root_commit,
            expected_harp: expected,
        };
        // Keyed by all it says: the same recheck again is a DUPLICATE, and
        // decides as it did (a harp and a registered identity never change).
        let key = format!(
            "register:{id}:{}:{}:{}:{}",
            register.root, register.git_common_dir, register.root_commit, register.expected_harp
        );
        self.send(keyed_book(project_cover(id), &register, &key))
            .await?;
        let state = self.project(id).await?;
        Ok(v1::Project {
            name: format!("projects/{}", state.harp),
            root: state.root,
            uid: id.to_string(),
            git_common_dir: state.git_common_dir,
            root_commit: state.root_commit,
        })
    }

    /// Name the project `id` with the first harp offered that no other
    /// project holds (its claim first, then the name).
    async fn name(&self, id: &str) -> Result<(), ApiError> {
        for harp in self.parts.harps.fresh() {
            let Ok(name) = ProjectName::new(&harp) else {
                continue;
            };
            let claim = ClaimProjectName {
                harp: harp.clone(),
                project_id: id.to_string(),
            };
            let cover = Cover {
                domain: PROJECT_NAMES_DOMAIN.to_string(),
                root: Some(name.root()),
                edition: None,
            };
            let claimed = keyed_book(cover, &claim, &format!("project-claim:{id}:{harp}"));
            match self.send(claimed).await {
                Ok(()) => {}
                Err(ApiError::Refused(_)) => continue,
                Err(failed) => return Err(failed),
            }
            let named = NameProject {
                project_id: id.to_string(),
                harp: harp.clone(),
            };
            let key = format!("project-name:{id}:{harp}");
            return self.send(keyed_book(project_cover(id), &named, &key)).await;
        }
        Err(ApiError::Failed {
            detail: format!("no harp offered was free for the project {id}"),
        })
    }

    /// Send `book` through the core: a refusal is the domain's, as it
    /// refused.
    async fn send(&self, book: angzarr_embedded::v1::CommandBook) -> Result<(), ApiError> {
        match self.parts.edge.handle(book).await {
            Ok(_) => Ok(()),
            Err(Failure::Rejected(rejected)) => Err(ApiRefusal::rejected(&rejected).into()),
            Err(other) => Err(ApiError::failed(other)),
        }
    }

    /// The project `id` as its own stream says it now.
    async fn project(&self, id: &str) -> Result<sessions::ProjectState, ApiError> {
        let pages = self
            .parts
            .edge
            .events(&project_cover(id), 0)
            .await
            .map_err(ApiError::failed)?;
        let mut state = sessions::ProjectState::default();
        for page in pages {
            let Some(event_page::Body::Event(event)) = page.body else {
                continue;
            };
            match ProjectEvent::from_any(&event).map_err(ApiError::failed)? {
                ProjectEvent::ProjectNamed(named) if state.harp.is_empty() => {
                    state.harp = named.harp;
                }
                ProjectEvent::ProjectRegistered(registered) if state.root.is_empty() => {
                    state.root = registered.root;
                    state.git_common_dir = registered.git_common_dir;
                    state.root_commit = registered.root_commit;
                }
                _ => {}
            }
        }
        Ok(state)
    }

    /// `GetProject`: a project by its resource name, from the warm index.
    pub async fn get_project(
        &self,
        request: v1::GetProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        let name = ProjectName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        let row = self.parts.index.index().project_named_by(name.harp());
        let row = row.ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                name: request.name.clone(),
            }))
        })?;
        Ok(v1::Project {
            name: name.to_string(),
            root: row.root,
            uid: row.project_id,
            git_common_dir: row.git_common_dir,
            root_commit: row.root_commit,
        })
    }

    /// `GetSession`: a session by its resource name, from the warm index,
    /// its state its lock's.
    pub async fn get_session(
        &self,
        request: v1::GetSessionRequest,
    ) -> Result<v1::Session, ApiError> {
        let name = SessionName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        let index = self.parts.index.index();
        let record = index.find_by_id(&name.uid()).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: request.name.clone(),
            }))
        })?;
        Ok(self.resource(&index, &record).0)
    }

    /// The session `record` as its resource, with its state, and as the
    /// view a search matches.
    fn resource(&self, index: &SessionIndex, record: &SessionRecord) -> (v1::Session, View) {
        let state = self.parts.states.state(&record.project_dir, &record.harp);
        let mut session = index.resource(record);
        session.state = state_of(state) as i32;
        let transcript = index.transcript(&record.id);
        let title = match record.title.is_empty() {
            false => Some(record.title.clone()),
            true => transcript.as_deref().and_then(title_of),
        };
        let view = View {
            record: record.clone(),
            state,
            title,
            captured: transcript.map(|text| Captured {
                bytes: text.len() as u64,
            }),
            pending: Vec::new(),
        };
        (session, view)
    }

    /// Every session under `parent` (`projects/-`: every project's), as
    /// resources and views, most recently started first.
    fn under(&self, parent: &str) -> Result<Vec<(v1::Session, View)>, ApiError> {
        let project = match parent == EVERY_PROJECT {
            true => None,
            false => Some(
                ProjectName::parse(parent)
                    .map_err(|rejection| invalid(parent, &rejection))?
                    .harp()
                    .to_string(),
            ),
        };
        let index = self.parts.index.index();
        Ok(index
            .records()
            .into_iter()
            .filter(|record| match &project {
                None => true,
                Some(harp) => index.project_harp(&record.project_id) == Some(harp.as_str()),
            })
            .map(|record| self.resource(&index, &record))
            .collect())
    }

    /// `ListSessions`: a project's sessions (or every project's), in a
    /// state (unspecified: any), only those with a transcript if asked.
    pub async fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let sessions = self
            .under(&request.parent)?
            .into_iter()
            .filter(|(session, _)| in_state(session, request.state))
            .filter(|(_, view)| !request.has_transcript || view.captured.is_some())
            .map(|(session, _)| session)
            .collect();
        Ok(v1::ListSessionsResponse {
            sessions,
            next_page_token: String::new(),
        })
    }

    /// `SearchSessions`: the sessions under `parent` whose harp or title
    /// holds every word, case-insensitively (the listing's search).
    pub async fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let sessions = self
            .under(&request.parent)?
            .into_iter()
            .filter(|(session, _)| in_state(session, request.state))
            .filter(|(_, view)| view.matches(&request.words))
            .map(|(session, _)| session)
            .collect();
        Ok(v1::ListSessionsResponse {
            sessions,
            next_page_token: String::new(),
        })
    }
}

/// A project's own stream, rooted on its id (S54).
fn project_cover(project_id: &str) -> Cover {
    Cover {
        domain: PROJECTS_DOMAIN.to_string(),
        root: Some(project_id_root(project_id)),
        edition: None,
    }
}

/// `name`, refused as no resource name: the reason the grammar gave.
fn invalid(name: &str, rejection: &SessionRejection) -> ApiError {
    let reason = match rejection {
        SessionRejection::NameMalformed { reason, .. } => reason.clone(),
        _ => "shape".to_string(),
    };
    ApiRefusal::of(&v1::NameInvalid {
        name: name.to_string(),
        reason,
    })
    .into()
}

/// A lock's state, as the API says it.
fn state_of(state: State) -> v1::SessionState {
    match state {
        State::Live => v1::SessionState::Live,
        State::Ended => v1::SessionState::Ended,
        State::Unknown => v1::SessionState::Unknown,
    }
}

/// Whether `session` is in `state` (unspecified: any).
fn in_state(session: &v1::Session, state: i32) -> bool {
    state == v1::SessionState::Unspecified as i32 || session.state == state
}

#[cfg(test)]
#[path = "server.test.rs"]
mod tests;
