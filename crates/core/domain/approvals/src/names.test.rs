use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Failure, Registry};

use crate::{
    ApprovalNameAggregate, ApprovalNameClaimed, ApprovalNameCommand, ApprovalNameEvent,
    ApprovalsRejection, ClaimApprovalName, APPROVAL_NAMES_DOMAIN,
};

fn claim() -> Harness<ApprovalNameAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<ApprovalNameAggregate>(ApprovalNameAggregate::default)
        .unwrap();
    let cover = Cover {
        domain: APPROVAL_NAMES_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([3; 16])),
        edition: None,
    };
    Harness::new(registry, cover).with_samples(Samples::starting_at(100))
}

fn claiming(approval: &str) -> ApprovalNameCommand {
    ClaimApprovalName {
        project_id: "p-1".to_string(),
        ask_id: "ask-tidy-wren".to_string(),
        approval: approval.to_string(),
    }
    .into()
}

fn run(
    harness: &mut Harness<ApprovalNameAggregate>,
    command: ApprovalNameCommand,
) -> Result<Vec<ApprovalNameEvent>, ApprovalsRejection> {
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

#[test]
fn a_free_name_is_claimed_its_holder_again_records_nothing_and_another_is_refused() {
    let mut harness = claim();
    assert_eq!(
        run(&mut harness, claiming("a-1")),
        Ok(vec![ApprovalNameEvent::ApprovalNameClaimed(
            ApprovalNameClaimed {
                project_id: "p-1".to_string(),
                ask_id: "ask-tidy-wren".to_string(),
                approval: "a-1".to_string(),
            }
        )])
    );
    assert_eq!(run(&mut harness, claiming("a-1")), Ok(vec![]));
    assert_eq!(
        run(&mut harness, claiming("a-2")),
        Err(ApprovalsRejection::NameTaken {
            ask_id: "ask-tidy-wren".to_string()
        })
    );
    assert_eq!(harness.state().state().approval, "a-1");
}
