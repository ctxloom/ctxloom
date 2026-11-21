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
            "daemon-api-failed",
            "daemon-api-refused",
            "daemon-api-name-invalid",
            "daemon-api-session-not-found",
            "daemon-api-project-not-found",
            "daemon-api-run-not-found",
            "daemon-api-unimplemented",
            "daemon-api-transcript-empty",
            "daemon-api-project-unknown",
            "daemon-api-daemon-hosting",
            "daemon-api-draining",
            "daemon-api-client-version",
            "daemon-api-home-gone",
            "daemon-api-age-invalid",
            "daemon-api-etag-invalid",
            "daemon-api-plan-stale",
            "daemon-api-needs-unanswered",
            "daemon-api-config-unreadable",
            "daemon-api-launch-in-progress",
            "daemon-api-wake-inform-only",
            "daemon-api-primary-not-hosted",
            "daemon-api-session-not-hosted",
            "daemon-api-approval-ambiguous",
            "daemon-api-approval-none-waiting",
            "daemon-api-approval-rule-kept",
            "daemon-api-version-skew",
            "daemon-api-launch-lock",
            "daemon-api-start-up-incomplete",
            "daemon-api-handing-over",
        ]
    );
}
