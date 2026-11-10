use std::collections::{BTreeMap, BTreeSet};

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
    let got = permits(&caller, &BTreeSet::new());
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
    let got = permits(&caller, &BTreeSet::new());
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
    let got = permits(&caller, &BTreeSet::new());
    assert_eq!(got.forwarded, env(&[("ANTHROPIC_AUTH_TOKEN", "t")]));
    assert!(got.ignored.is_empty());
}

#[test]
fn a_permit_that_names_no_variable_is_ignored() {
    let caller = env(&[("CTXLOOM_PERMIT_", "x"), ("CTXLOOM_PERMIT_1ST", "y")]);
    let got = permits(&caller, &BTreeSet::new());
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
        ("LANG", "en_US.UTF-8"),
        ("CTXLOOM_BEARER", "run.own"),
    ]);
    let mut command = env(&[
        ("LANG", "en_US.UTF-8"),
        ("CTXLOOM_BEARER", "run.own"),
        ("CTXLOOM_PERMIT_OPENCODE_API_KEY", "oc-key"),
    ]);
    let ignored = apply_permits(&caller, &mut command, &[]);
    assert_eq!(ignored.len(), 1);
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
    assert!(apply_permits(&env(&[("PATH", "/x")]), &mut command, &[]).is_empty());
    assert_eq!(command, env(&[("PATH", "/usr/bin")]));
}

// A value is a credential as often as not: never in a Debug.
#[test]
fn a_permits_debug_names_the_variables_and_never_a_value() {
    let caller = env(&[("CTXLOOM_PERMIT_OPENCODE_API_KEY", "sk-secret-value")]);
    let shown = format!("{:?}", permits(&caller, &BTreeSet::new()));
    assert!(shown.contains("OPENCODE_API_KEY"), "{shown}");
    assert!(!shown.contains("sk-secret-value"), "{shown}");
}

// Ruling 78b: a permit never replaces a name ctxloom set for the engine. The
// names are derived from what was set: a value the caller did not give (the
// adapter computed it), and the run's own (`also`); a name passed through
// as the caller gave it stays permittable.
#[test]
fn a_permit_never_replaces_what_ctxloom_set_for_the_engine() {
    let caller = env(&[
        ("HOME", "/home/alice"),
        ("PATH", "/ctxloom/bin:/usr/bin"),
        ("LANG", "en_US.UTF-8"),
        ("CTXLOOM_PERMIT_HOME", "/elsewhere"),
        ("CTXLOOM_PERMIT_XDG_DATA_HOME", "/elsewhere/data"),
        ("CTXLOOM_PERMIT_OPENCODE_CONFIG_DIR", "/elsewhere/oc"),
        ("CTXLOOM_PERMIT_PATH", "/evil"),
        ("CTXLOOM_PERMIT_LANG", "C"),
        ("CTXLOOM_PERMIT_OPENCODE_API_KEY", "oc-key"),
    ]);
    // What an adapter built: HOME and the XDG base and config dir relocated,
    // PATH and LANG passed through.
    let mut command = env(&[
        ("HOME", "/s/home"),
        ("XDG_DATA_HOME", "/s/home/xdg/data"),
        ("OPENCODE_CONFIG_DIR", "/s/home"),
        ("PATH", "/ctxloom/bin:/usr/bin"),
        ("LANG", "en_US.UTF-8"),
    ]);
    let owned = owned_names(&caller, &command, &["PATH".to_string()]);
    assert_eq!(
        owned.iter().map(String::as_str).collect::<Vec<_>>(),
        ["HOME", "OPENCODE_CONFIG_DIR", "PATH", "XDG_DATA_HOME"]
    );
    let ignored = apply_permits(&caller, &mut command, &["PATH".to_string()]);
    assert_eq!(
        command,
        env(&[
            ("HOME", "/s/home"),
            ("LANG", "C"),
            ("OPENCODE_API_KEY", "oc-key"),
            ("OPENCODE_CONFIG_DIR", "/s/home"),
            ("PATH", "/ctxloom/bin:/usr/bin"),
            ("XDG_DATA_HOME", "/s/home/xdg/data"),
        ])
    );
    assert_eq!(
        ignored,
        ["HOME", "OPENCODE_CONFIG_DIR", "PATH", "XDG_DATA_HOME"]
            .iter()
            .map(|name| Ignored::Owned {
                permit: format!("CTXLOOM_PERMIT_{name}")
            })
            .collect::<Vec<_>>()
    );
}
