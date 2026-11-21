use crate::{diagnostic_samples, TrustRejection, EN_US, TRUST_DOMAIN};

#[test]
fn the_domain_name_is_trust() {
    assert_eq!(TRUST_DOMAIN, "trust");
}

#[test]
fn every_diagnostic_has_a_message_and_its_code_is_prefixed() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "trust");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("trust-"), "{code} is prefixed");
        assert!(
            catalog.contains(&format!("{code} =")),
            "{code} has no message"
        );
    }
}

#[test]
fn rejections_display_their_code_and_the_samples_are_not_empty() {
    assert_eq!(TrustRejection::PlanStale.to_string(), "trust-plan-stale");
    assert_eq!(diagnostic_samples().len(), 1);
}
