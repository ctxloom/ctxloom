//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{ArmAlarm, CancelAlarm, Refusal, SchedulerAggregate, SchedulerRejection};

fn table() -> Vec<(TypedRefusal, SchedulerRejection)> {
    vec![
        (
            TypedRefusal::of(&AlarmIdEmpty {}),
            SchedulerRejection::AlarmIdEmpty,
        ),
        (
            TypedRefusal::of(&TargetMismatch {
                stream: "scheduler/s".to_string(),
                target: "toy/t".to_string(),
            }),
            SchedulerRejection::TargetMismatch {
                stream: "scheduler/s".to_string(),
                target: "toy/t".to_string(),
            },
        ),
    ]
}

#[test]
fn every_refusal_reads_back_as_its_rejection_by_its_type() {
    for (refusal, rejection) in table() {
        assert_eq!(SchedulerRejection::try_from(&refusal), Ok(rejection));
    }
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&CancelAlarm {
        alarm_id: "a".to_string(),
    });
    assert_eq!(SchedulerRejection::try_from(&other), Err(NotOurs));
}

fn arm_refuses(rejection: SchedulerRejection) -> Failed<Refuses<SchedulerAggregate, ArmAlarm>> {
    refuse!(
        SchedulerRejection,
        rejection,
        Refuses<SchedulerAggregate, ArmAlarm>,
        [AlarmIdEmpty => AlarmIdEmpty, TargetMismatch{stream, target} => TargetMismatch]
    )
}

#[test]
fn a_handler_refuses_with_the_message_of_the_rejection_under_its_declared_status() {
    for (message, rejection) in table() {
        let declared = match rejection {
            SchedulerRejection::AlarmIdEmpty => <AlarmIdEmpty as Refusal>::REFUSAL_STATUS,
            SchedulerRejection::TargetMismatch { .. } => {
                <TargetMismatch as Refusal>::REFUSAL_STATUS
            }
        };
        let Failed::Rejected {
            rejection: choice,
            status,
        } = arm_refuses(rejection)
        else {
            panic!("a refusal");
        };
        assert_eq!(TypedRefusal::from(choice), message);
        assert_eq!(status, declared);
    }
}
