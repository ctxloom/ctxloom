use angzarr_diagnostic::Diagnostic;

use super::*;

fn warning(why: &str) -> v1::Notice {
    daemon_api::StartUpIncomplete {
        why: why.to_string(),
    }
    .notice()
}

// OD-52: what a command heard of the daemon is said once, as the daemon's
// own diagnostic (its code and args), and then never again.
#[test]
fn what_is_heard_is_taken_once_in_its_own_code_and_args() {
    hear(vec![warning("the store is locked")]);
    let said = take_heard();
    assert_eq!(said.len(), 1);
    assert_eq!(said[0].code(), "daemon-api-start-up-incomplete");
    let args: Vec<(String, String)> = said[0]
        .args()
        .into_iter()
        .map(|(name, arg)| (name.to_string(), format!("{arg:?}")))
        .collect();
    assert_eq!(args.len(), 1, "{args:?}");
    assert_eq!(args[0].0, "why");
    assert!(args[0].1.contains("the store is locked"), "{args:?}");
    assert!(take_heard().is_empty());
}

// OD-55: every answer carries the daemon's warnings, so a command hears
// the same one many times; it says each once a process, whenever it heard
// it, and a warning heard again after it was said is not said again.
#[test]
fn a_warning_heard_from_many_answers_is_said_once_a_process() {
    let once = warning("heard on every answer");
    let other = warning("another");
    hear(vec![once.clone()]);
    hear(vec![once.clone(), other.clone()]);
    let said = take_heard();
    assert_eq!(said.len(), 2, "{said:?}");
    hear(vec![once, other]);
    assert!(take_heard().is_empty());
}
