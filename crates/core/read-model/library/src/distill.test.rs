//! The distilled form's state, the floor an answer must clear, and the
//! request and file distill writes (plan step 5, T30).

use super::*;

const DIGEST: &str = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

// T30: only a recorded digest that is the source's now is fresh.
#[test]
fn test_a_distilled_form_is_fresh_only_while_its_recorded_digest_is_its_source_s() {
    assert_eq!(state("", "", ""), DistilledState::None);
    assert_eq!(state("", DIGEST, DIGEST), DistilledState::None);
    assert_eq!(state("short", DIGEST, DIGEST), DistilledState::Fresh);
    assert_eq!(state("short", DIGEST, "other"), DistilledState::Stale);
    // A form from before T30 records none: stale, even against no source.
    assert_eq!(state("short", "", DIGEST), DistilledState::Stale);
    assert_eq!(state("short", "", ""), DistilledState::Stale);
}

#[test]
fn test_each_state_has_its_word() {
    assert_eq!(DistilledState::None.word(), "none");
    assert_eq!(DistilledState::Fresh.word(), "fresh");
    assert_eq!(DistilledState::Stale.word(), "stale");
    assert_eq!(DistilledState::default(), DistilledState::None);
}

#[test]
fn test_a_source_is_recorded_by_the_sha256_of_its_bytes() {
    assert_eq!(source_digest(b""), DIGEST);
    assert_eq!(source_digest(b"abc"), crate::sha256::hex(b"abc"));
}

// content_distill.feature: the floor is proportional, under 2% of the source.
#[test]
fn test_an_answer_under_two_percent_of_its_source_is_implausibly_short() {
    let source = "x".repeat(1000);
    assert!(implausibly_short(&source, "tiny"));
    assert!(implausibly_short(&source, &"y".repeat(19)));
    assert!(!implausibly_short(&source, &"y".repeat(20)));
    assert!(!implausibly_short(&source, &"y".repeat(21)));
    // Counted trimmed, in characters.
    assert!(implausibly_short(
        &source,
        &format!("  {}  \n", "y".repeat(19))
    ));
    assert!(!implausibly_short(
        &format!("\n{source}\n\n"),
        &"\u{e9}".repeat(20)
    ));
    // A short source asks only a short answer, and an empty one none.
    assert!(!implausibly_short("ab", "a"));
    assert!(!implausibly_short("", ""));
    assert!(implausibly_short("a long enough source", ""));
}

#[test]
fn test_the_request_is_the_prompt_then_the_body_inside_its_tags() {
    assert_eq!(
        request("Compress this.\n\n", "\n  the body\n"),
        "Compress this.\n\n<content_to_compress>\nthe body\n</content_to_compress>\n"
    );
}

#[test]
fn test_a_distilled_file_records_its_source_s_digest_before_the_answer() {
    assert_eq!(
        distilled_file(DIGEST, "\n the short form \n\n"),
        format!("---\nsource_sha256: {DIGEST}\n---\nthe short form\n")
    );
    assert_eq!(SOURCE_DIGEST_KEY, "source_sha256");
}
