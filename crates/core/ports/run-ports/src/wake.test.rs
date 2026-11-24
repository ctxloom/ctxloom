//! The terminal wake's line (owner ruling 2026-11-18): one renderer, one
//! parser, so they cannot disagree.

use crate::{wake_line, wake_nonce};

const NONCE: &str = "0123456789abcdef";

#[test]
fn a_wake_line_names_its_nonce_and_reads_back_as_it() {
    let line = wake_line(NONCE);
    assert_eq!(line, "ctxloom: wake 0123456789abcdef");
    assert_eq!(wake_nonce(&line), Some(NONCE));
}

// Only the WHOLE prompt is a wake: a human's prompt that quotes one, or a
// line with a nonce of another shape, is not, so the hook never blocks it.
#[test]
fn only_the_whole_line_with_a_nonce_of_its_shape_is_a_wake() {
    let line = wake_line(NONCE);
    assert_eq!(wake_nonce(&format!("why did I get {line}?")), None);
    assert_eq!(wake_nonce(&format!("{line}\nand more")), None);
    assert_eq!(
        wake_nonce(&wake_line("0123456789ABCDEF")),
        None,
        "lowercase hex"
    );
    assert_eq!(wake_nonce(&wake_line("0123456789abcde")), None, "16 digits");
    assert_eq!(wake_nonce(&wake_line("0123456789abcdeg")), None, "hex");
    assert_eq!(
        wake_nonce(&format!("{line}\n")),
        Some(NONCE),
        "a trailing newline"
    );
}
