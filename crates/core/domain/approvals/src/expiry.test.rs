//! The arm policy's outputs, by the event that triggers it; the delivery's
//! name. The path through the kernel and the clock loop is the wiring's
//! (`scheduling.test.rs`).

use angzarr_embedded::markup::{pack_message, Commands, DomainMessage, Policy, PolicyContext};
use angzarr_embedded::v1::{Cover, EventRef, Uuid};
use angzarr_embedded::{Payload as _, Timestamp};
use scheduler::{stream_for, ArmAlarm, CancelAlarm, SchedulerAggregate, SchedulerCommand, Stream};

use super::{expiry_alarm, expiry_at, ArmsExpiry, DeliverToApprovals};
use crate::{
    ApprovalAnswered, ApprovalAsked, ApprovalExpired, ApprovalWithdrawn, OptionKind, SessionRef,
    APPROVALS_DOMAIN,
};

const ROOT: [u8; 16] = [9; 16];

fn ctx(recorded: i64) -> PolicyContext {
    let trigger = EventRef {
        stream: Some(Cover {
            domain: APPROVALS_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(ROOT)),
            edition: None,
        }),
        sequence: 0,
    };
    PolicyContext::new(
        trigger,
        Uuid::from_bytes([1; 16]),
        Timestamp {
            seconds: recorded,
            nanos: 5,
        },
    )
}

/// What the policy sends the scheduler for `event`.
fn sent(event: impl DomainMessage) -> Vec<(Uuid, SchedulerCommand)> {
    let commands: Option<Commands<SchedulerAggregate>> = ArmsExpiry
        .on_event(&pack_message(&event), &ctx(1000))
        .expect("the policy never fails");
    commands
        .map(|c| {
            c.orders()
                .iter()
                .enumerate()
                .map(|(i, (root, any))| {
                    assert!(!c.is_fact(i), "a command, not a fact");
                    (root.clone(), SchedulerCommand::from_any(any).unwrap())
                })
                .collect()
        })
        .unwrap_or_default()
}

fn scheduler_root() -> Uuid {
    stream_for(&target())
}

fn target() -> Stream {
    Stream {
        domain: APPROVALS_DOMAIN.to_string(),
        root: ROOT.to_vec(),
        edition: None,
    }
}

fn asked(timeout_seconds: u32) -> ApprovalAsked {
    let session = |id: &str| SessionRef {
        project_id: "p-1".to_string(),
        session: id.to_string(),
    };
    ApprovalAsked {
        ask_id: "ask-tidy-wren".to_string(),
        child: session("child"),
        run: "r-1".to_string(),
        engine: "claude-code".to_string(),
        root: session("root"),
        tool_name: "Bash".to_string(),
        input_excerpt: "{}".to_string(),
        input_bytes: 2,
        input_sha256: "abc".to_string(),
        timeout_seconds,
        engine_request_id: "toolu_1".to_string(),
        options: Vec::new(),
        name: "projects/amber/approvals/ask-tidy-wren".to_string(),
        child_name: "projects/amber/sessions/brisk".to_string(),
        root_name: "projects/amber/sessions/root".to_string(),
        agent: "default".to_string(),
    }
}

#[test]
fn an_expiry_alarm_is_named_by_its_asks_stream() {
    assert_eq!(
        expiry_alarm(&ROOT),
        "approval-expiry:09090909-0909-0909-0909-090909090909"
    );
    assert_eq!(
        expiry_at(
            Timestamp {
                seconds: 10,
                nanos: 7
            },
            300
        ),
        Timestamp {
            seconds: 310,
            nanos: 7
        }
    );
}

#[test]
fn an_ask_with_a_time_arms_its_expiry_at_its_recording_plus_its_time() {
    assert_eq!(
        sent(asked(300)),
        vec![(
            scheduler_root(),
            SchedulerCommand::ArmAlarm(ArmAlarm {
                target: target(),
                alarm_id: expiry_alarm(&ROOT),
                at: Timestamp {
                    seconds: 1300,
                    nanos: 5
                },
            })
        )]
    );
}

#[test]
fn an_ask_with_no_time_arms_nothing() {
    assert_eq!(sent(asked(0)), vec![]);
}

#[test]
fn an_answer_or_a_withdrawal_cancels_the_expiry() {
    let cancel = vec![(
        scheduler_root(),
        SchedulerCommand::CancelAlarm(CancelAlarm {
            alarm_id: expiry_alarm(&ROOT),
        }),
    )];
    assert_eq!(
        sent(ApprovalAnswered {
            option: "allow-once".to_string(),
            kind: OptionKind::AllowOnce,
            message: String::new(),
        }),
        cancel
    );
    assert_eq!(
        sent(ApprovalWithdrawn {
            reason: "steered".to_string()
        }),
        cancel
    );
    // An expiry is the alarm's own: nothing more to cancel.
    assert_eq!(sent(ApprovalExpired {}), vec![]);
}

#[test]
fn the_delivery_is_the_approvals_own() {
    assert_eq!(
        <DeliverToApprovals as Policy>::NAME,
        "approvals::ApprovalAggregate::alarms"
    );
    assert_ne!(
        <DeliverToApprovals as Policy>::NAME,
        <ArmsExpiry as Policy>::NAME
    );
}
