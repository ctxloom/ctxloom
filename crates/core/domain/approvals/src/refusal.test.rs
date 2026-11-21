use angzarr_embedded::markup::{Failed, TypedRefusal};

use super::*;

#[test]
fn each_refusal_reads_back_as_its_rejection_with_its_fields() {
    let settled = TypedRefusal::of(&ApprovalIsSettled {
        ask_id: "a".to_string(),
        how: "expired".to_string(),
        option: String::new(),
    });
    assert_eq!(
        ApprovalsRejection::try_from(&settled),
        Ok(ApprovalsRejection::ApprovalSettled {
            ask_id: "a".to_string(),
            how: "expired".to_string()
        })
    );
    let unknown = TypedRefusal::of(&ApprovalUnknown {
        ask_id: "a".to_string(),
    });
    assert_eq!(
        ApprovalsRejection::try_from(&unknown),
        Ok(ApprovalsRejection::ApprovalUnknown {
            ask_id: "a".to_string()
        })
    );
    let relayed = TypedRefusal::of(&MessageNotRelayed {
        ask_id: "a".to_string(),
        engine: "opencode".to_string(),
    });
    assert_eq!(
        ApprovalsRejection::try_from(&relayed),
        Ok(ApprovalsRejection::MessageNotRelayed {
            ask_id: "a".to_string(),
            engine: "opencode".to_string()
        })
    );
    let taken = TypedRefusal::of(&ApprovalNameTaken {
        ask_id: "a".to_string(),
    });
    assert_eq!(
        ApprovalsRejection::try_from(&taken),
        Ok(ApprovalsRejection::NameTaken {
            ask_id: "a".to_string()
        })
    );
}

#[test]
fn a_message_that_is_no_refusal_of_ours_is_none_of_ours() {
    let other = TypedRefusal::of(&crate::RecordApprovalWithdrawn {
        ask_id: "a".to_string(),
        reason: String::new(),
    });
    assert_eq!(ApprovalsRejection::try_from(&other), Err(NotOurs));
}

#[test]
fn each_answer_refusal_is_under_the_status_its_message_declares() {
    let status = |rejection| match answer_refused(rejection) {
        Failed::Rejected { status, .. } => status,
        _ => panic!("not a rejection"),
    };
    let a = || "a".to_string();
    assert_eq!(
        status(ApprovalsRejection::ApprovalUnknown { ask_id: a() }),
        <ApprovalUnknown as Refusal>::REFUSAL_STATUS
    );
    assert_eq!(
        status(ApprovalsRejection::ApprovalSettled {
            ask_id: a(),
            how: a()
        }),
        <ApprovalIsSettled as Refusal>::REFUSAL_STATUS
    );
    assert_eq!(
        status(ApprovalsRejection::OptionNotOffered {
            ask_id: a(),
            option: a(),
            offered: a()
        }),
        <OptionNotOffered as Refusal>::REFUSAL_STATUS
    );
    assert_eq!(
        status(ApprovalsRejection::MessageNotRelayed {
            ask_id: a(),
            engine: a()
        }),
        <MessageNotRelayed as Refusal>::REFUSAL_STATUS
    );
    assert!(matches!(
        answer_refused(ApprovalsRejection::NameTaken { ask_id: a() }),
        Failed::Bug(_)
    ));
    match name_taken(a()) {
        Failed::Rejected { status, .. } => {
            assert_eq!(status, <ApprovalNameTaken as Refusal>::REFUSAL_STATUS)
        }
        _ => panic!("not a rejection"),
    }
}
