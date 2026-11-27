// The catalog test of the binary reads these: one value of every diagnostic
// of the crate, each this crate's code.
#[test]
fn the_samples_hold_every_diagnostic_of_the_crate() {
    let codes: Vec<String> = crate::diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    assert_eq!(
        codes,
        [
            "daemon-api-contract-failed",
            "daemon-api-contract-refused",
            "daemon-api-contract-name-invalid",
            "daemon-api-contract-session-not-found",
            "daemon-api-contract-project-not-found",
            "daemon-api-contract-per-project-store",
            "daemon-api-contract-run-not-found",
            "daemon-api-contract-unimplemented",
            "daemon-api-contract-transcript-empty",
            "daemon-api-contract-project-unknown",
            "daemon-api-contract-daemon-hosting",
            "daemon-api-contract-draining",
            "daemon-api-contract-client-version",
            "daemon-api-contract-home-gone",
            "daemon-api-contract-age-invalid",
            "daemon-api-contract-etag-invalid",
            "daemon-api-contract-plan-stale",
            "daemon-api-contract-needs-unanswered",
            "daemon-api-contract-config-unreadable",
            "daemon-api-contract-launch-in-progress",
            "daemon-api-contract-wake-inform-only",
            "daemon-api-contract-primary-not-hosted",
            "daemon-api-contract-run-ended",
            "daemon-api-contract-session-not-hosted",
            "daemon-api-contract-approval-ambiguous",
            "daemon-api-contract-approval-none-waiting",
            "daemon-api-contract-approval-rule-kept",
        ]
    );
}
