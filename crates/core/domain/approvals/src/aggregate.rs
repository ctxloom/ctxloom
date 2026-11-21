//! The approval aggregate on angzarr-embedded's markup: one ask.

// The `#[aggregate]` macro generates the ask's fact enum from its handlers, and
// every fact is `Record*` (the naming rule, OD-53), so its variants share
// the prefix by design.
#![allow(clippy::enum_variant_names)]

use angzarr_embedded::markup::{
    pack_message, Bug, Context, FactEmit, Failed, PageContext, Refuses, Snapshots,
};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use scheduler::RecordAlarmReached;

use crate::expiry::{expiry_alarm, expiry_at};
use crate::refusal::answer_refused;
use crate::{
    AnswerApproval, ApprovalAnswered, ApprovalAsked, ApprovalCommand, ApprovalEvent,
    ApprovalExpired, ApprovalIsSettled, ApprovalOption, ApprovalUnknown, ApprovalWithdrawn,
    ApprovalsRejection, Ask, MessageNotRelayed, OptionNotOffered, RecordApprovalAsked,
    RecordApprovalWithdrawn, Status,
};

#[derive(Clone, Debug, Default)]
pub struct ApprovalAggregate {
    ask: Ask,
}

impl ApprovalAggregate {
    /// The folded ask.
    pub fn state(&self) -> &Ask {
        &self.ask
    }
}

/// The option `cmd` answers `ask` with, or why it cannot, in the order
/// judged: no ask, a settled one, an option it does not offer, a message
/// its engine cannot carry.
fn option_of<'a>(
    ask: &'a Ask,
    cmd: &AnswerApproval,
) -> Result<&'a ApprovalOption, ApprovalsRejection> {
    let ask_id = || match ask.ask_id.is_empty() {
        true => cmd.ask_id.clone(),
        false => ask.ask_id.clone(),
    };
    if ask.status == Status::Unasked {
        return Err(ApprovalsRejection::ApprovalUnknown { ask_id: ask_id() });
    }
    if let Some(how) = ask.status.how() {
        return Err(ApprovalsRejection::ApprovalSettled {
            ask_id: ask_id(),
            how,
        });
    }
    let Some(option) = ask.options.iter().find(|o| o.id == cmd.option) else {
        let offered: Vec<&str> = ask.options.iter().map(|o| o.id.as_str()).collect();
        return Err(ApprovalsRejection::OptionNotOffered {
            ask_id: ask_id(),
            option: cmd.option.clone(),
            offered: offered.join(", "),
        });
    };
    // An ACP option is relayed by its own id, and ACP's answer is that id
    // alone (Q6).
    if !cmd.message.is_empty() && !option.engine_option_id.is_empty() {
        return Err(ApprovalsRejection::MessageNotRelayed {
            ask_id: ask_id(),
            engine: ask.engine.clone(),
        });
    }
    Ok(option)
}

/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for ApprovalAggregate {
    type Anchor = crate::generated::ApprovalState;
}

#[aggregate(domain = "approvals", command = ApprovalCommand, event = ApprovalEvent)]
impl ApprovalAggregate {
    /// The engine asked: recorded once (a redelivery is the same ask), and
    /// with a timeout of 0 (`approval_timeout: now`, D16) expired in the
    /// same decide.
    #[handles_fact(RecordApprovalAsked)]
    fn asked(&self, fact: RecordApprovalAsked, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        if self.ask.status != Status::Unasked {
            return Ok(FactEmit::none());
        }
        let at_once = fact.timeout_seconds == 0;
        let asked = ApprovalAsked {
            ask_id: fact.ask_id,
            child: fact.child,
            run: fact.run,
            engine: fact.engine,
            root: fact.root,
            tool_name: fact.tool_name,
            input_excerpt: fact.input_excerpt,
            input_bytes: fact.input_bytes,
            input_sha256: fact.input_sha256,
            timeout_seconds: fact.timeout_seconds,
            engine_request_id: fact.engine_request_id,
            options: fact.options,
            name: fact.name,
            child_name: fact.child_name,
            root_name: fact.root_name,
            agent: fact.agent,
        };
        let mut events = vec![pack_message(&asked)];
        if at_once {
            events.push(pack_message(&ApprovalExpired {}));
        }
        Ok(FactEmit::of(events))
    }

    /// The engine cancelled its call: withdrawn while it waits; nothing on
    /// a settled ask (or none).
    #[handles_fact(RecordApprovalWithdrawn)]
    fn withdrawn(
        &self,
        fact: RecordApprovalWithdrawn,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        if self.ask.status != Status::Waiting {
            return Ok(FactEmit::none());
        }
        Ok(FactEmit::of([pack_message(&ApprovalWithdrawn {
            reason: fact.reason,
        })]))
    }

    /// The scheduler says an alarm of this ask is reached (U5, D11):
    /// re-checked against the ask's own state, it records `ApprovalExpired`
    /// only while the ask still waits, for its own expiry alarm at the
    /// instant it armed. Anything else (an answered, withdrawn or expired
    /// ask, another alarm, another instant) records nothing.
    #[handles_fact(RecordAlarmReached)]
    fn reached(&self, fact: RecordAlarmReached, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let ours = !self.ask.alarm_id.is_empty() && fact.alarm_id == self.ask.alarm_id;
        let due = self.ask.expires == Some(fact.at);
        let live = self.ask.status == Status::Waiting;
        Ok(FactEmit::of(
            (live && ours && due).then(|| pack_message(&ApprovalExpired {})),
        ))
    }

    /// Answer the ask with one of its options (S25: the user).
    #[handles_cmd(
        AnswerApproval,
        refuses(
            ApprovalUnknown,
            ApprovalIsSettled,
            OptionNotOffered,
            MessageNotRelayed
        )
    )]
    fn answer(
        &self,
        cmd: AnswerApproval,
        ctx: &Context,
    ) -> Result<ApprovalAnswered, Failed<Refuses<Self, AnswerApproval>>> {
        let _ = ctx;
        let kind = option_of(&self.ask, &cmd).map_err(answer_refused)?.kind;
        Ok(ApprovalAnswered {
            option: cmd.option,
            kind,
            message: cmd.message,
        })
    }

    #[applies(ApprovalAsked)]
    fn on_asked(&mut self, event: ApprovalAsked, page: &PageContext) {
        let root = page
            .cover
            .and_then(|cover| cover.root.as_ref())
            .map(|root| root.value.to_vec())
            .unwrap_or_default();
        let recorded = page.meta.and_then(|meta| meta.recorded_at);
        // An ask with no time is expired in its own decide: no alarm.
        let timed = event.timeout_seconds > 0;
        self.ask = Ask {
            alarm_id: match timed {
                true => expiry_alarm(&root),
                false => String::new(),
            },
            expires: recorded
                .filter(|_| timed)
                .map(|at| expiry_at(at, event.timeout_seconds)),
            ask_id: event.ask_id,
            child: Some(event.child),
            root: Some(event.root),
            run: event.run,
            engine: event.engine,
            tool_name: event.tool_name,
            options: event.options,
            status: Status::Waiting,
        };
    }

    #[applies(ApprovalAnswered)]
    fn on_answered(&mut self, event: ApprovalAnswered, page: &PageContext) {
        let _ = page;
        self.ask.status = Status::Answered(event.option);
    }

    #[applies(ApprovalExpired)]
    fn on_expired(&mut self, event: ApprovalExpired, page: &PageContext) {
        let _ = (event, page);
        self.ask.status = Status::Expired;
    }

    #[applies(ApprovalWithdrawn)]
    fn on_withdrawn(&mut self, event: ApprovalWithdrawn, page: &PageContext) {
        let _ = (event, page);
        self.ask.status = Status::Withdrawn;
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
mod tests;
