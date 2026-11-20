//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use super::*;
use crate::{FoundTree, OccupyTree, Refusal, TreeAggregate, TreeRejection};

fn table() -> Vec<(TypedRefusal, TreeRejection)> {
    vec![
        (
            TypedRefusal::of(&AlreadyFounded {}),
            TreeRejection::AlreadyFounded,
        ),
        (TypedRefusal::of(&NoTree {}), TreeRejection::NoTree),
    ]
}

#[test]
fn every_refusal_reads_back_as_its_rejection_by_its_type() {
    for (refusal, rejection) in table() {
        assert_eq!(TreeRejection::try_from(&refusal), Ok(rejection));
    }
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&OccupyTree {
        session: "s".to_string(),
    });
    assert_eq!(TreeRejection::try_from(&other), Err(NotOurs));
}

#[test]
fn a_handler_refuses_with_the_message_of_the_rejection_under_its_declared_status() {
    let found: Failed<Refuses<TreeAggregate, FoundTree>> = refuse!(
        TreeRejection,
        TreeRejection::AlreadyFounded,
        Refuses<TreeAggregate, FoundTree>,
        [AlreadyFounded => AlreadyFounded]
    );
    let Failed::Rejected { rejection, status } = found else {
        panic!("a refusal");
    };
    assert_eq!(
        TypedRefusal::from(rejection),
        TypedRefusal::of(&AlreadyFounded {})
    );
    assert_eq!(status, <AlreadyFounded as Refusal>::REFUSAL_STATUS);

    let occupy: Failed<Refuses<TreeAggregate, OccupyTree>> = refuse!(
        TreeRejection,
        TreeRejection::NoTree,
        Refuses<TreeAggregate, OccupyTree>,
        [NoTree => NoTree]
    );
    let Failed::Rejected { rejection, status } = occupy else {
        panic!("a refusal");
    };
    assert_eq!(TypedRefusal::from(rejection), TypedRefusal::of(&NoTree {}));
    assert_eq!(status, <NoTree as Refusal>::REFUSAL_STATUS);
}

#[test]
fn a_rejection_the_command_lists_no_message_for_is_the_handlers_bug() {
    let found: Failed<Refuses<TreeAggregate, FoundTree>> = refuse!(
        TreeRejection,
        TreeRejection::NoTree,
        Refuses<TreeAggregate, FoundTree>,
        [AlreadyFounded => AlreadyFounded]
    );
    assert!(matches!(found, Failed::Bug(_)));
}
