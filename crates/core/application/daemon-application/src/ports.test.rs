//! The ports' value types: what their Debug withholds.

use std::collections::BTreeMap;

use super::HandOff;

// Review 4: a launcher's hand-off holds its environment and its user's
// settings, either of which may hold a credential; the Debug names each
// variable and shows no value.
#[test]
fn test_a_hand_off_s_debug_names_its_variables_and_shows_no_value() {
    let handed = HandOff {
        env: BTreeMap::from([("TOKEN".to_string(), "sk-not-shown".to_string())]), // gitleaks:allow
        launcher: BTreeMap::from([("SETTINGS".to_string(), "hidden-too".to_string())]),
        agent: "reviewer".to_string(),
        program: None,
    };
    let shown = format!("{handed:?}");
    assert!(
        shown.contains("TOKEN") && shown.contains("SETTINGS"),
        "{shown}"
    );
    assert!(!shown.contains("sk-not-shown"), "{shown}");
    assert!(!shown.contains("hidden-too"), "{shown}");
    assert!(shown.contains("reviewer"), "{shown}");
}
