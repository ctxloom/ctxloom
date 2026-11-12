use crate::diagnostic_samples;

// The catalog test of the binary reads these: one value of every error the
// crate has, the token's, the engine's and the records'.
#[test]
fn the_samples_hold_a_value_of_every_error() {
    let samples = diagnostic_samples();
    let codes: Vec<&str> = samples.iter().map(|s| s.code()).collect();
    assert_eq!(samples.len(), 22, "{codes:?}");
    for prefix in [
        "run-ports-token-",
        "run-ports-endpoint-",
        "run-ports-bearer-",
        "run-ports-cert-",
        "run-ports-bind-",
        "run-ports-engine-",
        "run-ports-records-",
        "run-ports-child-",
        "run-ports-command-",
    ] {
        assert!(
            codes.iter().any(|c| c.starts_with(prefix)),
            "{prefix} in {codes:?}"
        );
    }
}
