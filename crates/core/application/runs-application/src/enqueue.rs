//! The run's enqueue, which mints its three credentials (D29; owner ruling
//! 2026-11-04, item 16): each token is returned once, only its SHA-256 is
//! journaled.

use std::sync::Arc;

use angzarr_embedded::markup::TypedRefusal;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{CommandBookExt, Failure, Rejected};
use run_ports::{Edge, Token, TokenSource};

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
    /// The process that enqueues the run and will launch it (ruling 86),
    /// journaled so a later process can tell when it died.
    pub launcher: run_ports::ProcessIdentity,
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

/// What the enqueue gives back: the run's root and its three tokens, shown once.
/// The runner's goes to the runner's 0600 file (launcher, S6); the model's to a
/// separate file only the model-side adapter reads (S6, S10). Owner ruling
/// 2026-11-04, item 16: two principals per run. The hook's goes into the
/// engine's environment (the terminal session's or a one-shot child's) and
/// nowhere else (owner rulings 2026-11-09, items 16 and 51); like the others it is held by the launching code
/// alone, and `Token`'s `Debug` is redacted.
#[derive(Debug)]
pub struct Enqueued {
    pub run: Uuid,
    pub runner_token: Token,
    pub model_token: Token,
    pub hook_token: Token,
}

pub struct Enqueuer<E: Edge, T: TokenSource> {
    edge: Arc<E>,
    source: Arc<T>,
}

impl<E: Edge, T: TokenSource> Enqueuer<E, T> {
    pub fn new(edge: Arc<E>, source: Arc<T>) -> Enqueuer<E, T> {
        Enqueuer { edge, source }
    }

    pub async fn enqueue(&self, request: EnqueueRequest) -> Result<Enqueued, EnqueueError> {
        let runner_token = self.source.mint();
        let model_token = self.source.mint();
        let hook_token = self.source.mint();
        let run = runs::run_root(&request.session, request.incarnation);
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(run.clone()),
            edition: None,
        };
        // Only the digests are journaled (D29); the run refuses equal ones.
        let enqueue = runs::EnqueueRun {
            session: request.session,
            incarnation: request.incarnation,
            mode: request.mode,
            runner_credential_hash: runner_token.hash().as_bytes().to_vec(),
            model_credential_hash: model_token.hash().as_bytes().to_vec(),
            hook_credential_hash: hook_token.hash().as_bytes().to_vec(),
            prompt_given: request.evidence.prompt_given,
            engine: request.evidence.engine,
            drivable_engines: request.evidence.drivable_engines,
            credential: credential(request.evidence.credential.unwrap_or_default()),
            fatal_findings: request.evidence.fatal_findings,
            // RED (ruling 86): the launcher is not yet sent.
            launcher: runs::LauncherProcess { pid: 0, started: 0 },
        };
        let book = CommandBook::command_of(cover, &runs::RunCommand::from(enqueue));
        self.edge.handle(book).await.map_err(refused)?;
        Ok(Enqueued {
            run,
            runner_token,
            model_token,
            hook_token,
        })
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
