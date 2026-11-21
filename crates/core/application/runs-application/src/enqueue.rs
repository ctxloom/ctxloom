//! The run's enqueue. It mints no credential: the runner's and the model's
//! tokens went with the separation model (slice 2: one principal, the
//! user), and the hook bearer's with the per-run listener (slice 4 U7, owner
//! ruling s4-Q3: a hook delivers to the user's daemon with the user
//! bearer).

use std::sync::Arc;

use angzarr_embedded::markup::TypedRefusal;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{CommandBookExt, Failure, Rejected};
use run_ports::Edge;

use runs::RunRejection;

use crate::error::AppError;

#[derive(Clone, Debug, PartialEq)]
pub struct EnqueueRequest {
    /// The session's stable id, as UUID text.
    pub session: String,
    pub incarnation: u32,
    pub mode: runs::RunMode,
    /// What the run's pre-launch checks are judged on (rulings 73e, 73f).
    pub evidence: Evidence,
    /// The run's own lock file, which the enqueuing process holds until the
    /// runner is started (rulings 86, 92): journaled, so a later process can
    /// tell when its launcher died.
    pub launcher_lock: String,
    /// What the run is given (step 9: the grant record, journaled): its MCP
    /// servers by name and its permissions document; none, no record.
    pub grant: Option<runs::Grant>,
}

/// The evidence of a run's pre-launch checks, as the run application asked its
/// ports: the run's aggregate judges it in `EnqueueRun`'s decide (owner
/// rulings 73e, 73f). Names and yes-or-no only, never a credential's value;
/// the run's event journals none of it.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Evidence {
    /// Whether the plan gave a prompt that is not blank.
    pub prompt_given: bool,
    /// The engine the run is on.
    pub engine: String,
    /// Every engine this build drives.
    pub drivable_engines: Vec<String>,
    /// A child's credential; none for a session, and for an engine that
    /// cannot be driven (ruling 73d).
    pub credential: Option<CredentialEvidence>,
    /// How many fatal startup findings the plan refused the run for (ruling
    /// 73h: a count; the findings themselves stay the library's).
    pub fatal_findings: u32,
    /// The project-only settings this launch reads that its project's own
    /// config says and the user has not trusted (step 8 U2, ruling 142):
    /// their dotted paths. Empty when the project is trusted.
    pub untrusted_settings: Vec<String>,
}

/// What a child's run authenticates with (ruling 53).
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct CredentialEvidence {
    /// The engine's source names a credentials file and no variable.
    pub file_only: bool,
    /// The variables the engine's built-in source accepts.
    pub builtin_variables: Vec<String>,
    /// The variables the child would be given a credential by.
    pub accepted_variables: Vec<String>,
    /// Whether one of them is set, and not empty.
    pub accepted_set: bool,
}

impl Evidence {
    /// Evidence that passes every check: a prompt, on `engine`, which is
    /// driven, with nothing to authenticate.
    pub fn passing(engine: &str) -> Evidence {
        Evidence {
            prompt_given: true,
            engine: engine.to_string(),
            drivable_engines: vec![engine.to_string()],
            credential: None,
            fatal_findings: 0,
            untrusted_settings: Vec::new(),
        }
    }
}

/// Why an enqueue did not happen: the run refused it (its own rejection,
/// whole, so an aggregate keeps its refusals), or the kernel failed.
#[derive(Clone, Debug, PartialEq)]
pub enum EnqueueError {
    Refused(RunRejection),
    Failed(AppError),
}

/// What the enqueue gives back: the run's root.
#[derive(Debug)]
pub struct Enqueued {
    pub run: Uuid,
}

pub struct Enqueuer<E: Edge> {
    edge: Arc<E>,
}

impl<E: Edge> Enqueuer<E> {
    pub fn new(edge: Arc<E>) -> Enqueuer<E> {
        Enqueuer { edge }
    }

    pub async fn enqueue(&self, request: EnqueueRequest) -> Result<Enqueued, EnqueueError> {
        let run = runs::run_root(&request.session, request.incarnation);
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(run.clone()),
            edition: None,
        };
        let enqueue = runs::EnqueueRun {
            session: request.session,
            incarnation: request.incarnation,
            mode: request.mode,
            prompt_given: request.evidence.prompt_given,
            engine: request.evidence.engine,
            drivable_engines: request.evidence.drivable_engines,
            credential: credential(request.evidence.credential.unwrap_or_default()),
            fatal_findings: request.evidence.fatal_findings,
            launcher_lock: request.launcher_lock,
            untrusted_settings: request.evidence.untrusted_settings,
            grant: request.grant,
        };
        let book = CommandBook::command_of(cover, &runs::RunCommand::from(enqueue));
        self.edge.handle(book).await.map_err(refused)?;
        Ok(Enqueued { run })
    }
}

/// A child's credential evidence as the command carries it; a session's (none)
/// is the default, which the run judges as nothing to authenticate.
fn credential(evidence: CredentialEvidence) -> runs::CredentialEvidence {
    runs::CredentialEvidence {
        file_only: evidence.file_only,
        builtin_variables: evidence.builtin_variables,
        accepted_variables: evidence.accepted_variables,
        accepted_set: evidence.accepted_set,
    }
}

/// The run's own rejection, whole, where the kernel refused with one of the
/// runs domain's refusals; any other failure as the use case's error.
fn refused(failure: Failure<Rejected>) -> EnqueueError {
    let ours = match &failure {
        Failure::Rejected(rejected) => rejected
            .rejection::<TypedRefusal>()
            .and_then(|typed| RunRejection::try_from(typed).ok()),
        _ => None,
    };
    match ours {
        Some(rejection) => EnqueueError::Refused(rejection),
        None => EnqueueError::Failed(AppError::from_failure(failure)),
    }
}

#[cfg(test)]
#[path = "enqueue.test.rs"]
mod tests;
