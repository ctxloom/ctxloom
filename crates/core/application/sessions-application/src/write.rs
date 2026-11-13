//! Writing sessions: keep, bind and title. A write finds its session as a
//! read does, then runs one command through angzarr-embedded's kernel
//! (angzarr-embedded-runner's `Runner`), which retries it. A remove is a
//! destroy the DAEMON runs (slice 2 step 6, daemon-api's `DeleteSession`):
//! [`purge`] is its head-guarded purge of one stream, the store's own.

use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{
    CommandBookExt, EventStore, Handled, OnRetryable, Purge, StoreError, StreamRef,
};
use angzarr_embedded_runner::Runner;

use crate::bridge::Port;
use crate::typed;
use ports::{Clock, CommandIds, Delay, Jitter, Project, ProjectIdentity};
use session_index::SessionRecord;
use session_ports::{Liveness, ProjectStores};
use sessions::{
    BindEngineSession, SessionAggregate, SessionCommand, SetKept, SetSessionTitle, SESSION_DOMAIN,
};

use crate::read::Reading;
use crate::{
    failed, AppError, BindRequest, Bound, DeadLetter, KeepRequest, Kept, TitleRequest, Titled,
};

/// A write's way to its sessions and the kernel, borrowed from the service
/// for one call.
pub(crate) struct Writing<'s, I, S, L, C, D> {
    pub read: Reading<'s, I, S, L>,
    pub clock: &'s mut C,
    pub delay: &'s mut D,
    pub retry: OnRetryable,
    pub dead_letters: &'s mut Vec<DeadLetter>,
}

/// The stream of the session with stable id `id` (a UUID's text).
pub(crate) fn session_cover(id: &str) -> Result<Cover, AppError> {
    let bytes = uuid::Uuid::parse_str(id)
        .map_err(|e| {
            AppError::Failed(
                angzarr_diagnostic::Report::new("sessions-application-bad-id")
                    .arg("ext_err_txt", e.to_string()),
            )
        })?
        .into_bytes();
    Ok(Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes(bytes)),
        edition: None,
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
    /// Run `command` on `record`'s stream in `project`'s store, under a fresh
    /// command id, retried by the kernel; whether it recorded anything.
    async fn commit(
        &mut self,
        project: &Project,
        record: &SessionRecord,
        command: impl Into<SessionCommand>,
    ) -> Result<bool, AppError> {
        let command = command.into();
        let book = CommandBook::command_of(session_cover(&record.id)?, &command);
        self.commit_book(project, record, book).await
    }

    /// Send `book` (a command, or a fact such as the bind) to the session.
    async fn commit_book(
        &mut self,
        project: &Project,
        record: &SessionRecord,
        book: CommandBook,
    ) -> Result<bool, AppError> {
        let _ = record;
        let store = self.read.stores.open(project).await.map_err(failed)?;
        let command_id = self.clock.command_id();
        let mut runner = Runner {
            registry: self.read.registry,
            clock: &mut Port(&mut *self.clock),
            delay: &mut Port(&mut *self.delay),
            retry: self.retry,
            dead_letters: &mut *self.dead_letters,
        };
        let handled =
            typed::run::<SessionAggregate, _, _, _>(&mut runner, store, book, &command_id)
                .await??;
        Ok(recorded(&handled))
    }

    pub(crate) async fn keep(&mut self, request: KeepRequest) -> Result<Kept, AppError> {
        let (project, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        let changed = self
            .commit(&project, &record, SetKept { kept: request.keep })
            .await?;
        Ok(Kept {
            harp: record.harp,
            kept: request.keep,
            changed,
            dead_letters: std::mem::take(self.dead_letters),
        })
    }

    pub(crate) async fn bind(&mut self, request: BindRequest) -> Result<Bound, AppError> {
        let found = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await;
        let (project, record) = match found {
            Ok(found) => found,
            Err(AppError::NoSession { .. }) => return Ok(Bound::NoSuchSession),
            Err(other) => return Err(other),
        };
        // The bind is a fact (owner ruling 2026-11-04, S5 review item 8).
        let book = sessions::bind_book(
            session_cover(&record.id)?,
            &BindEngineSession {
                native_id: request.native_id,
                transcript_path: request.transcript_path,
                engine_version: None,
            },
        );
        let changed = self.commit_book(&project, &record, book).await?;
        Ok(match changed {
            true => Bound::Recorded {
                dead_letters: std::mem::take(self.dead_letters),
            },
            false => Bound::Unchanged,
        })
    }

    /// Set a session's title (S55): one command to its stream; nothing is
    /// claimed or moved, its harp and name being permanent.
    pub(crate) async fn set_title(&mut self, request: TitleRequest) -> Result<Titled, AppError> {
        let (project, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        let changed = self
            .commit(
                &project,
                &record,
                SetSessionTitle {
                    title: request.title.clone(),
                },
            )
            .await?;
        Ok(Titled {
            harp: record.harp,
            title: request.title,
            changed,
            dead_letters: std::mem::take(self.dead_letters),
        })
    }
}

/// Whether `handled` recorded any event: a decision with none (a bind that
/// changes nothing) is applied and empty.
pub(crate) fn recorded(handled: &Handled) -> bool {
    // A fact answered DUPLICATE (a repeat bind, owner ruling 2026-11-04, S5
    // review item 8) recorded nothing new, though it re-reads its events.
    matches!(handled, Handled::Applied(_))
        && handled.books().iter().any(|book| !book.pages.is_empty())
}

/// Purge `stream` at the head just read, re-read and tried again when another
/// writer moved it in between (head-guarded, angzarr's 14.43); how many
/// events it removed.
pub async fn purge<S: EventStore + Purge>(
    store: &S,
    stream: StreamRef<'_>,
) -> Result<u64, AppError> {
    let mut tries = 0;
    loop {
        let head = store.head(stream).await.map_err(failed)?.next_sequence;
        match store.purge(stream, head).await {
            Err(StoreError::Conflict { .. }) if tries < 3 => tries += 1,
            other => return other.map_err(failed),
        }
    }
}
