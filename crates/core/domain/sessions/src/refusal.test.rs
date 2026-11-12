//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{Refusal, SessionAggregate, SessionRejection, SetKept, StopSession};

fn harp() -> String {
    "owl".to_string()
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
            TypedRefusal::of(&NotOwner {
                requester: "r".to_string(),
            }),
            SessionRejection::NotOwner {
                requester: "r".to_string(),
            },
        ),
        (
            TypedRefusal::of(&SessionIsClosed { harp: harp() }),
            SessionRejection::SessionClosed { harp: harp() },
        ),
        (
            TypedRefusal::of(&OwnerClosed { harp: harp() }),
            SessionRejection::OwnerClosed { harp: harp() },
        ),
        (
            TypedRefusal::of(&NoOpenIncarnation { harp: harp() }),
            SessionRejection::NoOpenIncarnation { harp: harp() },
        ),
        (
            TypedRefusal::of(&DelegationLimit { limit: 1, open: 1 }),
            SessionRejection::DelegationLimit { limit: 1, open: 1 },
        ),
        (
            TypedRefusal::of(&UntrustedTarget {
                root: "/o".to_string(),
            }),
            SessionRejection::UntrustedTarget {
                root: "/o".to_string(),
            },
        ),
        (
            TypedRefusal::of(&UrgencyUnsupported {
                urgency: "steer".to_string(),
            }),
            SessionRejection::UrgencyUnsupported {
                urgency: "steer".to_string(),
            },
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

/// What `StopSession` refuses with for `rejection`.
fn stop_refuses(rejection: SessionRejection) -> Failed<Refuses<SessionAggregate, StopSession>> {
    refuse!(
        SessionRejection,
        rejection,
        Refuses<SessionAggregate, StopSession>,
        [NoSession => NoSession, ChildCannotStop{depth} => ChildCannotStop]
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
            SessionRejection::ChildCannotStop { depth: 2 },
            <ChildCannotStop as Refusal>::REFUSAL_STATUS,
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
        } = stop_refuses(rejection)
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
        stop_refuses(SessionRejection::NoClaim),
        Failed::Bug(_)
    ));
}
