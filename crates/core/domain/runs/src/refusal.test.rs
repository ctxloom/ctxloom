//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{QueueTurn, Refusal, RequestStop, RunAggregate, RunRejection};

/// One refusal message of every rejection, with the rejection it must read
/// back as: the table of the domain.
fn table() -> Vec<(TypedRefusal, RunRejection)> {
    vec![
        (
            TypedRefusal::of(&AlreadyEnqueued {}),
            RunRejection::AlreadyEnqueued,
        ),
        (TypedRefusal::of(&NoRun {}), RunRejection::NoRun),
        (
            TypedRefusal::of(&TurnInFlight { turn: 7 }),
            RunRejection::TurnInFlight { turn: 7 },
        ),
        (TypedRefusal::of(&Ended {}), RunRejection::Ended),
        (
            TypedRefusal::of(&CredentialHashInvalid { length: 5 }),
            RunRejection::CredentialHashInvalid { length: 5 },
        ),
        (
            TypedRefusal::of(&NotItsStream {}),
            RunRejection::NotItsStream,
        ),
        (
            TypedRefusal::of(&CredentialsNotDistinct {}),
            RunRejection::CredentialsNotDistinct,
        ),
        (
            TypedRefusal::of(&NoCredential {
                engine: "claude-code".to_string(),
                variables: "A, B".to_string(),
            }),
            RunRejection::NoCredential {
                engine: "claude-code".to_string(),
                variables: "A, B".to_string(),
            },
        ),
    ]
}

#[test]
fn every_refusal_reads_back_as_its_rejection_by_its_type() {
    for (refusal, rejection) in table() {
        assert_eq!(RunRejection::try_from(&refusal), Ok(rejection));
    }
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&RequestStop {
        reason: String::new(),
    });
    assert_eq!(RunRejection::try_from(&other), Err(NotOurs));
}

/// What `QueueTurn` refuses with for `rejection`.
fn queue_turn_refuses(rejection: RunRejection) -> Failed<Refuses<RunAggregate, QueueTurn>> {
    refuse!(
        RunRejection,
        rejection,
        Refuses<RunAggregate, QueueTurn>,
        [NoRun => NoRun, Ended => Ended, TurnInFlight{turn} => TurnInFlight]
    )
}

#[test]
fn a_handler_refuses_with_the_message_of_the_rejection_under_its_declared_status() {
    let cases = [
        (
            RunRejection::NoRun,
            TypedRefusal::of(&NoRun {}),
            <NoRun as Refusal>::REFUSAL_STATUS,
        ),
        (
            RunRejection::Ended,
            TypedRefusal::of(&Ended {}),
            <Ended as Refusal>::REFUSAL_STATUS,
        ),
        (
            RunRejection::TurnInFlight { turn: 3 },
            TypedRefusal::of(&TurnInFlight { turn: 3 }),
            <TurnInFlight as Refusal>::REFUSAL_STATUS,
        ),
    ];
    for (rejection, message, declared) in cases {
        let Failed::Rejected {
            rejection: choice,
            status,
        } = queue_turn_refuses(rejection)
        else {
            panic!("a refusal");
        };
        assert_eq!(TypedRefusal::from(choice), message);
        assert_eq!(status, declared);
    }
}

#[test]
fn a_rejection_the_command_lists_no_message_for_is_the_handlers_bug() {
    assert!(matches!(
        queue_turn_refuses(RunRejection::AlreadyEnqueued),
        Failed::Bug(_)
    ));
}
