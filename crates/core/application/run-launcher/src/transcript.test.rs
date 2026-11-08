use super::*;

#[test]
fn an_instant_is_spelled_as_rfc_3339_in_utc() {
    assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
    assert_eq!(rfc3339(1_700_000_000), "2023-11-14T22:13:20Z");
    // A leap day, and the day after it.
    assert_eq!(rfc3339(951_782_400), "2000-02-29T00:00:00Z");
    assert_eq!(rfc3339(951_868_799), "2000-02-29T23:59:59Z");
    assert_eq!(rfc3339(951_868_800), "2000-03-01T00:00:00Z");
    // The turn of a year, and a date before the epoch.
    assert_eq!(rfc3339(1_767_225_599), "2025-12-31T23:59:59Z");
    assert_eq!(rfc3339(-1), "1969-12-31T23:59:59Z");
}

#[test]
fn an_entry_is_one_line_in_gos_envelope() {
    let line = entry_line(
        "amber-swift-owl",
        "claude-code",
        3,
        0,
        Speaker::Assistant,
        "two\nlines \"quoted\"",
    );
    assert!(line.ends_with('\n'));
    assert_eq!(line.matches('\n').count(), 1, "one line: {line}");
    let value: serde_json::Value = serde_json::from_str(line.trim_end()).unwrap();
    assert_eq!(
        value,
        serde_json::json!({
            "v": 1,
            "harp": "amber-swift-owl",
            "engine": "claude-code",
            "seq": 3,
            "ts": "1970-01-01T00:00:00Z",
            "kind": "entry",
            "entry": {"type": "assistant", "content": "two\nlines \"quoted\""},
        })
    );
}

#[test]
fn the_user_speaks_as_user() {
    let line = entry_line("h", "e", 0, 0, Speaker::User, "hi");
    assert!(line.contains(r#""type":"user""#), "{line}");
}

#[test]
fn the_next_sequence_is_the_lines_already_there() {
    assert_eq!(next_seq(b""), 0);
    assert_eq!(next_seq(b"{}\n"), 1);
    assert_eq!(next_seq(b"{}\n{}\n"), 2);
    // A blank line is no entry; a last line without its newline is one.
    assert_eq!(next_seq(b"{}\n\n  \n{}"), 2);
}
