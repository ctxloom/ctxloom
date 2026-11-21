//! An ask through the kernel (`Harness`): recorded once, expired at once
//! for a timeout of 0, withdrawn while it waits, and answered once with an
//! option it offers.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{Any, Failure, Handled, Payload, Registry};

use crate::{
    claude_options, fact_book, AnswerApproval, ApprovalAggregate, ApprovalAnswered, ApprovalAsked,
    ApprovalCommand, ApprovalEvent, ApprovalExpired, ApprovalOption, ApprovalWithdrawn,
    ApprovalsRejection, OptionKind, RecordApprovalAsked, RecordApprovalWithdrawn, SessionRef,
    APPROVALS_DOMAIN,
};

const ASK: &str = "ask-tidy-wren";

fn cover() -> Cover {
    Cover {
        domain: APPROVALS_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([9; 16])),
        edition: None,
    }
}

fn ask() -> Harness<ApprovalAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<ApprovalAggregate>(ApprovalAggregate::default)
        .unwrap();
    Harness::new(registry, cover()).with_samples(Samples::starting_at(100))
}

fn session(id: &str) -> SessionRef {
    SessionRef {
        project_id: "p-1".to_string(),
        session: id.to_string(),
    }
}

fn asked_with(timeout: u32, options: Vec<ApprovalOption>) -> RecordApprovalAsked {
    RecordApprovalAsked {
        ask_id: ASK.to_string(),
        child: session("child"),
        run: "r-1".to_string(),
        engine: "claude-code".to_string(),
        root: session("root"),
        tool_name: "Bash".to_string(),
        input_excerpt: r#"{"command":"rm -rf build/"}"#.to_string(),
        input_bytes: 26,
        input_sha256: "abc".to_string(),
        timeout_seconds: timeout,
        engine_request_id: "toolu_1".to_string(),
        options,
        name: "projects/amber/approvals/ask-tidy-wren".to_string(),
        child_name: "projects/amber/sessions/brisk".to_string(),
        root_name: "projects/amber/sessions/root".to_string(),
        agent: "default".to_string(),
    }
}

fn bash() -> Vec<ApprovalOption> {
    claude_options("Bash", r#"{"command":"rm -rf build/"}"#)
}

fn acp() -> Vec<ApprovalOption> {
    vec![
        ApprovalOption {
            id: "allow-once".to_string(),
            kind: OptionKind::AllowOnce,
            label: "Allow".to_string(),
            rule: String::new(),
            engine_option_id: "once".to_string(),
        },
        ApprovalOption {
            id: "deny".to_string(),
            kind: OptionKind::RejectOnce,
            label: "Reject".to_string(),
            rule: String::new(),
            engine_option_id: "reject".to_string(),
        },
    ]
}

/// The events a book recorded on the ask's stream; none for a duplicate.
fn recorded(harness: &mut Harness<ApprovalAggregate>, book: CommandBook) -> Vec<ApprovalEvent> {
    match harness
        .when_book(book)
        .handled()
        .expect("a fact is never refused")
    {
        Handled::Applied(recorded) => recorded
            .books()
            .iter()
            .filter(|book| book.cover.as_ref() == Some(&cover()))
            .flat_map(|book| &book.pages)
            .map(|page| match &page.body {
                Some(angzarr_embedded::v1::event_page::Body::Event(event)) => {
                    ApprovalEvent::from_any(event).unwrap()
                }
                other => panic!("not an event: {other:?}"),
            })
            .collect(),
        _ => Vec::new(),
    }
}

fn fact(
    harness: &mut Harness<ApprovalAggregate>,
    fact: impl DomainMessage,
    key: &str,
) -> Vec<ApprovalEvent> {
    recorded(harness, fact_book(cover(), &fact, key))
}

fn answer(
    harness: &mut Harness<ApprovalAggregate>,
    option: &str,
    message: &str,
) -> Result<Vec<ApprovalEvent>, ApprovalsRejection> {
    let command: ApprovalCommand = AnswerApproval {
        ask_id: ASK.to_string(),
        option: option.to_string(),
        message: message.to_string(),
    }
    .into();
    match harness.when(command).result() {
        Ok(events) => Ok(events),
        Err(Failure::Rejected(rejected)) => {
            let typed = rejected
                .rejection::<angzarr_embedded::markup::TypedRefusal>()
                .expect("a typed refusal");
            Err(ApprovalsRejection::try_from(typed).expect("an approvals refusal"))
        }
        Err(other) => panic!("not a rejection: {other:?}"),
    }
}

fn asked_event(fact: RecordApprovalAsked) -> ApprovalEvent {
    ApprovalEvent::ApprovalAsked(ApprovalAsked {
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
    })
}

#[test]
fn an_ask_is_recorded_once_with_all_it_was_told() {
    let mut harness = ask();
    let told = asked_with(300, bash());
    assert_eq!(
        fact(&mut harness, told.clone(), "approval:r-1:toolu_1"),
        vec![asked_event(told.clone())]
    );
    // A redelivery is a duplicate; the same ask under another key, nothing.
    assert_eq!(
        fact(&mut harness, told.clone(), "approval:r-1:toolu_1"),
        vec![]
    );
    assert_eq!(fact(&mut harness, told, "approval:r-1:again"), vec![]);
    assert_eq!(harness.state().state().status, crate::Status::Waiting);
}

#[test]
fn an_ask_with_no_time_is_expired_in_the_same_decide() {
    let mut harness = ask();
    let told = asked_with(0, bash());
    assert_eq!(
        fact(&mut harness, told.clone(), "approval:r-1:toolu_1"),
        vec![
            asked_event(told),
            ApprovalEvent::ApprovalExpired(ApprovalExpired {})
        ]
    );
    assert_eq!(
        answer(&mut harness, "allow-once", ""),
        Err(ApprovalsRejection::ApprovalSettled {
            ask_id: ASK.to_string(),
            how: "expired".to_string()
        })
    );
}

#[test]
fn an_ask_is_answered_once_with_the_kind_of_its_option() {
    let mut harness = ask();
    fact(
        &mut harness,
        asked_with(300, bash()),
        "approval:r-1:toolu_1",
    );
    assert_eq!(
        answer(&mut harness, "deny", "not yet"),
        Ok(vec![ApprovalEvent::ApprovalAnswered(ApprovalAnswered {
            option: "deny".to_string(),
            kind: OptionKind::RejectOnce,
            message: "not yet".to_string(),
        })])
    );
    assert_eq!(
        answer(&mut harness, "allow-once", ""),
        Err(ApprovalsRejection::ApprovalSettled {
            ask_id: ASK.to_string(),
            how: "answered (deny)".to_string()
        })
    );
}

#[test]
fn an_answer_to_no_ask_is_refused_unknown() {
    let mut harness = ask();
    assert_eq!(
        answer(&mut harness, "allow-once", ""),
        Err(ApprovalsRejection::ApprovalUnknown {
            ask_id: ASK.to_string()
        })
    );
}

#[test]
fn an_option_the_ask_does_not_offer_is_refused_naming_those_it_does() {
    let mut harness = ask();
    fact(
        &mut harness,
        asked_with(300, bash()),
        "approval:r-1:toolu_1",
    );
    assert_eq!(
        answer(&mut harness, "deny-always", ""),
        Err(ApprovalsRejection::OptionNotOffered {
            ask_id: ASK.to_string(),
            option: "deny-always".to_string(),
            offered: "allow-once, allow-session, allow-always, deny".to_string(),
        })
    );
    assert_eq!(harness.state().state().status, crate::Status::Waiting);
}

#[test]
fn a_message_with_an_acp_answer_is_refused_and_a_claude_deny_carries_one() {
    let mut harness = ask();
    let mut told = asked_with(300, acp());
    told.engine = "opencode".to_string();
    fact(&mut harness, told, "approval:r-1:7");
    assert_eq!(
        answer(&mut harness, "deny", "not yet"),
        Err(ApprovalsRejection::MessageNotRelayed {
            ask_id: ASK.to_string(),
            engine: "opencode".to_string(),
        })
    );
    assert!(answer(&mut harness, "deny", "").is_ok());
}

#[test]
fn a_waiting_ask_is_withdrawn_and_a_settled_one_records_nothing() {
    let mut harness = ask();
    fact(
        &mut harness,
        asked_with(300, bash()),
        "approval:r-1:toolu_1",
    );
    let withdrawn = RecordApprovalWithdrawn {
        ask_id: ASK.to_string(),
        reason: "AbortError: remote-cancel".to_string(),
    };
    assert_eq!(
        fact(
            &mut harness,
            withdrawn.clone(),
            "approval-withdrawn:ask-tidy-wren"
        ),
        vec![ApprovalEvent::ApprovalWithdrawn(ApprovalWithdrawn {
            reason: "AbortError: remote-cancel".to_string()
        })]
    );
    assert_eq!(
        fact(&mut harness, withdrawn, "approval-withdrawn:again"),
        vec![]
    );
    assert_eq!(
        answer(&mut harness, "allow-once", ""),
        Err(ApprovalsRejection::ApprovalSettled {
            ask_id: ASK.to_string(),
            how: "withdrawn".to_string()
        })
    );
}

#[test]
fn a_withdrawal_of_no_ask_records_nothing() {
    let mut harness = ask();
    let withdrawn = RecordApprovalWithdrawn {
        ask_id: ASK.to_string(),
        reason: String::new(),
    };
    assert_eq!(
        fact(&mut harness, withdrawn, "approval-withdrawn:x"),
        vec![]
    );
    let _ = Any::default();
}

// Its expiry (U5, D11): the scheduler's reached alarm, re-checked against
// the ask's own state.
mod expiry {
    use angzarr_embedded::Timestamp;
    use scheduler::RecordAlarmReached;

    use super::{answer, ask, asked_with, bash, cover, fact, ASK};
    use crate::{
        expiry_alarm, ApprovalEvent, ApprovalExpired, ApprovalsRejection, RecordApprovalWithdrawn,
        Status,
    };

    fn ours() -> String {
        expiry_alarm(&[9; 16])
    }

    fn reached(alarm_id: &str, at: Timestamp) -> RecordAlarmReached {
        RecordAlarmReached {
            alarm_id: alarm_id.to_string(),
            at,
        }
    }

    #[test]
    fn an_ask_with_a_time_runs_out_at_its_recording_plus_its_time() {
        let mut harness = ask();
        fact(
            &mut harness,
            asked_with(300, bash()),
            "approval:r-1:toolu_1",
        );
        let state = harness.state().state().clone();
        assert_eq!(state.alarm_id, ours());
        assert_eq!(
            state.alarm_id,
            format!("approval-expiry:{}", uuid::Uuid::from_bytes([9; 16]))
        );
        // The harness's first sample is its start plus one.
        assert_eq!(
            state.expires,
            Some(Timestamp {
                seconds: 401,
                nanos: 0
            })
        );
    }

    #[test]
    fn an_ask_with_no_time_arms_nothing() {
        let mut harness = ask();
        fact(&mut harness, asked_with(0, bash()), "approval:r-1:toolu_1");
        let state = harness.state().state().clone();
        assert_eq!(state.alarm_id, "");
        assert_eq!(state.expires, None);
    }

    #[test]
    fn its_own_alarm_at_its_instant_expires_a_waiting_ask_once() {
        let mut harness = ask();
        fact(
            &mut harness,
            asked_with(300, bash()),
            "approval:r-1:toolu_1",
        );
        let at = harness.state().state().expires.unwrap();
        assert_eq!(
            fact(&mut harness, reached(&ours(), at), "alarm:a"),
            vec![ApprovalEvent::ApprovalExpired(ApprovalExpired {})]
        );
        assert_eq!(harness.state().state().status, Status::Expired);
        assert_eq!(fact(&mut harness, reached(&ours(), at), "alarm:b"), vec![]);
        assert_eq!(
            answer(&mut harness, "allow-once", ""),
            Err(ApprovalsRejection::ApprovalSettled {
                ask_id: ASK.to_string(),
                how: "expired".to_string()
            })
        );
    }

    #[test]
    fn another_alarm_another_instant_or_a_settled_ask_records_nothing() {
        let mut harness = ask();
        let early = Timestamp {
            seconds: 401,
            nanos: 0,
        };
        assert_eq!(
            fact(&mut harness, reached(&ours(), early), "none-asked"),
            vec![]
        );
        fact(
            &mut harness,
            asked_with(300, bash()),
            "approval:r-1:toolu_1",
        );
        let at = harness.state().state().expires.unwrap();
        assert_eq!(
            fact(&mut harness, reached("approval-expiry:other", at), "other"),
            vec![]
        );
        let before = Timestamp {
            seconds: at.seconds - 1,
            nanos: 0,
        };
        assert_eq!(
            fact(&mut harness, reached(&ours(), before), "instant"),
            vec![]
        );
        answer(&mut harness, "allow-once", "").unwrap();
        assert_eq!(fact(&mut harness, reached(&ours(), at), "answered"), vec![]);

        let mut harness = ask();
        fact(
            &mut harness,
            asked_with(300, bash()),
            "approval:r-1:toolu_1",
        );
        let at = harness.state().state().expires.unwrap();
        fact(
            &mut harness,
            RecordApprovalWithdrawn {
                ask_id: ASK.to_string(),
                reason: "steered".to_string(),
            },
            "withdrawn",
        );
        assert_eq!(
            fact(&mut harness, reached(&ours(), at), "withdrawn-reach"),
            vec![]
        );
        let _ = cover();
    }
}
