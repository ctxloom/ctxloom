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
        ["daemon-api-start-up-incomplete", "daemon-api-handing-over",]
    );
}
