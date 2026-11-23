//! The exit ladder (D28, `exit_codes_test.go`; rulings 149, 152, 153): 0, 32
//! failed, 48 for a refusal, 49 for fatal findings; reached through the
//! test-only commands.

use super::run;

#[test]
fn test_the_exit_ladder() {
    for (args, code) in [
        (&["test", "sample"][..], 0),
        (&["test", "refuse"], 48),
        (&["test", "findings"], 49),
    ] {
        let ran = run(args);
        assert_eq!(ran.code, code, "{args:?}: {}", ran.said());
    }
}

#[test]
fn test_a_refusal_is_said_on_stderr_and_answers_nothing() {
    // A refusal is an error: it is said on stderr, and stdout holds answers only.
    let ran = run(&["--format", "text", "test", "refuse"]);
    assert_eq!(ran.code, 48, "{}", ran.said());
    assert_eq!(ran.stdout, "", "{}", ran.said());
    assert_ne!(ran.stderr, "", "{}", ran.said());
}

#[test]
fn test_a_usage_error_is_a_refusal_too() {
    for args in [&["frobnicate"][..], &["--format", "yaml", "version"]] {
        let ran = run(args);
        assert_eq!(ran.code, 48, "{args:?}: {}", ran.said());
    }
}
