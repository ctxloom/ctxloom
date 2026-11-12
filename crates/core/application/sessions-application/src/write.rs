//! Writing sessions: keep, bind, rename and remove. A write finds its session
//! as a read does, then runs one command through angzarr-embedded's kernel
//! (angzarr-embedded-runner's `Runner`), which retries it. A remove is a
//! destroy (owner rulings 94, 94b, 98) and runs no command: it purges the
//! session's streams from the store, head-guarded, and removes its directory.

use angzarr_diagnostic::Report;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{
    CommandBookExt, EventStore, Handled, OnRetryable, Purge, StoreError, StreamRef,
};
use angzarr_embedded_runner::Runner;

use crate::bridge::Port;
use crate::typed;
use ports::{Clock, CommandIds, Delay, Jitter, Project, ProjectIdentity};
use session_index::{Retained, Retention, SessionRecord};
use session_ports::{Liveness, LockState, ProjectStores, SessionFiles};
use sessions::{
    claim_root, validate_rename, BindEngineSession, ClaimName, NameClaimAggregate,
    NameClaimCommand, ReleaseName, RenameSession, SessionAggregate, SessionCommand,
    SessionRejection, SetKept, NAMES_DOMAIN, SESSION_DOMAIN,
};

use crate::read::Reading;
use crate::{
    failed, AppError, BindRequest, Bound, Class, DeadLetter, FilePlan, KeepRequest, Kept, PlanItem,
    Refusal, Removal, RemoveRequest, RenameRequest, Renamed, TitleRequest, Titled, REMOVE_VERB,
};

/// A write's way to its sessions and the kernel, borrowed from the service
/// for one call.
pub(crate) struct Writing<'s, I, S, F, L, C, D> {
    pub read: Reading<'s, I, S, F, L>,
    pub clock: &'s mut C,
    pub delay: &'s mut D,
    pub retry: OnRetryable,
    pub retention_days: u64,
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

impl<I, S, F, L, C, D> Writing<'_, I, S, F, L, C, D>
where
    I: ProjectIdentity,
    S: ProjectStores,
    F: SessionFiles,
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
        let (_project, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        Ok(Titled {
            harp: record.harp,
            title: request.title,
            changed: false,
            dead_letters: std::mem::take(self.dead_letters),
        })
    }

    /// Run `command` on the claim stream of `harp` in `project`'s store: the
    /// kernel's answer, a refusal typed.
    async fn claim_command(
        &mut self,
        project: &Project,
        harp: &str,
        command: impl Into<NameClaimCommand>,
    ) -> Result<(), AppError> {
        self.claim_typed(project, harp, command)
            .await?
            .map_err(AppError::from)
    }

    /// Run `command` on the claim stream of `harp`: done, or the domain's
    /// refusal as its typed rejection; a failure to run it at all.
    async fn claim_typed(
        &mut self,
        project: &Project,
        harp: &str,
        command: impl Into<NameClaimCommand>,
    ) -> Result<Result<(), angzarr_embedded_runner::Refusal<SessionRejection>>, AppError> {
        let command = command.into();
        let cover = Cover {
            domain: NAMES_DOMAIN.to_string(),
            root: Some(claim_root(&project.id, harp)),
            edition: None,
        };
        let book = CommandBook::command_of(cover, &command);
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
            typed::run::<NameClaimAggregate, _, _, _>(&mut runner, store, book, &command_id)
                .await?
                .map(drop),
        )
    }

    /// Give `session`'s hold on `harp` back, best effort: a session minted
    /// before claims existed holds none, and that is not a failure (the
    /// refusal read by its type, never its code's text).
    async fn release(
        &mut self,
        project: &Project,
        harp: &str,
        session: &str,
    ) -> Result<(), AppError> {
        let released = self
            .claim_typed(
                project,
                harp,
                ReleaseName {
                    session: session.to_string(),
                },
            )
            .await?;
        let Err(refusal) = released else {
            return Ok(());
        };
        // `ReleaseName` refuses only these two, so any other refusal is one a
        // later release learned to make: an error, not "nothing to release".
        match &refusal.rejection {
            Some(SessionRejection::NoClaim | SessionRejection::NotHolder { .. }) => Ok(()),
            _ => Err(AppError::from(refusal)),
        }
    }

    /// Rename a session (plan 3.1): the session judges it on what its lock
    /// says (owner ruling 91: a live one is refused before anything is
    /// claimed); then claim the new name, move the output folder beside
    /// itself and the session's directory and lock to the new name (defect
    /// 2), record the name, free the old one; each step undone if a later one
    /// fails, so a refusal leaves everything where it was.
    pub(crate) async fn rename(&mut self, request: RenameRequest) -> Result<Renamed, AppError> {
        let (project, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        let to = request.to;
        validate_rename(&to).map_err(|r| AppError::Rejected(Report::of(&r)))?;
        if to == record.harp {
            return Err(AppError::Rejected(Report::of(
                &SessionRejection::NamesTaken { harp: to },
            )));
        }
        let lock = match self.read.liveness.lock_state(&record.harp) {
            LockState::Held => sessions::LockEvidence::Held,
            LockState::Free => sessions::LockEvidence::Free,
            LockState::Unproven => sessions::LockEvidence::Unproven,
        };
        if lock == sessions::LockEvidence::Held {
            // Asked of the session itself, so the refusal is its own, and
            // nothing is claimed or moved for a rename it refuses.
            self.commit(
                &project,
                &record,
                RenameSession {
                    harp: to.clone(),
                    output_dir: record.output_dir.clone(),
                    lock,
                },
            )
            .await?;
        }
        self.claim_command(
            &project,
            &to,
            ClaimName {
                project_id: project.id.clone(),
                harp: to.clone(),
                session: record.id.clone(),
            },
        )
        .await?;
        let new_output = beside(&record.output_dir, &to);
        let moved = match self.move_output(&record.output_dir, &new_output) {
            Ok(moved) => moved,
            Err(error) => {
                self.release(&project, &to, &record.id).await?;
                return Err(error);
            }
        };
        let undo_output = |files: &mut F| {
            if moved {
                let _ = files.move_output(&new_output, &record.output_dir);
            }
        };
        let moved_dir = match self.read.files.move_session(&record.harp, &to) {
            Ok(moved_dir) => moved_dir,
            Err(error) => {
                undo_output(self.read.files);
                self.release(&project, &to, &record.id).await?;
                return Err(AppError::Files(error));
            }
        };
        let recorded = self
            .commit(
                &project,
                &record,
                RenameSession {
                    harp: to.clone(),
                    output_dir: new_output.clone(),
                    lock,
                },
            )
            .await;
        if let Err(error) = recorded {
            if moved_dir {
                let _ = self.read.files.move_session(&to, &record.harp);
            }
            undo_output(self.read.files);
            self.release(&project, &to, &record.id).await?;
            return Err(error);
        }
        self.release(&project, &record.harp, &record.id).await?;
        Ok(Renamed {
            from: record.harp,
            to,
            output_dir: new_output,
            dead_letters: std::mem::take(self.dead_letters),
        })
    }

    fn move_output(&mut self, from: &str, to: &str) -> Result<bool, AppError> {
        if from.is_empty() {
            return Ok(false);
        }
        self.read
            .files
            .move_output(from, to)
            .map_err(AppError::Files)
    }

    /// Remove a session: report its destroy, and on `yes` apply it, unless
    /// it is refused (its lock does not prove it ended, without
    /// `even_if_live`; it is kept; it is within its retention).
    pub(crate) async fn remove(&mut self, request: RemoveRequest) -> Result<Removal, AppError> {
        let (project, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        let harp = record.harp.clone();
        let plan = self.plan(&harp)?;
        let refusal = self.refusal(&record, request.even_if_live)?;
        let removal = Removal {
            harp,
            applied: false,
            files: FilePlan {
                bytes_freed: 0,
                ..plan
            },
            events_destroyed: 0,
            refusal,
            dead_letters: Vec::new(),
        };
        if removal.refusal.is_some() || !request.yes {
            return Ok(removal);
        }
        self.destroy(&project, &record, removal).await
    }

    /// Destroy `record` (owner rulings 94, 94b, 98): purge its stream and the
    /// stream of each of its runs (UUIDv5(session, 1..=n)), each guarded by
    /// the head just read, then remove its directory and lock. The name's
    /// claim is kept (94c). The streams go first: a directory left by a
    /// failure after them names a session nothing lists (and the mint never
    /// reuses its harp).
    async fn destroy(
        &mut self,
        project: &Project,
        record: &SessionRecord,
        mut removal: Removal,
    ) -> Result<Removal, AppError> {
        let session = uuid::Uuid::parse_str(&record.id).map_err(|e| {
            AppError::Failed(
                Report::new("sessions-application-bad-id").arg("ext_err_txt", e.to_string()),
            )
        })?;
        let mut roots = vec![(SESSION_DOMAIN, session)];
        for incarnation in 1..=record.incarnation.max(1) {
            let run = runs::run_root(&record.id, incarnation);
            if let Ok(run) = uuid::Uuid::from_slice(&run.value) {
                roots.push((runs::RUN_DOMAIN, run));
            }
        }
        let store = self.read.stores.open(project).await.map_err(failed)?;
        for (domain, root) in roots {
            removal.events_destroyed += purge(store, StreamRef::main(domain, root)).await?;
        }
        removal.files.bytes_freed = self
            .read
            .files
            .destroy_session(&record.harp)
            .map_err(AppError::Files)?;
        removal.applied = true;
        removal.dead_letters = std::mem::take(self.dead_letters);
        Ok(removal)
    }

    /// What removing `harp` destroys of its files: the whole directory, the
    /// machine-written conversation (the engine's native history, and a
    /// transcript an older ctxloom left) and everything else, each named by
    /// its class.
    fn plan(&mut self, harp: &str) -> Result<FilePlan, AppError> {
        let survey = self.read.files.survey(harp).map_err(AppError::Files)?;
        let destroy = survey
            .into_iter()
            .map(|file| {
                let machine =
                    file.rel.starts_with("transcripts/") || file.rel.starts_with("native/");
                PlanItem {
                    path: file.path,
                    rel: file.rel,
                    class: if machine {
                        Class::Machine
                    } else {
                        Class::Authored
                    },
                    bytes: file.bytes,
                }
            })
            .collect();
        Ok(FilePlan {
            destroy,
            bytes_freed: 0,
        })
    }

    /// Why `record` may not be removed, if it may not: its lock does not prove
    /// its owner dead (unless `even_if_live`), then its transcript's
    /// retention.
    fn refusal(
        &mut self,
        record: &SessionRecord,
        even_if_live: bool,
    ) -> Result<Option<Refusal>, AppError> {
        let harp = record.harp.clone();
        let lock = self.read.liveness.lock_state(&harp);
        if lock != LockState::Free && !even_if_live {
            let reason = match lock {
                LockState::Held => "its lock is held: the owner is alive and running",
                _ => "it has no lock to prove the owner ended",
            };
            return Ok(Some(Refusal::NotProvenDead {
                harp,
                reason: reason.to_string(),
                verb: REMOVE_VERB.to_string(),
            }));
        }
        // The newest of its engine's native history and its own capture
        // (ruling 87a: the transcript is the session's events).
        let native = self
            .read
            .files
            .last_active(&harp)
            .map_err(AppError::Files)?;
        let last_active = [native, record.last_captured]
            .into_iter()
            .flatten()
            .max_by_key(|at| (at.seconds, at.nanos));
        let now = self.clock.now();
        let retention = Retention {
            cutoff: Some(session_index::stamp::days_before(now, self.retention_days)),
        };
        Ok(match retention.retains(Some(record), last_active) {
            None => None,
            Some(Retained::Kept) => Some(Refusal::Kept { harp }),
            Some(Retained::Recent { ended }) => Some(Refusal::Retained {
                harp,
                detail: match ended {
                    Some(at) => format!("it ended {}", session_index::stamp::rfc3339(at)),
                    None => "nothing records when it ended".to_string(),
                },
            }),
            Some(Retained::NoBound) => Some(Refusal::Retained {
                harp,
                detail: "no retention is configured".to_string(),
            }),
        })
    }
}

/// Whether `handled` recorded any event: a decision with none (a bind that
/// changes nothing) is applied and empty.
fn recorded(handled: &Handled) -> bool {
    // A fact answered DUPLICATE (a repeat bind, owner ruling 2026-11-04, S5
    // review item 8) recorded nothing new, though it re-reads its events.
    matches!(handled, Handled::Applied(_))
        && handled.books().iter().any(|book| !book.pages.is_empty())
}

/// Purge `stream` at the head just read, re-read and tried again when another
/// writer moved it in between (head-guarded, angzarr's 14.43); how many
/// events it removed.
pub(crate) async fn purge<S: EventStore + Purge>(
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

/// Where a session's output folder goes under the name `to`: beside itself.
/// A session recording none has none.
fn beside(output_dir: &str, to: &str) -> String {
    match std::path::Path::new(output_dir).parent() {
        Some(parent) if !output_dir.is_empty() => parent.join(to).display().to_string(),
        _ => String::new(),
    }
}
