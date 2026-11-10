use std::collections::BTreeMap;

use super::*;

fn env(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

// Ruling 78: CTXLOOM_PERMIT_<NAME>=v reaches the engine as <NAME>=v, for a
// child and the primary alike.
#[test]
fn a_permit_forwards_its_value_under_the_name_it_names() {
    let caller = env(&[
        ("CTXLOOM_PERMIT_OPENCODE_API_KEY", "oc-key"),
        ("PATH", "/usr/bin"),
    ]);
    let got = permits(&caller);
    assert_eq!(got.forwarded, env(&[("OPENCODE_API_KEY", "oc-key")]));
    assert!(got.ignored.is_empty(), "{got:?}");
}

// The run's own CTXLOOM_* variables are never replaced, and neither is any
// other CTXLOOM_* name (a permit of a permit included).
#[test]
fn a_permit_of_a_ctxloom_name_is_ignored() {
    let caller = env(&[
        ("CTXLOOM_PERMIT_CTXLOOM_BEARER", "forged"),
        ("CTXLOOM_PERMIT_CTXLOOM_HOME", "/elsewhere"),
        ("CTXLOOM_PERMIT_CTXLOOM_PERMIT_X", "y"),
    ]);
    let got = permits(&caller);
    assert!(got.forwarded.is_empty(), "{got:?}");
    assert_eq!(
        got.ignored,
        vec![
            Ignored::RunsOwn {
                permit: "CTXLOOM_PERMIT_CTXLOOM_BEARER".to_string()
            },
            Ignored::RunsOwn {
                permit: "CTXLOOM_PERMIT_CTXLOOM_HOME".to_string()
            },
            Ignored::RunsOwn {
                permit: "CTXLOOM_PERMIT_CTXLOOM_PERMIT_X".to_string()
            },
        ]
    );
}

// Ruling 78a: an explicit permit brings ANTHROPIC_AUTH_TOKEN into a child
// too (the permit wins over the primary-only rule); the primary alike.
#[test]
fn a_permit_for_the_primarys_token_reaches_a_child_too() {
    let caller = env(&[("CTXLOOM_PERMIT_ANTHROPIC_AUTH_TOKEN", "t")]);
    let got = permits(&caller);
    assert_eq!(got.forwarded, env(&[("ANTHROPIC_AUTH_TOKEN", "t")]));
    assert!(got.ignored.is_empty());
}

#[test]
fn a_permit_that_names_no_variable_is_ignored() {
    let caller = env(&[("CTXLOOM_PERMIT_", "x"), ("CTXLOOM_PERMIT_1ST", "y")]);
    let got = permits(&caller);
    assert!(got.forwarded.is_empty());
    assert_eq!(
        got.ignored,
        vec![
            Ignored::NoName {
                permit: "CTXLOOM_PERMIT_".to_string()
            },
            Ignored::NoName {
                permit: "CTXLOOM_PERMIT_1ST".to_string()
            },
        ]
    );
}

// Applied to a command's environment: the permit wins over what the engine
// set, no CTXLOOM_PERMIT_* variable is left (even one the config named), and
// the run's own variables stand.
#[test]
fn applied_the_permits_set_their_names_and_leave_no_permit_behind() {
    let caller = env(&[
        ("CTXLOOM_PERMIT_OPENCODE_API_KEY", "oc-key"),
        ("CTXLOOM_PERMIT_LANG", "C"),
        ("CTXLOOM_PERMIT_CTXLOOM_BEARER", "forged"),
    ]);
    let mut command = env(&[
        ("LANG", "en_US.UTF-8"),
        ("CTXLOOM_BEARER", "run.own"),
        ("CTXLOOM_PERMIT_OPENCODE_API_KEY", "oc-key"),
    ]);
    apply_permits(&caller, &mut command);
    assert_eq!(
        command,
        env(&[
            ("CTXLOOM_BEARER", "run.own"),
            ("LANG", "C"),
            ("OPENCODE_API_KEY", "oc-key"),
        ])
    );
}

#[test]
fn no_permit_changes_nothing() {
    let mut command = env(&[("PATH", "/usr/bin")]);
    apply_permits(&env(&[("PATH", "/x")]), &mut command);
    assert_eq!(command, env(&[("PATH", "/usr/bin")]));
}

// A value is a credential as often as not: never in a Debug.
#[test]
fn a_permits_debug_names_the_variables_and_never_a_value() {
    let caller = env(&[("CTXLOOM_PERMIT_OPENCODE_API_KEY", "sk-secret-value")]);
    let shown = format!("{:?}", permits(&caller));
    assert!(shown.contains("OPENCODE_API_KEY"), "{shown}");
    assert!(!shown.contains("sk-secret-value"), "{shown}");
}
