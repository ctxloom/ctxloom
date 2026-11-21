//! The wire messages carry the names the kernel routes by: a stored event's
//! type URL must never change.

use super::v1;
use prost::Name;

#[test]
fn every_message_is_in_the_approvals_package() {
    for url in [
        v1::RecordApprovalAsked::type_url(),
        v1::RecordApprovalWithdrawn::type_url(),
        v1::AnswerApproval::type_url(),
        v1::ApprovalAsked::type_url(),
        v1::ApprovalAnswered::type_url(),
        v1::ApprovalExpired::type_url(),
        v1::ApprovalWithdrawn::type_url(),
        v1::ApprovalUnknown::type_url(),
        v1::ApprovalIsSettled::type_url(),
        v1::OptionNotOffered::type_url(),
        v1::MessageNotRelayed::type_url(),
        v1::ApprovalState::type_url(),
        v1::ClaimApprovalName::type_url(),
        v1::ApprovalNameClaimed::type_url(),
        v1::ApprovalNameTaken::type_url(),
        v1::ApprovalNameState::type_url(),
    ] {
        assert!(url.starts_with("/ctxloom.approvals.v1."), "{url}");
    }
}
