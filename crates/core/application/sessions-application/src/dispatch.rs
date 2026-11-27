//! The way out of a use case that only SENDS commands (ONE WRITER, owner
//! ruling 2026-11-25): a command is handed to whoever owns the store, never
//! run against a store of this use case's own. The importer is the first: the
//! daemon implements [`Dispatch`] over its core's edge, so its commands are
//! the daemon's, handled once under the ids the importer derived.

use std::future::Future;

use angzarr_embedded::markup::TypedRefusal;
use angzarr_embedded::v1::{CommandBook, Cover, EventPage, Uuid};
use angzarr_embedded::{DeadLetter, Failure, Recovery, Rejected, StoreError};
use angzarr_embedded_runner::{Refusal, Unrun};
use coded_diagnostic::Report;
use ports::Project;
use sessions::SessionRejection;

/// What the owner of the store answered a command it took.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Sent {
    /// It was a DUPLICATE of a command already applied: nothing was added.
    pub duplicate: bool,
}

/// A command sent, answered, or refused with the domain's rejection.
pub type Sending = Result<Result<Sent, Refusal<SessionRejection>>, Unrun>;

/// Commands in, answers out: the importer's port to the one writer.
pub trait Dispatch {
    /// `book`, a command (or a fact) for a stream of `project`, applied under
    /// `id` (a fact's id is its origin's): its answer, or the domain's refusal
    /// read back from the refusal's type.
    fn send(
        &mut self,
        project: &Project,
        book: CommandBook,
        id: &Uuid,
    ) -> impl Future<Output = Sending> + Send;

    /// The pages of `cover`'s stream of `project`, oldest first.
    fn events(
        &mut self,
        project: &Project,
        cover: &Cover,
    ) -> impl Future<Output = Result<Vec<EventPage>, StoreError>> + Send;

    /// The dead letters the sends answered since last asked.
    fn dead_letters(&mut self) -> Vec<DeadLetter>;
}

/// A refusal's domain reading, from what the owner answered.
pub fn refused(rejected: &Rejected) -> Refusal<SessionRejection> {
    let rejection = rejected
        .rejection::<TypedRefusal>()
        .and_then(|typed| SessionRejection::try_from(typed).ok());
    let report = match &rejection {
        Some(rejection) => Report::of(rejection),
        None => rejected.report().clone(),
    };
    Refusal { rejection, report }
}

/// [`failed`] for a caller that retried nothing (a test's, one attempt).
#[cfg(test)]
pub(crate) fn failed_unrun(failure: &Failure<Rejected>) -> Unrun {
    failed(failure, 1)
}

/// What a failed send is: a retryable one the owner has already retried is
/// given up on, any other is a failure.
pub fn failed(failure: &Failure<Rejected>, attempts: u32) -> Unrun {
    match failure.recovery() {
        Recovery::Retry => Unrun::GaveUp {
            attempts,
            error: Report::of(failure),
        },
        _ => Unrun::Failed(Report::of(failure)),
    }
}
