//! An ask's expiry through the scheduler (step 8 U5; S26, D11, D12): the
//! ask arms an alarm for its time, and the alarm, once reached, is what
//! denies it.
//!
//! - [`ArmsExpiry`], the approvals' policy towards the scheduler: an
//!   `ApprovalAsked` with a time (`timeout_seconds` over 0) arms
//!   `approval-expiry:<ask>` ([`expiry_alarm`], the ask's stream root) at
//!   the instant it was recorded plus its time; an answer or a withdrawal
//!   cancels it. An ask with no time (`approval_timeout: now`, D16) is
//!   expired in its own decide and arms nothing.
//! - [`DeliverToApprovals`], the scheduler's delivery policy for the
//!   approvals (`scheduler::delivery!`): it hands the ask
//!   `RecordAlarmReached`, which the aggregate re-checks against its own
//!   state (still waiting, its own alarm, the instant it armed) before it
//!   records `ApprovalExpired` (the aggregate's `reached`).

use std::convert::Infallible;

use angzarr_embedded::markup::{Commands, PolicyContext};
use angzarr_embedded::Timestamp;
use angzarr_embedded_macros::{on_event, policy};
use scheduler::{stream_for, ArmAlarm, CancelAlarm, SchedulerAggregate, Stream};

use crate::{ApprovalAggregate, ApprovalAnswered, ApprovalAsked, ApprovalWithdrawn};

/// What an expiry alarm's id begins with.
pub const EXPIRY_ALARM_PREFIX: &str = "approval-expiry:";

/// The expiry alarm of the ask whose stream's root is `root`:
/// `approval-expiry:<the root as a UUID's text>`.
pub fn expiry_alarm(root: &[u8]) -> String {
    let uid = uuid::Uuid::from_slice(root)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default();
    format!("{EXPIRY_ALARM_PREFIX}{uid}")
}

/// When an ask recorded at `asked` with `timeout_seconds` runs out.
pub fn expiry_at(asked: Timestamp, timeout_seconds: u32) -> Timestamp {
    Timestamp {
        seconds: asked.seconds + i64::from(timeout_seconds),
        nanos: asked.nanos,
    }
}

/// The approvals' policy towards the scheduler: an ask with a time arms its
/// expiry, and its answer or withdrawal cancels it.
pub struct ArmsExpiry;

/// The scheduler stream of the ask the policy's trigger is on, the ask's
/// stream as the alarm's target, and its alarm's id.
fn scheduler_of(ctx: &PolicyContext) -> (angzarr_embedded::v1::Uuid, Stream, String) {
    let trigger = ctx.trigger().stream.clone().unwrap_or_default();
    let root = ctx.source_root().value.to_vec();
    let alarm = expiry_alarm(&root);
    let target = Stream {
        domain: trigger.domain,
        root,
        edition: trigger.edition,
    };
    (stream_for(&target), target, alarm)
}

/// The cancel of the trigger's ask's expiry.
fn cancelled(ctx: &PolicyContext) -> Commands<SchedulerAggregate> {
    let (root, _, alarm_id) = scheduler_of(ctx);
    Commands::none().to(root, CancelAlarm { alarm_id })
}

#[policy(name = "approvals-arm-expiry", source = ApprovalAggregate, target = SchedulerAggregate)]
impl ArmsExpiry {
    #[on_event(ApprovalAsked)]
    fn asked(
        &self,
        event: &ApprovalAsked,
        ctx: &PolicyContext,
    ) -> Result<Commands<SchedulerAggregate>, Infallible> {
        if event.timeout_seconds == 0 {
            return Ok(Commands::none());
        }
        let (root, target, alarm_id) = scheduler_of(ctx);
        Ok(Commands::none().to(
            root,
            ArmAlarm {
                target,
                alarm_id,
                at: expiry_at(ctx.recorded_at(), event.timeout_seconds),
            },
        ))
    }

    #[on_event(ApprovalAnswered)]
    fn answered(
        &self,
        event: &ApprovalAnswered,
        ctx: &PolicyContext,
    ) -> Result<Commands<SchedulerAggregate>, Infallible> {
        let _ = event;
        Ok(cancelled(ctx))
    }

    #[on_event(ApprovalWithdrawn)]
    fn withdrawn(
        &self,
        event: &ApprovalWithdrawn,
        ctx: &PolicyContext,
    ) -> Result<Commands<SchedulerAggregate>, Infallible> {
        let _ = event;
        Ok(cancelled(ctx))
    }
}

scheduler::delivery!(DeliverToApprovals, ApprovalAggregate);

#[cfg(test)]
#[path = "expiry.test.rs"]
mod tests;
