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
            "daemon-api-transcript-empty",
            "daemon-api-project-unknown",
            "daemon-api-daemon-hosting",
            "daemon-api-age-invalid",
            "daemon-api-config-unreadable",
            "daemon-api-version-skew-busy",
            "daemon-api-version-skew",
        ]
    );
}
