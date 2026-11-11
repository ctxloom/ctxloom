//! The orchestrator's commands as the USER sends them (D25: the human acts
//! through the ctxloom CLI, as the user, ruling 37): a message to a session
//! with its urgency (D12), a close (K1), a clear (M1) and a child's launch
//! (K2). Each finds its session as a write does and runs one command through
//! the kernel; none names a requester (the user's authority, D-N9). The
//! user's daemon hears what they record (its plan reads the store), so
//! nothing here talks to it.

use angzarr_embedded::v1::Uuid;
use ports::{Clock, CommandIds, Delay, Jitter, ProjectIdentity};
use session_ports::{Liveness, ProjectStores, SessionFiles};
use sessions::{
    ClearChild, CloseSession, LaunchChild, SendMessage, SessionAggregate, SessionCommand,
    TargetTrust, Urgency,
};

use crate::bridge::Port;
use crate::write::{session_cover, Writing};
use crate::{failed, typed, AppError};
use angzarr_embedded_runner::Runner;

/// A message to a session (D12).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct SendRequest {
    pub project: Option<String>,
    pub harp: String,
    /// The message's id: its command's key, so the same id again is a
    /// duplicate. Empty mints one.
    pub message_id: String,
    pub urgency: Urgency,
    pub text: String,
}

/// A close of a session (K1).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct CloseRequest {
    pub project: Option<String>,
    pub harp: String,
    pub cause: String,
}

/// A clear of a child (M1, D17).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct ClearRequest {
    pub project: Option<String>,
    pub harp: String,
    /// The clear's id; empty mints one.
    pub clear_id: String,
}

/// A child's launch on its owner (K2), in the owner's own project.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct LaunchRequest {
    pub project: Option<String>,
    /// The owner's harp.
    pub owner: String,
    /// The child's harp (the caller's mint: names are minted at the edge).
    pub harp: String,
    pub engine: String,
    /// The child's first task (D-N25); empty, none.
    pub prompt: String,
    /// How many open children the owner may hold.
    pub delegation_limit: u32,
    /// The child session's stable id, as UUID text: the caller's mint, so
    /// what it hands the user daemon for the child is keyed by it (D-N45).
    /// Empty mints one.
    pub session: String,
}

/// What an orchestration command did.
#[derive(Clone, PartialEq, Debug)]
pub struct Ordered {
    /// The session it was sent to.
    pub harp: String,
    /// The id it was known by: the message's, the clear's, the child
    /// session's.
    pub id: String,
    /// Whether it recorded anything (a duplicate, or a second close, did not).
    pub recorded: bool,
}

/// The urgencies `engine` takes for a session that is (`child`) or is not
/// a child the daemon hosts: what a send gives as its capability evidence.
pub fn urgencies_of(child: bool, engine: &str) -> Vec<Urgency> {
    let state = sessions::SessionState {
        owner: child.then(|| sessions::SessionRef {
            project_id: String::new(),
            session: String::new(),
        }),
        engine: engine.to_string(),
        ..sessions::SessionState::default()
    };
    session_policies::supported(&state)
}

/// A fresh id's text.
fn fresh(id: &Uuid) -> String {
    uuid::Uuid::from_slice(&id.value)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
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
    /// Run `command` on the session `harp` names, under `command_id` (a
    /// fresh one for none): the session's harp, and whether it recorded.
    async fn order(
        &mut self,
        project: Option<&str>,
        harp: &str,
        command: SessionCommand,
        command_id: Option<Uuid>,
    ) -> Result<(String, bool), AppError> {
        let (project, record) = self.read.find(project, harp).await?;
        let book =
            angzarr_embedded::CommandBookExt::command_of(session_cover(&record.id)?, &command);
        let store = self.read.stores.open(&project).await.map_err(failed)?;
        let command_id = command_id.unwrap_or_else(|| self.clock.command_id());
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
        Ok((record.harp, crate::write::recorded(&handled)))
    }

    pub(crate) async fn send(&mut self, request: SendRequest) -> Result<Ordered, AppError> {
        let (_, record) = self
            .read
            .find(request.project.as_deref(), &request.harp)
            .await?;
        let message_id = match request.message_id.is_empty() {
            true => fresh(&self.clock.command_id()),
            false => request.message_id.clone(),
        };
        let command = SendMessage {
            message_id: message_id.clone(),
            urgency: request.urgency,
            text: request.text.clone(),
            requester: None,
            supported: urgencies_of(record.owner.is_some(), &record.engine),
        };
        let (harp, recorded) = self
            .order(
                request.project.as_deref(),
                &request.harp,
                command.into(),
                Some(sessions::command_id(&message_id)),
            )
            .await?;
        Ok(Ordered {
            harp,
            id: message_id,
            recorded,
        })
    }

    pub(crate) async fn close(&mut self, request: CloseRequest) -> Result<Ordered, AppError> {
        let command = CloseSession {
            cause: request.cause.clone(),
            requester: None,
        };
        let (harp, recorded) = self
            .order(
                request.project.as_deref(),
                &request.harp,
                command.into(),
                None,
            )
            .await?;
        Ok(Ordered {
            id: harp.clone(),
            harp,
            recorded,
        })
    }

    pub(crate) async fn clear(&mut self, request: ClearRequest) -> Result<Ordered, AppError> {
        let clear_id = match request.clear_id.is_empty() {
            true => fresh(&self.clock.command_id()),
            false => request.clear_id.clone(),
        };
        let command = ClearChild {
            clear_id: clear_id.clone(),
            requester: None,
        };
        let (harp, recorded) = self
            .order(
                request.project.as_deref(),
                &request.harp,
                command.into(),
                None,
            )
            .await?;
        Ok(Ordered {
            harp,
            id: clear_id,
            recorded,
        })
    }

    pub(crate) async fn launch(&mut self, request: LaunchRequest) -> Result<Ordered, AppError> {
        let (project, owner) = self
            .read
            .find(request.project.as_deref(), &request.owner)
            .await?;
        let child = match request.session.is_empty() {
            true => fresh(&self.clock.command_id()),
            false => request.session.clone(),
        };
        let command = LaunchChild {
            child_project_id: project.id.clone(),
            child_session: child.clone(),
            child_root: owner.project_dir.clone(),
            harp: request.harp.clone(),
            engine: request.engine.clone(),
            trust: TargetTrust {
                same_project: true,
                trusted: true,
            },
            delegation_limit: request.delegation_limit,
            requester: None,
            prompt: request.prompt.clone(),
        };
        let key = format!("launch:{}:{child}", owner.id);
        let (harp, recorded) = self
            .order(
                Some(&project.id),
                &request.owner,
                command.into(),
                Some(sessions::command_id(&key)),
            )
            .await?;
        Ok(Ordered {
            harp,
            id: child,
            recorded,
        })
    }
}
