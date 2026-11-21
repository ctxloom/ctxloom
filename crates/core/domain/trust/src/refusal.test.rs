//! A rejection and its typed refusal, both ways.

use angzarr_embedded::markup::{Failed, TypedRefusal};

use super::*;
use crate::{RevokeTrust, TrustRejection};

#[test]
fn a_stale_plan_reads_back_as_its_rejection_by_its_type() {
    let refusal = TypedRefusal::of(&PlanStale {
        seen: "e-1".to_string(),
        now: "e-2".to_string(),
    });
    assert_eq!(
        TrustRejection::try_from(&refusal),
        Ok(TrustRejection::PlanStale)
    );
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&RevokeTrust {
        name: "n".to_string(),
        project_id: "p".to_string(),
        actor: crate::Actor {
            user: "u".to_string(),
        },
    });
    assert_eq!(TrustRejection::try_from(&other), Err(NotOurs));
}

#[test]
fn a_stale_plan_refuses_under_the_status_its_message_declares() {
    match plan_stale("e-1".to_string(), "e-2".to_string()) {
        Failed::Rejected { status, .. } => {
            assert_eq!(status, <PlanStale as Refusal>::REFUSAL_STATUS);
        }
        _ => panic!("not a rejection"),
    }
}
