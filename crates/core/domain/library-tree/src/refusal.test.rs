//! A rejection and its typed refusal, both ways.

use std::collections::BTreeMap;

use angzarr_embedded::markup::TypedRefusal;

use super::*;
use crate::{ChangeFailed, LibraryTreeRejection};

/// One refusal message of every rejection, with the rejection it must read
/// back as: the table of the domain.
fn table() -> Vec<(TypedRefusal, LibraryTreeRejection)> {
    let path = || "content/bundles/v2/demo/bundle.yaml".to_string();
    let args = BTreeMap::from([("name".to_string(), "demo".to_string())]);
    vec![
        (
            TypedRefusal::of(&PlanStale {
                path: path(),
                seen: 3,
                now: 5,
            }),
            LibraryTreeRejection::PlanStale {
                path: path(),
                seen: 3,
                now: 5,
            },
        ),
        (
            TypedRefusal::of(&ResourceExists { path: path() }),
            LibraryTreeRejection::ResourceExists { path: path() },
        ),
        (
            TypedRefusal::of(&NoSuchResource { path: path() }),
            LibraryTreeRejection::NoSuchResource { path: path() },
        ),
        (
            TypedRefusal::of(&BuiltinReadOnly {
                bundle: "ctxloom".to_string(),
            }),
            LibraryTreeRejection::BuiltinReadOnly {
                bundle: "ctxloom".to_string(),
            },
        ),
        (
            TypedRefusal::of(&PlanRejected {
                code: "library-application-x".to_string(),
                args: args.clone(),
            }),
            LibraryTreeRejection::PlanRejected {
                code: "library-application-x".to_string(),
                args,
            },
        ),
    ]
}

#[test]
fn every_refusal_reads_back_as_its_rejection_with_its_fields() {
    for (refusal, rejection) in table() {
        assert_eq!(LibraryTreeRejection::try_from(&refusal), Ok(rejection));
    }
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&ChangeFailed {
        request_id: "r".to_string(),
        step: 0,
        ext_err_txt: "x".to_string(),
    });
    assert_eq!(LibraryTreeRejection::try_from(&other), Err(NotOurs));
}

// OQ-10 (owner, 2026-11-13): a stale plan is ABORTED (AIP-154), now that
// angzarr core declares it (b1445adf); every other state refusal of the
// tree stays a failed precondition. Compared, never spelled.
#[test]
fn test_a_stale_plan_is_refused_under_a_status_of_its_own() {
    let precondition = <BuiltinReadOnly as crate::Refusal>::REFUSAL_STATUS;
    assert_eq!(
        <PlanRejected as crate::Refusal>::REFUSAL_STATUS,
        precondition
    );
    assert_ne!(<PlanStale as crate::Refusal>::REFUSAL_STATUS, precondition);
    assert_ne!(
        <PlanStale as crate::Refusal>::REFUSAL_STATUS,
        <ResourceExists as crate::Refusal>::REFUSAL_STATUS
    );
    assert_ne!(
        <PlanStale as crate::Refusal>::REFUSAL_STATUS,
        <OneChangePerCall as crate::Refusal>::REFUSAL_STATUS
    );
}
