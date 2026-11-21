use crate::{diagnostic_samples, APPROVALS_DOMAIN, APPROVAL_NAMES_DOMAIN, EN_US};

#[test]
fn the_domains_are_approvals_and_approval_names() {
    assert_eq!(APPROVALS_DOMAIN, "approvals");
    assert_eq!(APPROVAL_NAMES_DOMAIN, "approval-names");
}

#[test]
fn every_diagnostic_has_a_message_and_its_code_is_prefixed() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "approvals");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("approvals-"), "{code} is prefixed");
        assert!(
            catalog.contains(&format!("{code} =")),
            "{code} has no message"
        );
    }
    assert_eq!(diagnostic_samples().len(), 5);
}

// A rejection displays itself as every error does: its code, then its args.
#[test]
fn a_rejection_says_its_message_with_its_args() {
    let settled = crate::ApprovalsRejection::ApprovalSettled {
        ask_id: "ask-tidy-wren".to_string(),
        how: "expired".to_string(),
    };
    assert_eq!(
        settled.to_string(),
        "approvals-approval-settled ask_id=ask-tidy-wren how=expired"
    );
}
