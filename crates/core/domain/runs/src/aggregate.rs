//! The run aggregate on angzarr-embedded's markup: one run per incarnation.
//!
//! Commands are refusable (enqueue, queue a turn, request a stop). Facts are
//! what already happened and are never refused (owner ruling 2026-11-04, item
//! 16, totality): a fact the run cannot use records nothing. A run ends exactly
//! once. A run holds no credential (owner ruling s4-Q3: the hook bearer's
//! digest went with the per-run listener).

// The `#[aggregate]` macro generates the run's fact enum from its handlers, and
// every fact is `Record*` (the naming rule, OD-53), so its variants share
// the prefix by design.
#![allow(clippy::enum_variant_names)]

use angzarr_embedded::markup::{
    pack_message, Bug, Context, DomainMessage, Emit, FactEmit, Failed, PageContext, Refuses,
    Snapshots,
};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use crate::refusal::refuse;
use crate::{
    apply, run_root, DistillWritten, EndCause, EnqueueRun, FinalReportedRecorded, LaunchPrepared,
    QueueTurn, RecordDistillWritten, RecordFinalReported, RecordLaunchFailed, RecordLaunchOrphaned,
    RecordLaunchPrepared, RecordRunExited, RecordRunnerAttached, RecordRunnerLost,
    RecordRunnerStarted, RecordSummaryReported, RecordTurnCompleted, RecordTurnFailed,
    RecordTurnStarted, RequestStop, RunCommand, RunEnded, RunEnqueued, RunEvent, RunRejection,
    RunState, RunnerAttachedRecorded, RunnerStartedRecorded, StopRequested,
    SummaryReportedRecorded, TurnCompletedRecorded, TurnFailedRecorded, TurnQueued,
    TurnStartedRecorded,
};
use crate::{AlreadyEnqueued, NoRun, NotItsStream, RunIsEnded, TurnInFlight};
use crate::{
    CredentialEvidence, FileOnlyCredential, LaunchRefusals, NoCredential, NoPrompt, Refusal,
    RunMode, UnknownEngine, UntrustedProjectSettings,
};
use crate::{InterruptRequested, LaunchFinding, TurnUrgency};

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

impl RunAggregate {
    /// Whether an enqueue has been folded.
    fn enqueued(&self) -> bool {
        self.run.mode.is_some()
    }

    /// The turn a completed or failed fact ends: the open turn holding the
    /// first of its messages (the stream's own number, never the runner's),
    /// else the number the fact names (a turn of no message).
    fn resolved_turn(&self, named: u64, message_ids: &[String]) -> u64 {
        message_ids
            .first()
            .and_then(|id| self.run.turns.iter().find(|t| t.message_ids.contains(id)))
            .map_or(named, |open| open.turn)
    }

    /// Whether the run can still take an observation: enqueued, not ended.
    fn live(&self) -> bool {
        self.enqueued() && self.run.ended.is_none()
    }

    /// The turn gate (S-b): none after the end; one turn in flight, behind
    /// which only a steer is queued, asking the turn in flight to be
    /// interrupted once; a wake or an inform waits.
    fn decide_turn(
        &self,
        cmd: QueueTurn,
    ) -> Result<(Option<InterruptRequested>, TurnQueued), RunRejection> {
        if !self.enqueued() {
            return Err(RunRejection::NoRun);
        }
        if self.run.ended.is_some() {
            return Err(RunRejection::Ended);
        }
        let interrupt = match self.run.turns.first() {
            None => None,
            Some(head) if cmd.urgency == TurnUrgency::Steer => {
                (!head.interrupt_requested).then(|| InterruptRequested {
                    turn: head.turn,
                    message_ids: head.message_ids.clone(),
                })
            }
            Some(head) => return Err(RunRejection::TurnInFlight { turn: head.turn }),
        };
        let queued = TurnQueued {
            prompt_ref: cmd.prompt_ref,
            resume: cmd.resume,
            message_ids: cmd.message_ids,
            urgency: cmd.urgency,
        };
        Ok((interrupt, queued))
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

/// The enqueue: once, on its own derived stream.
fn decide_enqueue(
    enqueued: bool,
    ctx: &Context,
    cmd: EnqueueRun,
) -> Result<RunEnqueued, RunRejection> {
    // The folded state, never the router's `had_prior_events`: a purge's
    // tombstone leaves a stream with an event and no run.
    if enqueued {
        return Err(RunRejection::AlreadyEnqueued);
    }
    if ctx.root() != Some(&run_root(&cmd.session, cmd.incarnation)) {
        return Err(RunRejection::NotItsStream);
    }
    launchable(&cmd)?;
    Ok(RunEnqueued {
        session: cmd.session,
        incarnation: cmd.incarnation,
        mode: cmd.mode,
        launcher_lock: cmd.launcher_lock,
        grant: cmd.grant,
        spec: cmd.spec,
    })
}

/// The pre-launch checks, judged on the command's evidence (owner rulings 73,
/// 73a-73h), in their order: the prompt (a one-shot's), the engine, then a
/// child's credential, which an engine nobody drives has no policy for (73d).
/// There is no posture check (owner ruling 134: ctxloom has no posture of its
/// own; an engine's permissions are its own document).
/// One failure is its own refusal; two or more, or one beside the plan's
/// fatal findings, are one `LaunchRefusals` holding each in that order, and
/// the findings alone are an empty one (they are the library's to say).
fn launchable(cmd: &EnqueueRun) -> Result<(), RunRejection> {
    let mut refusals = Vec::new();
    if cmd.mode == RunMode::OneShot && !cmd.prompt_given {
        refusals.push(RunRejection::NoPrompt);
    }
    let driven = cmd.drivable_engines.contains(&cmd.engine);
    if !driven {
        refusals.push(RunRejection::UnknownEngine {
            name: cmd.engine.clone(),
            known: cmd.drivable_engines.join(", "),
        });
    }
    if driven {
        refusals.extend(credential(&cmd.engine, &cmd.credential));
    }
    // The last check (owner Q9): the untrusted project-only settings this
    // launch reads (step 8 U2, ruling 142).
    if !cmd.untrusted_settings.is_empty() {
        let mut paths = cmd.untrusted_settings.clone();
        paths.sort();
        paths.dedup();
        refusals.push(RunRejection::UntrustedProjectSettings {
            paths: paths.join(", "),
        });
    }
    match (refusals.len(), cmd.fatal_findings) {
        (0, 0) => Ok(()),
        (1, 0) => Err(refusals.remove(0)),
        (_, fatal_findings) => Err(RunRejection::LaunchRefused {
            refusals,
            fatal_findings,
            findings: cmd
                .fatal
                .iter()
                .map(|finding| LaunchFinding {
                    text: finding.text.clone(),
                    remedy: finding.remedy.clone(),
                })
                .collect(),
        }),
    }
}

/// The pre-launch checks of `cmd` as its enqueue decides them (rulings 73,
/// 73a-73h), judged on its evidence alone and before anything is recorded:
/// a child's launch is judged so when its hand-off is made (S10), so a
/// refused launch leaves nothing. The typed refusal the enqueue would answer,
/// and the status its message declares; none when every check passes.
pub fn pre_launch_refusal(
    cmd: &EnqueueRun,
) -> Option<(
    angzarr_embedded::markup::TypedRefusal,
    angzarr_embedded::markup::BusinessStatus,
)> {
    match enqueue_refusal(launchable(cmd).err()?) {
        Failed::Rejected { rejection, status } => Some((rejection.into(), status)),
        // The checks raise refusals alone.
        _ => None,
    }
}

/// A child's credential refusal, if it has one: a source that is a file
/// alone serves no child (ruling 53); otherwise one of the variables it
/// would be given one by must be set, unless its engine asks for none.
fn credential(engine: &str, evidence: &CredentialEvidence) -> Option<RunRejection> {
    if evidence.file_only {
        return Some(RunRejection::FileOnlyCredential {
            engine: engine.to_string(),
            variables: evidence.builtin_variables.join(", "),
        });
    }
    (!evidence.accepted_variables.is_empty() && !evidence.accepted_set).then(|| {
        RunRejection::NoCredential {
            engine: engine.to_string(),
            variables: evidence.accepted_variables.join(", "),
        }
    })
}

/// The event of a fact the run could use, recorded; none when it could not.
fn recorded<E: DomainMessage>(event: Option<E>) -> Result<FactEmit<RunAggregate>, Bug> {
    Ok(FactEmit::of(event.map(|event| pack_message(&event))))
}

/// What the enqueue refuses with for `rejection`: its typed message, under
/// the status the message declares.
pub(crate) fn enqueue_refusal(
    rejection: RunRejection,
) -> Failed<Refuses<RunAggregate, EnqueueRun>> {
    if let RunRejection::LaunchRefused {
        refusals,
        fatal_findings,
        findings,
    } = rejection
    {
        return Failed::Rejected {
            rejection: Refuses::<RunAggregate, EnqueueRun>::from(launch_refused(
                refusals,
                fatal_findings,
                findings,
            )),
            status: <LaunchRefusals as Refusal>::REFUSAL_STATUS,
        };
    }
    refuse!(
        RunRejection,
        rejection,
        Refuses<RunAggregate, EnqueueRun>,
        [
            AlreadyEnqueued => AlreadyEnqueued,
            NotItsStream => NotItsStream,
            NoCredential{engine, variables} => NoCredential,
            FileOnlyCredential{engine, variables} => FileOnlyCredential,
            UntrustedProjectSettings{paths} => UntrustedProjectSettings,
            NoPrompt => NoPrompt,
            UnknownEngine{name, known} => UnknownEngine
        ]
    )
}

/// The aggregate's message: each refusal in the field of its kind, in check
/// order (one field per kind, so the fields' order is the checks').
fn launch_refused(
    refusals: Vec<RunRejection>,
    fatal_findings: u32,
    findings: Vec<LaunchFinding>,
) -> LaunchRefusals {
    let mut message = LaunchRefusals {
        no_prompt: Vec::new(),
        unknown_engine: Vec::new(),
        no_credential: Vec::new(),
        file_only_credential: Vec::new(),
        fatal_findings,
        untrusted_project_settings: Vec::new(),
        findings: findings
            .into_iter()
            .map(|LaunchFinding { text, remedy }| crate::FatalFinding { text, remedy })
            .collect(),
    };
    for refusal in refusals {
        match refusal {
            RunRejection::NoPrompt => message.no_prompt.push(NoPrompt {}),
            RunRejection::UnknownEngine { name, known } => {
                message.unknown_engine.push(UnknownEngine { name, known });
            }
            RunRejection::NoCredential { engine, variables } => {
                message
                    .no_credential
                    .push(NoCredential { engine, variables });
            }
            RunRejection::FileOnlyCredential { engine, variables } => message
                .file_only_credential
                .push(FileOnlyCredential { engine, variables }),
            RunRejection::UntrustedProjectSettings { paths } => message
                .untrusted_project_settings
                .push(UntrustedProjectSettings { paths }),
            // The checks raise nothing else.
            _ => {}
        }
    }
    message
}
/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for RunAggregate {
    type Anchor = crate::generated::RunState;
}

#[aggregate(domain = "runs", command = RunCommand, event = RunEvent)]
impl RunAggregate {
    /// Enqueue the run: once, on its own derived stream.
    #[handles_cmd(
        EnqueueRun,
        refuses(
            AlreadyEnqueued,
            NotItsStream,
            NoCredential,
            FileOnlyCredential,
            NoPrompt,
            UnknownEngine,
            UntrustedProjectSettings,
            LaunchRefusals
        )
    )]
    // The refusal is the generated `Refuses` choice, which holds
    // `LaunchRefusals` by value (four lists of refusals): the
    // markup's type, not one this crate can box.
    #[allow(clippy::result_large_err)]
    fn enqueue(
        &self,
        cmd: EnqueueRun,
        ctx: &Context,
    ) -> Result<RunEnqueued, Failed<Refuses<Self, EnqueueRun>>> {
        decide_enqueue(self.enqueued(), ctx, cmd).map_err(enqueue_refusal)
    }

    /// Queue a turn: one in flight, a steer behind it interrupting it, and
    /// none after the end.
    #[handles_cmd(QueueTurn, refuses(NoRun, RunIsEnded, TurnInFlight))]
    fn queue_turn(
        &self,
        cmd: QueueTurn,
        ctx: &Context,
    ) -> Result<Emit<Self>, Failed<Refuses<Self, QueueTurn>>> {
        let _ = ctx;
        let (interrupt, queued) = self.decide_turn(cmd).map_err(|rejection| {
            refuse!(
                RunRejection,
                rejection,
                Refuses<Self, QueueTurn>,
                [NoRun => NoRun, Ended => RunIsEnded, TurnInFlight{turn} => TurnInFlight]
            )
        })?;
        Ok(match interrupt {
            Some(interrupt) => Emit::of(interrupt).then(move |_: &Self, _: &Context| {
                Ok::<_, Failed<Refuses<Self, QueueTurn>>>(queued)
            }),
            None => Emit::of(queued),
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

    #[handles_fact(RecordRunnerAttached)]
    fn runner_attached(
        &self,
        fact: RecordRunnerAttached,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(RunnerAttachedRecorded {
            runner_id: fact.runner_id,
            protocol: fact.protocol,
        }))
    }

    /// The terminal launch prepared for the run's frontend: recorded once,
    /// while the run is live.
    #[handles_fact(RecordLaunchPrepared)]
    fn launch_prepared(
        &self,
        fact: RecordLaunchPrepared,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(
            (self.live() && !self.run.launch_prepared).then_some(LaunchPrepared {
                launch: fact.launch,
            }),
        )
    }

    #[applies(LaunchPrepared)]
    fn launch_prepared_recorded(&mut self, event: LaunchPrepared, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    /// What a distill run's answer came to: recorded once, for a run whose
    /// purpose is a distill's and that has an answer.
    #[handles_fact(RecordDistillWritten)]
    fn distill_written(
        &self,
        fact: RecordDistillWritten,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let due =
            self.run.distill.is_some() && self.run.answer.is_some() && !self.run.distill_written;
        recorded(due.then_some(DistillWritten {
            end: fact.end,
            notices: fact.notices,
        }))
    }

    #[applies(DistillWritten)]
    fn distill_written_recorded(&mut self, event: DistillWritten, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

    /// The launcher made the runner's process: recorded while the run is live.
    #[handles_fact(RecordRunnerStarted)]
    fn runner_started(
        &self,
        fact: RecordRunnerStarted,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(RunnerStartedRecorded {
            runtime: fact.runtime,
            workspace: fact.workspace,
            pid: fact.pid,
        }))
    }

    #[handles_fact(RecordTurnStarted)]
    fn turn_started(&self, fact: RecordTurnStarted, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.live().then_some(TurnStartedRecorded {
            turn: fact.turn,
            message_ids: fact.message_ids,
        }))
    }

    #[handles_fact(RecordTurnCompleted)]
    fn turn_completed(
        &self,
        fact: RecordTurnCompleted,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let turn = self.resolved_turn(fact.turn, &fact.message_ids);
        recorded(self.live().then_some(TurnCompletedRecorded {
            turn,
            native_key: fact.native_key,
            answer_ref: fact.answer_ref,
            exit_code: fact.exit_code,
            message_ids: fact.message_ids,
            answer: fact.answer,
        }))
    }

    #[handles_fact(RecordTurnFailed)]
    fn turn_failed(&self, fact: RecordTurnFailed, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let turn = self.resolved_turn(fact.turn, &fact.message_ids);
        recorded(self.live().then_some(TurnFailedRecorded {
            turn,
            class: fact.class,
            detail: fact.detail,
            message_ids: fact.message_ids,
        }))
    }

    #[handles_fact(RecordRunExited)]
    fn run_exited(&self, fact: RecordRunExited, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.end(EndCause::RunnerExit, Some(fact.code), String::new()))
    }

    #[handles_fact(RecordLaunchFailed)]
    fn launch_failed(
        &self,
        fact: RecordLaunchFailed,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(self.end(EndCause::LaunchFailed, None, fact.detail))
    }

    /// A started run whose owner is gone (rulings 93, 95): it ends for
    /// runner loss, once.
    #[handles_fact(RecordRunnerLost)]
    fn runner_lost(&self, fact: RecordRunnerLost, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = (fact, ctx);
        recorded(self.end(EndCause::RunnerLoss, None, String::new()))
    }

    /// A run owed a runner whose launcher died before starting one (ruling
    /// 100): it ends orphaned, once, and claims no launch failure.
    #[handles_fact(RecordLaunchOrphaned)]
    fn launch_orphaned(
        &self,
        fact: RecordLaunchOrphaned,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = (fact, ctx);
        recorded(self.end(EndCause::LaunchOrphaned, None, String::new()))
    }

    /// A model's claim: advisory evidence, recorded and nothing else (owner
    /// ruling 2026-11-04, item 16). Total: it applies in every state.
    #[handles_fact(RecordSummaryReported)]
    fn summary_reported(
        &self,
        fact: RecordSummaryReported,
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
    #[handles_fact(RecordFinalReported)]
    fn final_reported(
        &self,
        fact: RecordFinalReported,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
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
    #[applies(InterruptRequested)]
    fn interrupt_requested(&mut self, event: InterruptRequested, page: &PageContext) {
        apply(&mut self.run, &event.into(), page.sequence);
    }

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
pub(crate) mod tests;
