//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{Refusal, SessionAggregate, SessionRejection, SetKept, WithdrawSteer};

fn harp() -> String {
    "owl".to_string()
}

fn id() -> String {
    "m1".to_string()
}

/// One refusal message of every rejection, with the rejection it must read
/// back as: the table of the domain.
fn table() -> Vec<(TypedRefusal, SessionRejection)> {
    vec![
        (
            TypedRefusal::of(&SessionExists { harp: harp() }),
            SessionRejection::SessionExists { harp: harp() },
        ),
        (TypedRefusal::of(&NoSession {}), SessionRejection::NoSession),
        (
            TypedRefusal::of(&HarpInvalid {
                harp: harp(),
                reason: "empty".to_string(),
            }),
            SessionRejection::HarpInvalid {
                harp: harp(),
                reason: "empty".to_string(),
            },
        ),
        (
            TypedRefusal::of(&HarpTooLong {
                harp: harp(),
                limit: 24,
                length: 30,
            }),
            SessionRejection::HarpTooLong {
                harp: harp(),
                limit: 24,
                length: 30,
            },
        ),
        (
            TypedRefusal::of(&NamesTaken { harp: harp() }),
            SessionRejection::NamesTaken { harp: harp() },
        ),
        (TypedRefusal::of(&NoClaim {}), SessionRejection::NoClaim),
        (
            TypedRefusal::of(&NotHolder { harp: harp() }),
            SessionRejection::NotHolder { harp: harp() },
        ),
        (
            TypedRefusal::of(&ChildCannotStop { depth: 2 }),
            SessionRejection::ChildCannotStop { depth: 2 },
        ),
        (
            TypedRefusal::of(&UnknownMessage { message_id: id() }),
            SessionRejection::UnknownMessage { message_id: id() },
        ),
        (
            TypedRefusal::of(&AlreadyDelivered { message_id: id() }),
            SessionRejection::AlreadyDelivered { message_id: id() },
        ),
    ]
}

#[test]
fn every_refusal_reads_back_as_its_rejection_by_its_type() {
    for (refusal, rejection) in table() {
        assert_eq!(SessionRejection::try_from(&refusal), Ok(rejection));
    }
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&SetKept { kept: true });
    assert_eq!(SessionRejection::try_from(&other), Err(NotOurs));
}

/// What `WithdrawSteer` refuses with for `rejection`.
fn withdraw_refuses(
    rejection: SessionRejection,
) -> Failed<Refuses<SessionAggregate, WithdrawSteer>> {
    refuse!(
        SessionRejection,
        rejection,
        Refuses<SessionAggregate, WithdrawSteer>,
        [NoSession => NoSession, UnknownMessage{message_id} => UnknownMessage, AlreadyDelivered{message_id} => AlreadyDelivered]
    )
}

#[test]
fn a_handler_refuses_with_the_message_of_the_rejection_under_its_declared_status() {
    let cases = [
        (
            SessionRejection::NoSession,
            <NoSession as Refusal>::REFUSAL_STATUS,
        ),
        (
            SessionRejection::UnknownMessage { message_id: id() },
            <UnknownMessage as Refusal>::REFUSAL_STATUS,
        ),
        (
            SessionRejection::AlreadyDelivered { message_id: id() },
            <AlreadyDelivered as Refusal>::REFUSAL_STATUS,
        ),
    ];
    for (rejection, declared) in cases {
        let expected = table()
            .into_iter()
            .find(|(_, r)| *r == rejection)
            .map(|(message, _)| message)
            .expect("in the table");
        let Failed::Rejected {
            rejection: choice,
            status,
        } = withdraw_refuses(rejection)
        else {
            panic!("a refusal");
        };
        assert_eq!(TypedRefusal::from(choice), expected);
        assert_eq!(status, declared);
    }
}

#[test]
fn a_rejection_the_command_lists_no_message_for_is_the_handlers_bug() {
    assert!(matches!(
        withdraw_refuses(SessionRejection::NoClaim),
        Failed::Bug(_)
    ));
}
