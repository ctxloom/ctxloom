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
fn an_entry_is_one_line_in_d27s_envelope() {
    let line = entry_line(
        "amber-swift-owl",
        "0b2c9a52-1111-4222-8333-444455556666",
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
            "session_id": "0b2c9a52-1111-4222-8333-444455556666",
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
    let line = entry_line("h", "s", "e", 0, 0, Speaker::User, "hi");
    assert!(line.contains(r#""type":"user""#), "{line}");
}

// Centuries that are not leap years (the era arithmetic's century term), and
// the one that is.
#[test]
fn a_century_year_that_is_not_a_leap_year_has_no_february_29() {
    assert_eq!(rfc3339(4_107_542_399), "2100-02-28T23:59:59Z");
    assert_eq!(rfc3339(4_107_542_400), "2100-03-01T00:00:00Z");
    assert_eq!(rfc3339(7_289_611_200), "2200-12-31T12:00:00Z");
    assert_eq!(rfc3339(13_574_563_200), "2400-02-29T00:00:00Z");
}
