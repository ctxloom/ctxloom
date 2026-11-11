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

use angzarr_embedded::markup::{Bug, Context, FactEmit, Failed, PageContext, Refuses};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use crate::refusal::refuse;
use crate::{
    adopted, end_recorded, engine_bound, kept_changed, lifeline, live, record, renamed,
    session_minted, AdoptRotations, BindEngineSession, ChildCannotStop, EndRecorded,
    EngineMismatch, EngineSessionBound, HarpInvalid, HarpTooLong, IncarnationEnded,
    IncarnationOwed, KeptChanged, MintSession, NoNativeSession, NoSession, RecordEnd,
    RecordIncarnationEnded, RenameSession, RenamedTo, ResumeSession, SessionAdopted,
    SessionCommand, SessionDestroyed, SessionEvent, SessionExists, SessionLive, SessionMinted,
    SessionRejection, SessionRunning, SessionState, SessionUnknown, SetKept, StopOrdered,
    StopSession,
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

    /// Rename the session; `None` when the name is the one held.
    #[handles_cmd(
        RenameSession,
        refuses(NoSession, HarpInvalid, HarpTooLong, SessionLive)
    )]
    fn rename(
        &self,
        cmd: RenameSession,
        ctx: &Context,
    ) -> Result<Option<RenamedTo>, Failed<Refuses<Self, RenameSession>>> {
        let _ = ctx;
        renamed(&self.session, &cmd).map_err(|rejection| {
            refuse!(
                SessionRejection,
                rejection,
                Refuses<Self, RenameSession>,
                [NoSession => NoSession, HarpInvalid{harp, reason} => HarpInvalid, HarpTooLong{harp, limit, length} => HarpTooLong, SessionLive{harp} => SessionLive]
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

    /// Order the session's runs to stop; refused from a child.
    #[handles_cmd(StopSession, refuses(NoSession, ChildCannotStop))]
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
                [NoSession => NoSession, ChildCannotStop{depth} => ChildCannotStop]
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
            EngineMismatch
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
                [SessionUnknown{harp} => SessionUnknown, SessionDestroyed{harp} => SessionDestroyed, SessionRunning{harp} => SessionRunning, NoNativeSession{harp} => NoNativeSession, EngineMismatch{harp, engine, asked} => EngineMismatch]
            )
        })
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

    #[applies(RenamedTo)]
    fn renamed_to(&mut self, event: RenamedTo, page: &PageContext) {
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
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
pub(crate) mod tests;
