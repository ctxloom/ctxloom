//! The terminal answer to the restart offer, `[Y/n]`: a bare Enter is a yes,
//! `n` and `no` (any case) are a no, a closed input is a no, anything else is
//! a no.

use super::answered;

#[test]
fn a_bare_enter_is_a_yes() {
    for enter in ["\n", "\r\n", "  \n"] {
        assert!(answered(Some(enter)), "{enter:?}");
    }
}

#[test]
fn y_and_yes_are_a_yes_however_written() {
    for yes in ["y\n", "Y", "yes", " YES \n", "Yes\r\n"] {
        assert!(answered(Some(yes)), "{yes:?}");
    }
}

#[test]
fn n_and_no_and_anything_else_are_a_no() {
    for no in [
        "n\n", "N", "no", " NO \n", "No\r\n", "yep", "ye", "sure", "y y", "nope",
    ] {
        assert!(!answered(Some(no)), "{no:?}");
    }
}

#[test]
fn a_closed_input_is_a_no_though_a_bare_enter_is_a_yes() {
    assert!(!answered(None));
    assert!(answered(Some("\n")));
}
