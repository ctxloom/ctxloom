//! Reading sessions: the listing, the search, one session, and the
//! transcripts. A read opens a project's store only if it exists
//! ([`ProjectStores::open_existing`]) and creates nothing: reading the record
//! touches nothing (session.feature). A transcript is the session's events
//! (owner ruling 87a): caught up fresh, a read sees what the hooks of a
//! session still running have delivered so far.

use angzarr_diagnostic::Report;
use angzarr_embedded::Registry;
use ports::{Project, ProjectIdentity};
use session_index::{only_state, title_of, Captured, SessionIndex, SessionRecord, State, View};
use session_ports::{Liveness, LockState, ProjectStores, SessionFiles};

/// A session as its store records it, and its transcript.
pub(crate) struct Held {
    pub record: SessionRecord,
    pub transcript: Option<String>,
}

use crate::{failed, AppError, ListQuery, SearchQuery, ShowQuery, TranscriptQuery, TranscriptRow};

/// A read's way to its projects, stores, files and locks, borrowed from the
/// service for one call.
pub(crate) struct Reading<'s, I, S, F, L> {
    pub identity: &'s mut I,
    pub stores: &'s mut S,
    pub files: &'s mut F,
    pub liveness: &'s mut L,
    pub registry: &'s Registry,
}

impl<I, S, F, L> Reading<'_, I, S, F, L>
where
    I: ProjectIdentity,
    S: ProjectStores,
    F: SessionFiles,
    L: Liveness,
{
    /// The projects a call spans: every project's under `all`, else the one
    /// `pinned` (or the work root) names, none where there is none.
    pub(crate) fn scope(
        &mut self,
        pinned: Option<&str>,
        all: bool,
    ) -> Result<Vec<Project>, AppError> {
        if all {
            return self.identity.every_project().map_err(AppError::Identity);
        }
        let project = self.identity.for_read(pinned).map_err(AppError::Identity)?;
        Ok(project.into_iter().collect())
    }

    /// `project`'s sessions as its store holds them now, most recently
    /// started first; none where it has no store yet.
    pub(crate) async fn records(&mut self, project: &Project) -> Result<Vec<Held>, AppError> {
        let Some(store) = self.stores.open_existing(project).await.map_err(failed)? else {
            return Ok(Vec::new());
        };
        // Caught up fresh, from nothing: a read is the store as it is now.
        let mut catch_up = SessionIndex::catch_up();
        let caught = catch_up
            .step(store, self.registry.catalog())
            .await
            .map_err(|error| AppError::Failed(Report::of(&error)))?;
        if let Some((_, error)) = caught.failures().first() {
            return Err(AppError::Failed(Report::of(error)));
        }
        let index = catch_up.projector().index();
        Ok(index
            .records()
            .into_iter()
            .map(|record| Held {
                transcript: index.transcript(&record.id),
                record,
            })
            .collect())
    }

    /// The session named `harp`, and the project holding it: the one
    /// `pinned` (or the work root) names first, then any other (Go finds a
    /// harp wherever it was recorded).
    pub(crate) async fn find(
        &mut self,
        pinned: Option<&str>,
        harp: &str,
    ) -> Result<(Project, SessionRecord), AppError> {
        let (project, held) = self.find_held(pinned, harp).await?;
        Ok((project, held.record))
    }

    /// [`Reading::find`], with the session's transcript.
    pub(crate) async fn find_held(
        &mut self,
        pinned: Option<&str>,
        harp: &str,
    ) -> Result<(Project, Held), AppError> {
        let here = self.scope(pinned, false)?;
        let mut searched = Vec::new();
        for project in here {
            if let Some(found) = self.find_in(&project, harp).await? {
                return Ok((project, found));
            }
            searched.push(project.id);
        }
        for project in self.scope(None, true)? {
            if searched.contains(&project.id) {
                continue;
            }
            if let Some(found) = self.find_in(&project, harp).await? {
                return Ok((project, found));
            }
        }
        Err(AppError::NoSession {
            harp: harp.to_string(),
        })
    }

    async fn find_in(&mut self, project: &Project, harp: &str) -> Result<Option<Held>, AppError> {
        Ok(self
            .records(project)
            .await?
            .into_iter()
            .find(|held| held.record.harp == harp))
    }

    /// A session as a listing shows it: its state from its lock (never its
    /// end stamp), its title and its size from its transcript, as its events
    /// hold it.
    pub(crate) fn view(&mut self, held: Held) -> View {
        let Held { record, transcript } = held;
        let state = match self.liveness.lock_state(&record.harp) {
            LockState::Held => State::Live,
            LockState::Free => State::Ended,
            LockState::Unproven => State::Unknown,
        };
        let title = transcript.as_deref().and_then(title_of);
        let captured = transcript.as_ref().map(|text| Captured {
            bytes: text.len() as u64,
        });
        View {
            record,
            state,
            title,
            captured,
        }
    }

    /// Every session of `query`'s scope, most recently started first.
    async fn sessions(&mut self, query: &ListQuery) -> Result<Vec<View>, AppError> {
        let mut records = Vec::new();
        for project in self.scope(query.project.as_deref(), query.all)? {
            records.extend(self.records(&project).await?);
        }
        records.sort_by(|a, b| {
            let started = |r: &SessionRecord| (r.started_at.seconds, r.started_at.nanos);
            started(&b.record)
                .cmp(&started(&a.record))
                .then_with(|| a.record.harp.cmp(&b.record.harp))
        });
        let views = records
            .into_iter()
            .map(|record| self.view(record))
            .collect();
        Ok(only_state(views, query.state))
    }

    pub(crate) async fn list(&mut self, query: ListQuery) -> Result<Vec<View>, AppError> {
        self.sessions(&query).await
    }

    pub(crate) async fn search(&mut self, query: SearchQuery) -> Result<Vec<View>, AppError> {
        let views = self.sessions(&query.list).await?;
        Ok(views
            .into_iter()
            .filter(|view| view.matches(&query.words))
            .collect())
    }

    pub(crate) async fn show(&mut self, query: ShowQuery) -> Result<View, AppError> {
        let (_, held) = self
            .find_held(query.project.as_deref(), &query.harp)
            .await?;
        Ok(self.view(held))
    }

    pub(crate) async fn transcripts(
        &mut self,
        query: TranscriptQuery,
    ) -> Result<Vec<TranscriptRow>, AppError> {
        let views = match &query.harp {
            Some(harp) => {
                let (_, held) = self.find_held(query.project.as_deref(), harp).await?;
                vec![self.view(held)]
            }
            None => {
                let list = ListQuery {
                    project: query.project.clone(),
                    all: query.all,
                    state: None,
                };
                self.sessions(&list).await?
            }
        };
        Ok(views
            .into_iter()
            .map(|view| TranscriptRow {
                harp: view.record.harp,
                captured: view.captured.is_some(),
                bytes: view.captured.map_or(0, |c| c.bytes),
            })
            .collect())
    }

    pub(crate) async fn transcript(&mut self, harp: &str) -> Result<String, AppError> {
        let (_, held) = self.find_held(None, harp).await?;
        held.transcript.ok_or(AppError::NoCapturedTranscript {
            harp: held.record.harp,
        })
    }
}
