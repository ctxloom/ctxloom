//! The run aggregate on angzarr-embedded's markup: one run per incarnation.
//!
//! Commands are refusable (enqueue, queue a turn, request a stop). Facts are
//! what already happened and are never refused (owner ruling 2026-11-04, item
//! 16, totality): a fact the run cannot use records nothing. A run ends exactly
//! once; folding the end revokes every credential (D29).

use angzarr_embedded::markup::{
    pack_message, Bug, Context, DomainMessage, FactEmit, Failed, PageContext, Refuses,
};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use crate::refusal::refuse;
use crate::{
    apply, run_root, EndCause, EnqueueRun, FinalReported, FinalReportedRecorded, LaunchFailed,
    QueueTurn, RequestStop, RunCommand, RunEnded, RunEnqueued, RunEvent, RunExited, RunRejection,
    RunState, RunnerAttached, RunnerAttachedRecorded, RunnerStarted, RunnerStartedRecorded,
    StopRequested, SummaryReported, SummaryReportedRecorded, TurnCompleted, TurnCompletedRecorded,
    TurnFailed, TurnFailedRecorded, TurnQueued, TurnStarted, TurnStartedRecorded,
};
use crate::{
    AlreadyEnqueued, CredentialHashInvalid, CredentialsNotDistinct, Ended, NoRun, NotItsStream,
    TurnInFlight,
};
use crate::{FileOnlyCredential, LaunchRefused, NoCredential, NoPrompt, UnknownEngine};

/// The run aggregate: one run, folded from its stream.
#[derive(Clone, Debug, Default)]
pub struct RunAggregate {
    run: RunState,
}

impl RunAggregate {
    /// The folded run.
    pub fn state(&self) -> &RunState {
        &self.run
    }
}

/// A SHA-256 digest's length.
const DIGEST_LEN: usize = 32;

impl RunAggregate {
    /// Whether an enqueue has been folded.
    fn enqueued(&self) -> bool {
        self.run.mode.is_some()
    }

    /// Whether the run can still take an observation: enqueued, not ended.
    fn live(&self) -> bool {
        self.enqueued() && self.run.ended.is_none()
    }

    /// The turn queue: one at a time, and none after the end.
    fn decide_turn(&self, cmd: QueueTurn) -> Result<TurnQueued, RunRejection> {
        if !self.enqueued() {
            return Err(RunRejection::NoRun);
        }
        if self.run.ended.is_some() {
            return Err(RunRejection::Ended);
        }
        if let Some(turn) = self.run.turn_in_flight {
            return Err(RunRejection::TurnInFlight { turn });
        }
        Ok(TurnQueued {
            prompt_ref: cmd.prompt_ref,
            resume: cmd.resume,
        })
    }

    /// The stop request: recorded once; an ended run has nothing left to
    /// stop and a second request changes nothing.
    fn decide_stop(&self, cmd: RequestStop) -> Result<Option<StopRequested>, RunRejection> {
        if !self.enqueued() {
            return Err(RunRejection::NoRun);
        }
        let stoppable = self.run.ended.is_none() && !self.run.stop_requested;
        Ok(stoppable.then_some(StopRequested { reason: cmd.reason }))
    }

    /// The end of the run, once: `None` when it is not live.
    fn end(&self, cause: EndCause, exit_code: Option<i32>, detail: String) -> Option<RunEnded> {
        self.live().then_some(RunEnded {
            cause,
            exit_code,
            detail,
        })
    }
}

/// The enqueue: once, on its own derived stream, with a distinct digest for
/// each principal's token.
fn decide_enqueue(ctx: &Context, cmd: EnqueueRun) -> Result<RunEnqueued, RunRejection> {
    if ctx.command.had_prior_events {
        return Err(RunRejection::AlreadyEnqueued);
    }
    digest(&cmd.runner_credential_hash)?;
    digest(&cmd.model_credential_hash)?;
    digest(&cmd.hook_credential_hash)?;
    let (runner, model, hook) = (
        &cmd.runner_credential_hash,
        &cmd.model_credential_hash,
        &cmd.hook_credential_hash,
    );
    if runner == model || runner == hook || model == hook {
        return Err(RunRejection::CredentialsNotDistinct);
    }
    if ctx.root() != Some(&run_root(&cmd.session, cmd.incarnation)) {
        return Err(RunRejection::NotItsStream);
    }
    Ok(RunEnqueued {
        session: cmd.session,
        incarnation: cmd.incarnation,
        mode: cmd.mode,
        runner_credential_hash: cmd.runner_credential_hash,
        model_credential_hash: cmd.model_credential_hash,
        hook_credential_hash: cmd.hook_credential_hash,
    })
}

/// The event of a fact the run could use, recorded; none when it could not.
fn recorded<E: DomainMessage>(event: Option<E>) -> Result<FactEmit<RunAggregate>, Bug> {
    Ok(FactEmit::of(event.map(|event| pack_message(&event))))
}

/// A credential hash must be a SHA-256 digest.
fn digest(hash: &[u8]) -> Result<(), RunRejection> {
    match hash.len() == DIGEST_LEN {
        true => Ok(()),
        false => Err(RunRejection::CredentialHashInvalid {
            length: hash.len() as u64,
        }),
    }
}

/// What the enqueue refuses with for `rejection`: its typed message, under
/// the status the message declares.
pub(crate) fn enqueue_refusal(
    rejection: RunRejection,
) -> Failed<Refuses<RunAggregate, EnqueueRun>> {
    // RED (ruling 73): the pre-launch checks' messages are not mapped yet.
    refuse!(
        RunRejection,
        rejection,
        Refuses<RunAggregate, EnqueueRun>,
        [AlreadyEnqueued => AlreadyEnqueued, CredentialHashInvalid{length} => CredentialHashInvalid, CredentialsNotDistinct => CredentialsNotDistinct, NotItsStream => NotItsStream]
    )
}

#[aggregate(domain = "runs", command = RunCommand, event = RunEvent)]
impl RunAggregate {
    /// Enqueue the run: once, on its own derived stream, with a distinct
    /// SHA-256 digest for each principal's token.
    #[handles_cmd(
        EnqueueRun,
        refuses(
            AlreadyEnqueued,
            CredentialHashInvalid,
            CredentialsNotDistinct,
            NotItsStream,
            NoCredential,
            FileOnlyCredential,
            NoPrompt,
            UnknownEngine,
            LaunchRefused
        )
    )]
    fn enqueue(
        &self,
        cmd: EnqueueRun,
        ctx: &Context,
    ) -> Result<RunEnqueued, Failed<Refuses<Self, EnqueueRun>>> {
        decide_enqueue(ctx, cmd).map_err(enqueue_refusal)
    }

    /// Queue a turn: one at a time, and none after the end.
    #[handles_cmd(QueueTurn, refuses(NoRun, Ended, TurnInFlight))]
    fn queue_turn(
        &self,
        cmd: QueueTurn,
        ctx: &Context,
    ) -> Result<TurnQueued, Failed<Refuses<Self, QueueTurn>>> {
        let _ = ctx;
        self.decide_turn(cmd).map_err(|rejection| {
            refuse!(
                RunRejection,
                rejection,
                Refuses<Self, QueueTurn>,
                [NoRun => NoRun, Ended => Ended, TurnInFlight{turn} => TurnInFlight]
            )
        })
    }

    /// Ask the run to stop: recorded once; the run ends later, by its exit.
    #[handles_cmd(RequestStop, refuses(NoRun))]
    fn request_stop(
        &self,
        cmd: RequestStop,
        ctx: &Context,
    ) -> Result<Option<StopRequested>, Failed<Refuses<Self, RequestStop>>> {
        let _ = ctx;
        self.decide_stop(cmd).map_err(|rejection| {
            refuse!(
                RunRejection,
                rejection,
                Refuses<Self, RequestStop>,
                [NoRun => NoRun]
            )
        })
    }

    #[handles_fact(RunnerAttached)]
    fn runner_attached(&self, fact: RunnerAttached, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(RunnerAttachedRecorded {
            runner_id: fact.runner_id,
            protocol: fact.protocol,
        }))
    }

    /// The launcher made the runner's process: recorded while the run is live.
    #[handles_fact(RunnerStarted)]
    fn runner_started(&self, fact: RunnerStarted, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(RunnerStartedRecorded {
            runtime: fact.runtime,
            workspace: fact.workspace,
            pid: fact.pid,
        }))
    }

    #[handles_fact(TurnStarted)]
    fn turn_started(&self, fact: TurnStarted, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(
            self.live()
                .then_some(TurnStartedRecorded { turn: fact.turn }),
        )
    }

    #[handles_fact(TurnCompleted)]
    fn turn_completed(&self, fact: TurnCompleted, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(TurnCompletedRecorded {
            turn: fact.turn,
            native_key: fact.native_key,
            answer_ref: fact.answer_ref,
            exit_code: fact.exit_code,
        }))
    }

    #[handles_fact(TurnFailed)]
    fn turn_failed(&self, fact: TurnFailed, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(TurnFailedRecorded {
            turn: fact.turn,
            class: fact.class,
            detail: fact.detail,
        }))
    }

    #[handles_fact(RunExited)]
    fn run_exited(&self, fact: RunExited, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.end(EndCause::RunnerExit, Some(fact.code), String::new()))
    }

    #[handles_fact(LaunchFailed)]
    fn launch_failed(&self, fact: LaunchFailed, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.end(EndCause::LaunchFailed, None, fact.detail))
    }

    /// A model's claim: advisory evidence, recorded and nothing else (owner
    /// ruling 2026-11-04, item 16). Total: it applies in every state.
    #[handles_fact(SummaryReported)]
    fn summary_reported(
        &self,
        fact: SummaryReported,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.enqueued().then_some(SummaryReportedRecorded {
            turn: fact.turn,
            ordinal: fact.ordinal,
            scope: fact.scope,
            text_ref: fact.text_ref,
        }))
    }

    /// A model's final claim: notifies, transitions nothing (it neither ends
    /// the run nor revokes a credential). Total.
    #[handles_fact(FinalReported)]
    fn final_reported(&self, fact: FinalReported, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.enqueued().then_some(FinalReportedRecorded {
            turn: fact.turn,
            text_ref: fact.text_ref,
        }))
    }

    #[applies(RunEnqueued)]
    fn run_enqueued(&mut self, event: RunEnqueued, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(RunnerAttachedRecorded)]
    fn runner_attached_recorded(&mut self, event: RunnerAttachedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(RunnerStartedRecorded)]
    fn runner_started_recorded(&mut self, event: RunnerStartedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(TurnQueued)]
    fn turn_queued(&mut self, event: TurnQueued, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    // A turn starting changes nothing the run decides on: its fold is empty, so
    // replacing it with nothing is the same function.
    #[cfg_attr(test, mutants::skip)]
    #[applies(TurnStartedRecorded)]
    fn turn_started_recorded(&mut self, event: TurnStartedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(TurnCompletedRecorded)]
    fn turn_completed_recorded(&mut self, event: TurnCompletedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(TurnFailedRecorded)]
    fn turn_failed_recorded(&mut self, event: TurnFailedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(StopRequested)]
    fn stop_requested(&mut self, event: StopRequested, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(SummaryReportedRecorded)]
    fn summary_reported_recorded(&mut self, event: SummaryReportedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(FinalReportedRecorded)]
    fn final_reported_recorded(&mut self, event: FinalReportedRecorded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    #[applies(RunEnded)]
    fn run_ended(&mut self, event: RunEnded, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
mod tests;
