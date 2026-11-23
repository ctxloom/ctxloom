use std::collections::BTreeMap;

use super::*;

fn env(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[test]
fn test_unset_variables_default_under_the_users_home() {
    let got = tool_homes(&env(&[]), Some("/home/alice"));
    assert_eq!(
        got.get(RUSTUP_HOME).map(String::as_str),
        Some("/home/alice/.rustup")
    );
    assert_eq!(
        got.get(CARGO_HOME).map(String::as_str),
        Some("/home/alice/.cargo")
    );
    assert_eq!(
        got.get(NPM_CACHE).map(String::as_str),
        Some("/home/alice/.npm")
    );
    assert_eq!(got.len(), TOOL_HOME_VARIABLES.len());
}

#[test]
fn test_a_variable_the_user_sets_wins_over_the_default() {
    let caller = env(&[(RUSTUP_HOME, "/opt/rustup"), (CARGO_HOME, "/opt/cargo")]);
    let got = tool_homes(&caller, Some("/home/alice"));
    assert_eq!(
        got.get(RUSTUP_HOME).map(String::as_str),
        Some("/opt/rustup")
    );
    assert_eq!(got.get(CARGO_HOME).map(String::as_str), Some("/opt/cargo"));
    assert_eq!(
        got.get(NPM_CACHE).map(String::as_str),
        Some("/home/alice/.npm")
    );
}

#[test]
fn test_an_empty_variable_counts_as_unset() {
    let got = tool_homes(&env(&[(CARGO_HOME, "")]), Some("/home/alice"));
    assert_eq!(
        got.get(CARGO_HOME).map(String::as_str),
        Some("/home/alice/.cargo")
    );
}

#[test]
fn test_npms_upper_case_spelling_is_the_users_cache_too() {
    let got = tool_homes(&env(&[("NPM_CONFIG_CACHE", "/c/npm")]), Some("/home/alice"));
    assert_eq!(got.get(NPM_CACHE).map(String::as_str), Some("/c/npm"));
}

#[test]
fn test_without_a_user_home_only_what_the_user_set_is_given() {
    let got = tool_homes(&env(&[(CARGO_HOME, "/opt/cargo")]), None);
    assert_eq!(got, env(&[(CARGO_HOME, "/opt/cargo")]));
    assert!(tool_homes(&env(&[]), None).is_empty());
}

#[test]
fn test_a_trailing_slash_of_the_home_is_not_doubled() {
    let got = tool_homes(&env(&[]), Some("/home/alice/"));
    assert_eq!(
        got.get(CARGO_HOME).map(String::as_str),
        Some("/home/alice/.cargo")
    );
}
