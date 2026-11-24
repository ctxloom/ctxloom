//! A session's title. Go: `operations.SessionTitle` and
//! `TestViewSession_TitleIsTheFirstUserPrompt` /
//! `TestViewSession_NoUserPromptIsNoTitle` (session_title_test.go),
//! `sessions.FirstLineSummary` (sidecar.go), `textutil.TruncateBytes`.

use daemon_proto::v1::{log_entry::Entry as Arm, LogEntry, LogPrompt, LogTurnEnd, LogWords};

use super::{first_line_summary, title_of, TITLE_BYTES};

fn prompt(text: &str) -> LogEntry {
    LogEntry {
        entry: Some(Arm::Prompt(LogPrompt {
            prompt_id: "p".to_string(),
            text: text.to_string(),
        })),
        ..LogEntry::default()
    }
}

fn words(text: &str) -> LogEntry {
    LogEntry {
        entry: Some(Arm::Words(LogWords {
            text: text.to_string(),
            ..LogWords::default()
        })),
        ..LogEntry::default()
    }
}

// Go: the title is the first user prompt that says anything: what came
// before it (an assistant greeting) is not the user's words; a blank prompt
// says nothing; it is cut to its first non-empty line and 80 characters.
#[test]
fn the_title_is_the_first_prompt_cut_to_one_line() {
    let long = "y".repeat(120);
    let entries = [
        words("hello, how can I help?"),
        prompt("   "),
        prompt(&format!("\n  fix the flaky purge test {long}\nsecond line")),
        prompt("a later prompt"),
    ];
    let got = title_of(&entries).expect("a title");
    assert_eq!(
        got,
        first_line_summary(&format!("fix the flaky purge test {long}"))
    );
    assert!(got.starts_with("fix the flaky purge test "), "{got}");
    assert_eq!(got.len(), TITLE_BYTES);
}

// Go: the fixture's title, read by the listing, the search and `show`
// (session_seed_fixture.go: "Seeded prompt for <harp>.").
#[test]
fn the_seeded_prompt_is_the_title() {
    assert_eq!(
        title_of(&[prompt("Seeded prompt for amber-swift-owl.")]).as_deref(),
        Some("Seeded prompt for amber-swift-owl.")
    );
}

// Go: no entry, or one with no prompt in it, is no title.
#[test]
fn no_prompt_is_no_title() {
    assert_eq!(title_of(&[]), None);
    assert_eq!(
        title_of(&[
            words("only me"),
            LogEntry {
                entry: Some(Arm::TurnEnd(LogTurnEnd::default())),
                ..LogEntry::default()
            }
        ]),
        None
    );
}

// Go: FirstLineSummary trims, takes the first line, caps at 80 bytes.
#[test]
fn a_summary_is_the_first_line_trimmed_and_capped() {
    assert_eq!(first_line_summary("  one\ntwo "), "one");
    assert_eq!(first_line_summary("short"), "short");
    assert_eq!(first_line_summary(""), "");
    assert_eq!(first_line_summary(&"a".repeat(100)).len(), 80);
}

// Go: TruncateBytes backs off to the nearest rune boundary so a multibyte
// character is never split.
#[test]
fn a_cap_never_splits_a_character() {
    // 79 ASCII bytes then a 3-byte character straddling the 80-byte cap.
    let text = format!("{}\u{20ac}tail", "a".repeat(79));
    let got = first_line_summary(&text);
    assert_eq!(got, "a".repeat(79));
    assert!(got.len() <= 80);
    // One byte more room and the character fits whole.
    let text = format!("{}\u{20ac}", "a".repeat(77));
    assert_eq!(first_line_summary(&text), text);
}
