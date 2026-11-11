//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{
    EnqueueRun, LaunchRefused, NoPrompt, QueueTurn, Refusal, RequestStop, RunAggregate,
    RunRejection, UnenforcedPosture, UnknownEngine,
};

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
        (
            TypedRefusal::of(&FileOnlyCredential {
                engine: "claude-code".to_string(),
                variables: "A, B".to_string(),
            }),
            RunRejection::FileOnlyCredential {
                engine: "claude-code".to_string(),
                variables: "A, B".to_string(),
            },
        ),
        (TypedRefusal::of(&NoPrompt {}), RunRejection::NoPrompt),
        (
            TypedRefusal::of(&UnknownEngine {
                name: "bogus".to_string(),
                known: "claude-code".to_string(),
            }),
            RunRejection::UnknownEngine {
                name: "bogus".to_string(),
                known: "claude-code".to_string(),
            },
        ),
        (
            TypedRefusal::of(&LaunchRefused {
                no_prompt: vec![NoPrompt {}],
                unknown_engine: vec![UnknownEngine {
                    name: "bogus".to_string(),
                    known: "claude-code".to_string(),
                }],
                no_credential: Vec::new(),
                file_only_credential: Vec::new(),
                fatal_findings: 1,
                unenforced_posture: Vec::new(),
            }),
            RunRejection::LaunchRefused {
                refusals: vec![
                    RunRejection::NoPrompt,
                    RunRejection::UnknownEngine {
                        name: "bogus".to_string(),
                        known: "claude-code".to_string(),
                    },
                ],
                fatal_findings: 1,
            },
        ),
    ]
    .into_iter()
    .chain(posture_table())
    .collect()
}

/// The posture's row of the table (owner ruling 109).
fn posture_table() -> Vec<(TypedRefusal, RunRejection)> {
    vec![
        (
            TypedRefusal::of(&LaunchRefused {
                no_prompt: Vec::new(),
                unknown_engine: Vec::new(),
                no_credential: vec![NoCredential {
                    engine: "claude-code".to_string(),
                    variables: "A, B".to_string(),
                }],
                file_only_credential: Vec::new(),
                fatal_findings: 0,
                unenforced_posture: vec![UnenforcedPosture {
                    runtime: "host".to_string(),
                    key: "sandbox".to_string(),
                    value: "read-only".to_string(),
                    honest: "full".to_string(),
                }],
            }),
            RunRejection::LaunchRefused {
                refusals: vec![
                    RunRejection::NoCredential {
                        engine: "claude-code".to_string(),
                        variables: "A, B".to_string(),
                    },
                    unenforced_sandbox(),
                ],
                fatal_findings: 0,
            },
        ),
        (
            TypedRefusal::of(&UnenforcedPosture {
                runtime: "host".to_string(),
                key: "sandbox".to_string(),
                value: "read-only".to_string(),
                honest: "full".to_string(),
            }),
            unenforced_sandbox(),
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

/// What `EnqueueRun` refuses with for `rejection`.
fn enqueue_refuses(rejection: RunRejection) -> Failed<Refuses<RunAggregate, EnqueueRun>> {
    crate::aggregate::enqueue_refusal(rejection)
}

// Ruling 73c: no prompt and an unknown engine are bad arguments; the aggregate
// is a precondition the run failed. Each status is its message's option,
// compared here with another message's of the same meaning, never spelled.
#[test]
fn the_enqueue_refuses_each_pre_launch_check_with_its_message_under_its_status() {
    let invalid = <CredentialHashInvalid as Refusal>::REFUSAL_STATUS;
    let precondition = <NoCredential as Refusal>::REFUSAL_STATUS;
    assert_eq!(<NoPrompt as Refusal>::REFUSAL_STATUS, invalid);
    assert_eq!(<UnknownEngine as Refusal>::REFUSAL_STATUS, invalid);
    assert_eq!(<LaunchRefused as Refusal>::REFUSAL_STATUS, precondition);
    let aggregate = RunRejection::LaunchRefused {
        refusals: vec![
            RunRejection::NoPrompt,
            RunRejection::FileOnlyCredential {
                engine: "e".to_string(),
                variables: "V".to_string(),
            },
        ],
        fatal_findings: 0,
    };
    let cases = [
        (RunRejection::NoPrompt, invalid),
        (
            RunRejection::UnknownEngine {
                name: "n".to_string(),
                known: "k".to_string(),
            },
            invalid,
        ),
        (
            RunRejection::NoCredential {
                engine: "e".to_string(),
                variables: "V".to_string(),
            },
            precondition,
        ),
        (
            RunRejection::FileOnlyCredential {
                engine: "e".to_string(),
                variables: "V".to_string(),
            },
            precondition,
        ),
        (aggregate, precondition),
    ];
    for (rejection, declared) in cases {
        let Failed::Rejected {
            rejection: choice,
            status,
        } = enqueue_refuses(rejection.clone())
        else {
            panic!("a refusal of {rejection:?}");
        };
        assert_eq!(status, declared, "{rejection:?}");
        assert_eq!(
            RunRejection::try_from(&TypedRefusal::from(choice)),
            Ok(rejection)
        );
    }
}

fn unenforced_sandbox() -> RunRejection {
    RunRejection::UnenforcedPosture {
        runtime: "host".to_string(),
        key: "sandbox".to_string(),
        value: "read-only".to_string(),
        honest: "full".to_string(),
    }
}
