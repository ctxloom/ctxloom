//! The exit ladder (D28, `exit_codes_test.go`; rulings 149, 152, 153): 0, 10
//! failed, 20 for a refusal, 21 for fatal findings; reached through the
//! test-only commands.

use super::run;

#[test]
fn test_the_exit_ladder() {
    for (args, code) in [
        (&["test", "sample"][..], 0),
        (&["test", "refuse"], 20),
        (&["test", "findings"], 21),
    ] {
        let ran = run(args);
        assert_eq!(ran.code, code, "{args:?}: {}", ran.said());
    }
}

#[test]
fn test_a_refusal_is_said_on_stderr_and_answers_nothing() {
    // A refusal is an error: it is said on stderr, and stdout holds answers only.
    let ran = run(&["--format", "text", "test", "refuse"]);
    assert_eq!(ran.code, 20, "{}", ran.said());
    assert_eq!(ran.stdout, "", "{}", ran.said());
    assert_ne!(ran.stderr, "", "{}", ran.said());
}

#[test]
fn test_a_usage_error_is_a_refusal_too() {
    for args in [&["frobnicate"][..], &["--format", "yaml", "version"]] {
        let ran = run(args);
        assert_eq!(ran.code, 20, "{args:?}: {}", ran.said());
    }
}
