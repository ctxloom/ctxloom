//! The terminal answer: a yes is `y` or `yes`, however written; nothing else is.

use super::is_yes;

#[test]
fn only_y_and_yes_are_a_yes() {
    for yes in ["y\n", "Y", "yes", " YES \n", "Yes\r\n"] {
        assert!(is_yes(yes), "{yes:?}");
    }
    for no in ["", "\n", "n", "no", "yep", "ye", "sure", "y y"] {
        assert!(!is_yes(no), "{no:?}");
    }
}
