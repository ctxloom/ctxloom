//! The session aggregate on angzarr-embedded's markup: thin handlers over
//! the rules in `lib.rs`.
//!
//! - the instance holds the folded [`SessionState`], read by
//!   [`SessionAggregate::state`];
//! - one `#[handles_cmd]` per command, `ctx.command.had_prior_events` saying
//!   whether the stream has history and `ctx.recorded_at()` the instant its
//!   events are recorded at: no clock;
//! - a bind, a keep, a rename, an adopt return `Option`: a change that
//!   changes nothing records nothing;
//! - every refusal is message-only, a [`SessionRejection`] as a
//!   `Failed::Rejected`;
//! - one `#[applies]` per event, folding at its page's `recorded_at`;
//! - no snapshot: a session's stream is short, and every load replays it.

use angzarr_embedded::markup::{Bug, Context, Emit, FactEmit, Failed, PageContext, Refuses};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use crate::refusal::refuse;
use crate::{
    adopted, end_recorded, engine_bound, kept_changed, lifeline, live, record, session_minted,
    title_set, AdoptRotations, BindEngineSession, ChildCannotStop, EndRecorded, EngineMismatch,
    EngineSessionBound, HarpInvalid, IncarnationEnded, IncarnationOwed, KeptChanged, MintSession,
    NoNativeSession, NoSession, RecordEnd, RecordIncarnationEnded, ResumeSession, SessionAdopted,
    SessionCommand, SessionDestroyed, SessionEvent, SessionExists, SessionMinted, SessionRejection,
    SessionRunning, SessionState, SessionUnknown, SetKept, SetSessionTitle, StopOrdered,
    StopSession, TitleSet, WorkspaceMismatch,
};
use crate::{
    daemon, ChildEnded, ChildForgotten, ChildLaunchFailed, ChildLaunched, ClearChild,
    ClearRequested, CloseSession, DelegationLimit, EndChild, ForgetChild, LaunchChild, MessageSent,
    NoOpenIncarnation, NotOwner, OwnerClosed, RecordChildLaunchFailed, SendMessage, SessionClosed,
    SessionIsClosed, UntrustedTarget, UrgencyUnsupported,
};
use crate::{
    AssistantTextShown, Compacted, PromptSubmitted, RecordAssistantText, RecordCompaction,
    RecordPromptSubmitted, RecordSessionEnded, RecordToolCall, RecordTurnEnded, SessionEnded,
    ToolCalled, TurnEnded,
};

/// The session aggregate: one session, folded from its stream.
#[derive(Clone, Debug, Default)]
pub struct SessionAggregate {
    session: SessionState,
}

impl SessionAggregate {
    /// The folded session.
    pub fn state(&self) -> &SessionState {
        &self.session
    }
}

/// `event`, folded into the session at its page's `recorded_at`
/// ([`crate::apply`]).
fn fold(session: &mut SessionState, event: SessionEvent, page: &PageContext) {
    let recorded_at = page.meta.and_then(|meta| meta.recorded_at);
    crate::apply(session, &event, recorded_at.unwrap_or_default());
}

// The domain is SESSION_DOMAIN, spelled as the markup takes it, a literal
// (`aggregate.test.rs` holds them to each other).
#[aggregate(domain = "sessions", command = SessionCommand, event = SessionEvent)]
impl SessionAggregate {
    /// Create the session in the stream the caller addressed: refused on a
    /// stream with history, recorded at `ctx.recorded_at()`.
    #[handles_cmd(MintSession, refuses(SessionExists, HarpInvalid))]
    fn mint(
        &self,
        cmd: MintSession,
        ctx: &Context,
    ) -> Result<(SessionMinted, IncarnationOwed), Failed<Refuses<Self, MintSession>>> {
        let taken = ctx.command.had_prior_events;
        let minted = session_minted(&self.session, taken, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, MintSession>,
                [SessionExists{harp} => SessionExists, HarpInvalid{harp, reason} => HarpInvalid]
            )
        })?;
        // Owner ruling 2026-11-04 (S5 review Q1 (b)): a new session owes its
        // first incarnation in the same decision.
        Ok((minted, lifeline::first_owed()))
    }

    /// Bind the engine's session and transcript: a FACT (owner ruling
    /// 2026-11-04, S5 review item 8), so it is never refused; records nothing
    /// when it changes nothing, or when the stream holds no live session.
    #[handles_fact(BindEngineSession)]
    fn bind(&self, fact: BindEngineSession, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(engine_bound(&self.session, &fact).ok().flatten())
    }

    /// Mark or unmark the transcript kept; `None` when it changes nothing.
    #[handles_cmd(SetKept, refuses(NoSession))]
    fn set_kept(
        &self,
        cmd: SetKept,
        ctx: &Context,
    ) -> Result<Option<KeptChanged>, Failed<Refuses<Self, SetKept>>> {
        let _ = ctx;
        kept_changed(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, SetKept>,
                [NoSession => NoSession]
            )
        })
    }

    /// Set the session's title (S55); `None` when it is the one held.
    #[handles_cmd(SetSessionTitle, refuses(NoSession))]
    fn set_title(
        &self,
        cmd: SetSessionTitle,
        ctx: &Context,
    ) -> Result<Option<TitleSet>, Failed<Refuses<Self, SetSessionTitle>>> {
        let _ = ctx;
        title_set(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, SetSessionTitle>,
                [NoSession => NoSession]
            )
        })
    }

    /// Adopt rotations the lineage lacks; `None` when it lacks none.
    #[handles_cmd(AdoptRotations, refuses(NoSession))]
    fn adopt(
        &self,
        cmd: AdoptRotations,
        ctx: &Context,
    ) -> Result<Option<SessionAdopted>, Failed<Refuses<Self, AdoptRotations>>> {
        let _ = ctx;
        adopted(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, AdoptRotations>,
                [NoSession => NoSession]
            )
        })
    }

    /// Record when the last run ended.
    #[handles_cmd(RecordEnd, refuses(NoSession))]
    fn record_end(
        &self,
        cmd: RecordEnd,
        ctx: &Context,
    ) -> Result<EndRecorded, Failed<Refuses<Self, RecordEnd>>> {
        let _ = ctx;
        end_recorded(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, RecordEnd>,
                [NoSession => NoSession]
            )
        })
    }

    /// Order the session's runs to stop; refused from a session that is not
    /// its owner, else from a child.
    #[handles_cmd(StopSession, refuses(NoSession, ChildCannotStop, NotOwner))]
    fn stop_session(
        &self,
        cmd: StopSession,
        ctx: &Context,
    ) -> Result<Option<StopOrdered>, Failed<Refuses<Self, StopSession>>> {
        let _ = ctx;
        lifeline::stop_ordered(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, StopSession>,
                [NoSession => NoSession, ChildCannotStop{depth} => ChildCannotStop, NotOwner{requester} => NotOwner]
            )
        })
    }

    /// Close the session: terminal (K1); nothing on one already closed.
    #[handles_cmd(CloseSession, refuses(NoSession, NotOwner))]
    fn close_session(
        &self,
        cmd: CloseSession,
        ctx: &Context,
    ) -> Result<Option<SessionClosed>, Failed<Refuses<Self, CloseSession>>> {
        let _ = ctx;
        daemon::closed(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, CloseSession>,
                [NoSession => NoSession, NotOwner{requester} => NotOwner]
            )
        })
    }

    /// Send the session a message (D12); a wake to a session with no run
    /// open owes the next incarnation in the same decision (M2).
    #[handles_cmd(
        SendMessage,
        refuses(NoSession, SessionIsClosed, NotOwner, UrgencyUnsupported)
    )]
    fn send_message(
        &self,
        cmd: SendMessage,
        ctx: &Context,
    ) -> Result<Emit<Self>, Failed<Refuses<Self, SendMessage>>> {
        let _ = ctx;
        let (sent, owed) = daemon::message_sent(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, SendMessage>,
                [NoSession => NoSession, SessionClosed{harp} => SessionIsClosed, NotOwner{requester} => NotOwner, UrgencyUnsupported{urgency} => UrgencyUnsupported]
            )
        })?;
        let emit = Emit::of(sent);
        Ok(match owed {
            Some(owed) => emit.then(move |_: &Self, _: &Context| {
                Ok::<_, Failed<Refuses<Self, SendMessage>>>(owed)
            }),
            None => emit,
        })
    }

    /// Ask the session to clear its conversation (M1).
    #[handles_cmd(ClearChild, refuses(NoSession, SessionIsClosed, NotOwner))]
    fn clear_child(
        &self,
        cmd: ClearChild,
        ctx: &Context,
    ) -> Result<ClearRequested, Failed<Refuses<Self, ClearChild>>> {
        let _ = ctx;
        daemon::clear_requested(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, ClearChild>,
                [NoSession => NoSession, SessionClosed{harp} => SessionIsClosed, NotOwner{requester} => NotOwner]
            )
        })
    }

    /// Launch a child of this session, its owner (K2).
    #[handles_cmd(
        LaunchChild,
        refuses(
            NoSession,
            OwnerClosed,
            NoOpenIncarnation,
            DelegationLimit,
            UntrustedTarget,
            NotOwner
        )
    )]
    fn launch_child(
        &self,
        cmd: LaunchChild,
        ctx: &Context,
    ) -> Result<ChildLaunched, Failed<Refuses<Self, LaunchChild>>> {
        let me = ctx
            .root()
            .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
            .map(|id| id.hyphenated().to_string())
            .unwrap_or_default();
        daemon::child_launched(&self.session, &me, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, LaunchChild>,
                [NoSession => NoSession, OwnerClosed{harp} => OwnerClosed, NoOpenIncarnation{harp} => NoOpenIncarnation, DelegationLimit{limit, open} => DelegationLimit, UntrustedTarget{root} => UntrustedTarget, NotOwner{requester} => NotOwner]
            )
        })
    }

    /// Resume the session (owner ruling 115): owe its next incarnation.
    #[handles_cmd(
        ResumeSession,
        refuses(
            SessionUnknown,
            SessionDestroyed,
            SessionRunning,
            NoNativeSession,
            EngineMismatch,
            WorkspaceMismatch
        )
    )]
    fn resume(
        &self,
        cmd: ResumeSession,
        ctx: &Context,
    ) -> Result<IncarnationOwed, Failed<Refuses<Self, ResumeSession>>> {
        let _ = ctx;
        lifeline::resumed(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, ResumeSession>,
                [SessionUnknown{harp} => SessionUnknown, SessionDestroyed{harp} => SessionDestroyed, SessionRunning{harp} => SessionRunning, NoNativeSession{harp} => NoNativeSession, EngineMismatch{harp, engine, asked} => EngineMismatch, WorkspaceMismatch{harp, workspace, asked} => WorkspaceMismatch]
            )
        })
    }

    /// The owner's child ended; nothing for a child it does not hold.
    #[handles_cmd(EndChild, refuses(NoSession))]
    fn end_child(
        &self,
        cmd: EndChild,
        ctx: &Context,
    ) -> Result<Option<ChildEnded>, Failed<Refuses<Self, EndChild>>> {
        let _ = ctx;
        daemon::child_ended(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, EndChild>,
                [NoSession => NoSession]
            )
        })
    }

    /// The owner forgets its child, before the child's purge (M3).
    #[handles_cmd(ForgetChild, refuses(NoSession))]
    fn forget_child(
        &self,
        cmd: ForgetChild,
        ctx: &Context,
    ) -> Result<Option<ChildForgotten>, Failed<Refuses<Self, ForgetChild>>> {
        let _ = ctx;
        daemon::child_forgotten(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, ForgetChild>,
                [NoSession => NoSession]
            )
        })
    }

    /// The launch policy could not mint the child (a fact, K2's
    /// compensation).
    #[handles_fact(RecordChildLaunchFailed)]
    fn record_child_launch_failed(
        &self,
        fact: RecordChildLaunchFailed,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(daemon::launch_failed(&self.session, &fact))
    }

    /// A run's end, told by the run application (a fact).
    #[handles_fact(RecordIncarnationEnded)]
    fn record_incarnation_ended(
        &self,
        fact: RecordIncarnationEnded,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        lifeline::ended(&self.session, &fact)
    }

    /// RecordPromptSubmitted: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordPromptSubmitted)]
    fn hook_prompt_submitted(
        &self,
        fact: RecordPromptSubmitted,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| PromptSubmitted {
            prompt_id: fact.prompt_id,
            prompt: fact.prompt,
        }))
    }

    /// RecordAssistantText: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordAssistantText)]
    fn hook_assistant_text(
        &self,
        fact: RecordAssistantText,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| AssistantTextShown {
            prompt_id: fact.prompt_id,
            turn_id: fact.turn_id,
            message_id: fact.message_id,
            index: fact.index,
            is_final: fact.is_final,
            text: fact.text,
        }))
    }

    /// RecordToolCall: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordToolCall)]
    fn hook_tool_call(&self, fact: RecordToolCall, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| ToolCalled {
            prompt_id: fact.prompt_id,
            tool_use_id: fact.tool_use_id,
            tool: fact.tool,
            input: fact.input,
            response_bytes: fact.response_bytes,
            response_sha256: fact.response_sha256.clone(),
            status: fact.status,
        }))
    }

    /// RecordTurnEnded: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordTurnEnded)]
    fn hook_turn_ended(&self, fact: RecordTurnEnded, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| TurnEnded {
            prompt_id: fact.prompt_id,
        }))
    }

    /// RecordSessionEnded: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordSessionEnded)]
    fn hook_session_ended(
        &self,
        fact: RecordSessionEnded,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| SessionEnded {
            native_session: fact.native_session,
            reason: fact.reason,
        }))
    }

    /// RecordCompaction: a FACT (it already happened in the engine), so never refused;
    /// recorded as its event on a live session, nothing on a stream with none.
    #[handles_fact(RecordCompaction)]
    fn hook_compaction(
        &self,
        fact: RecordCompaction,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        record(live(&self.session).ok().map(|_| Compacted {
            prompt_id: fact.prompt_id,
            phase: fact.phase,
            trigger: fact.trigger,
            summary_bytes: fact.summary_bytes,
            summary_sha256: fact.summary_sha256,
        }))
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(PromptSubmitted)]
    fn applied_promptsubmitted(&mut self, event: PromptSubmitted, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(AssistantTextShown)]
    fn applied_assistanttextshown(&mut self, event: AssistantTextShown, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(ToolCalled)]
    fn applied_toolcalled(&mut self, event: ToolCalled, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(TurnEnded)]
    fn applied_turnended(&mut self, event: TurnEnded, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(SessionEnded)]
    fn applied_sessionended(&mut self, event: SessionEnded, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: the capture events carry no aggregate state; projectors fold them
    // (`crate::apply` has no arm that changes the state for them).
    #[cfg_attr(test, mutants::skip)]
    #[applies(Compacted)]
    fn applied_compacted(&mut self, event: Compacted, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(SessionMinted)]
    fn session_minted(&mut self, event: SessionMinted, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(EngineSessionBound)]
    fn engine_session_bound(&mut self, event: EngineSessionBound, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(KeptChanged)]
    fn kept_changed(&mut self, event: KeptChanged, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(TitleSet)]
    fn title_set(&mut self, event: TitleSet, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(SessionAdopted)]
    fn session_adopted(&mut self, event: SessionAdopted, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(EndRecorded)]
    fn end_recorded(&mut self, event: EndRecorded, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(IncarnationOwed)]
    fn incarnation_owed(&mut self, event: IncarnationOwed, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(IncarnationEnded)]
    fn incarnation_ended(&mut self, event: IncarnationEnded, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(StopOrdered)]
    fn stop_ordered(&mut self, event: StopOrdered, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(SessionClosed)]
    fn session_closed(&mut self, event: SessionClosed, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: a message is the read side's; `crate::apply` changes no
    // state for it.
    #[cfg_attr(test, mutants::skip)]
    #[applies(MessageSent)]
    fn message_sent(&mut self, event: MessageSent, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    // Equivalent: a clear is the read side's; `crate::apply` changes no state
    // for it.
    #[cfg_attr(test, mutants::skip)]
    #[applies(ClearRequested)]
    fn clear_requested(&mut self, event: ClearRequested, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(ChildLaunched)]
    fn child_launched(&mut self, event: ChildLaunched, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(ChildLaunchFailed)]
    fn child_launch_failed(&mut self, event: ChildLaunchFailed, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(ChildEnded)]
    fn child_ended(&mut self, event: ChildEnded, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }

    #[applies(ChildForgotten)]
    fn child_forgotten(&mut self, event: ChildForgotten, page: &PageContext) {
        fold(&mut self.session, event.into(), page);
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
pub(crate) mod tests;
