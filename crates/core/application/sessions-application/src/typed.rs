//! Running a command to a sessions aggregate and reading its refusal back.
//!
//! `angzarr-embedded-runner`'s `Runner::run` decodes the one typed refusal
//! message it is told (`R`); a session command refuses with any of several
//! (`proto/ctxloom/sessions/v1`), and what a person is told is the domain's
//! [`SessionRejection`], mapped back from the refusal's TYPE and nothing
//! else (never its business status, which is the protos' option). So this
//! runs the command as the runner does (its own fields: the registry, the
//! clock, the ports the kernel waits on, the retry) and settles the answer
//! with the domain's `TryFrom<&TypedRefusal>`.

use angzarr_diagnostic::Report;
use angzarr_embedded::markup::TypedRefusal;
use angzarr_embedded::v1::{CommandBook, Uuid};
use angzarr_embedded::{
    Domain, Failure, Handled, Kernel, Recovery, Rejected, Sampled, Store, Wait,
};
use angzarr_embedded_runner::{attempts, Clock, Delay, Jitter, Ported, Refusal, Runner, Unrun};
use sessions::SessionRejection;

/// Run `book`, a command to a stream of the marked-up aggregate `A`, under
/// `command_id` on `store`, as `Runner::run` does: committed (or a duplicate
/// of what was), or refused with the domain's [`SessionRejection`] read from
/// the refusal's type; or why it was not decided.
pub(crate) async fn run<A, C, D, S>(
    runner: &mut Runner<'_, C, D>,
    store: &S,
    book: CommandBook,
    command_id: &Uuid,
) -> Result<Result<Handled, Refusal<SessionRejection>>, Unrun>
where
    A: Domain<Rejection = TypedRefusal>,
    C: Clock,
    D: Delay + Jitter + Send,
    S: Store,
{
    let sampled = Sampled {
        now: runner.clock.now(),
        command_id: command_id.clone(),
    };
    let waits = Ported::new(&mut *runner.delay);
    let handled = Kernel::new(runner.registry)
        .on_retryable(runner.retry)
        .wait_with(&waits as &dyn Wait)
        .handle::<A, _>(store, book, &sampled)
        .await;
    let answered = match &handled {
        Ok(handled) => handled.dead_letters(),
        Err(failure) => failure.dead_letters(),
    };
    runner.dead_letters.extend_from_slice(answered);
    settled(handled, runner.retry)
}

/// What the kernel answered, as the use case reads it: its answer, or the
/// domain's refusal; a retryable failure, which has had the kernel's retries,
/// given up on at once; any other failure.
fn settled(
    handled: Result<Handled, Failure<Rejected>>,
    retry: angzarr_embedded::OnRetryable,
) -> Result<Result<Handled, Refusal<SessionRejection>>, Unrun> {
    match handled {
        Ok(handled) => Ok(Ok(handled)),
        Err(Failure::Rejected(rejected)) => Ok(Err(refusal(&rejected))),
        Err(failure) if failure.recovery() == Recovery::Retry => Err(Unrun::GaveUp {
            attempts: attempts(&retry),
            error: Report::of(&failure),
        }),
        Err(failure) => Err(Unrun::Failed(Report::of(&failure))),
    }
}

/// The domain's refusal `rejected` is, by its type: the rejection, and its
/// report (the rejection's code and args; the runtime's own for a refusal
/// that is none of the domain's).
fn refusal(rejected: &Rejected) -> Refusal<SessionRejection> {
    let rejection = rejected
        .rejection::<TypedRefusal>()
        .and_then(|typed| SessionRejection::try_from(typed).ok());
    let report = match &rejection {
        Some(rejection) => Report::of(rejection),
        None => rejected.report().clone(),
    };
    Refusal { rejection, report }
}

#[cfg(test)]
#[path = "typed.test.rs"]
mod tests;
