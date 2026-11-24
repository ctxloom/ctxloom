//! The age bounds, over `duration-text`'s grammar. Go: `agebound.Parse`
//! (release/0.7), which read only `Nd`, `Nw`, `Nh` and a date.

use coded_diagnostic::Diagnostic as _;
use duration_text::DurationError;
use sessions_application::Timestamp;

use crate::{parse_age, ReclaimError};

const NOW: Timestamp = Timestamp {
    seconds: 1_800_000_000,
    nanos: 7,
};

#[test]
fn an_age_is_that_long_before_now() {
    for (text, seconds) in [
        ("30d", 30 * 86_400),
        ("12w", 12 * 7 * 86_400),
        ("720h", 720 * 3_600),
        ("1h30m", 5_400),
        ("90m", 5_400),
        ("36h", 129_600),
        ("1.5h", 5_400),
        ("0d", 0),
        (" 2w1d ", 15 * 86_400),
    ] {
        let at = parse_age("--older-than", text, NOW).unwrap();
        assert_eq!(at.seconds, NOW.seconds - seconds, "{text}");
        assert_eq!(at.nanos, NOW.nanos, "{text}");
    }
}

#[test]
fn a_date_is_its_midnight_in_utc() {
    let at = parse_age("--purge-older-than", "2026-01-01", NOW).unwrap();
    assert_eq!((at.seconds, at.nanos), (1_767_225_600, 0));
}

#[test]
fn a_date_before_the_epoch_counts_back_from_it() {
    let at = parse_age("--older-than", "1969-12-31", NOW).unwrap();
    assert_eq!((at.seconds, at.nanos), (-86_400, 0));
}

#[test]
fn an_age_reaching_before_the_epoch_keeps_its_fraction_of_a_second() {
    let now = Timestamp {
        seconds: 10,
        nanos: 250_000_000,
    };
    let at = parse_age("--older-than", "1m", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (-50, 250_000_000));
    let now = Timestamp {
        seconds: 10,
        nanos: 0,
    };
    let at = parse_age("--older-than", "10s", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (0, 0));
    let at = parse_age("--older-than", "11s", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (-1, 0));
    let now = Timestamp {
        seconds: 0,
        nanos: 250_000_000,
    };
    let at = parse_age("--older-than", "1s", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (-1, 250_000_000));
}

#[test]
fn a_now_before_the_epoch_is_read_as_it_is() {
    let now = Timestamp {
        seconds: -100,
        nanos: 500,
    };
    let at = parse_age("--older-than", "1m", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (-160, 500));
}

#[test]
fn an_unrepresentable_now_or_span_is_refused_as_an_overflow() {
    for (now, text) in [
        (
            Timestamp {
                seconds: i64::MIN,
                nanos: 0,
            },
            "1d",
        ),
        (
            Timestamp {
                seconds: 1,
                nanos: -1,
            },
            "1d",
        ),
    ] {
        match parse_age("--older-than", text, now) {
            Err(ReclaimError::BadAge {
                cause: DurationError::OutOfRange { .. },
                ..
            }) => {}
            other => panic!("{text}: {other:?}"),
        }
    }
}

#[test]
fn anything_else_is_refused_naming_the_flag_and_why() {
    // The grammar and its refusals are duration-text's (jiff's friendly
    // format): a refusal is held to its code, its jiff text is jiff's.
    for (text, code) in [
        ("", "duration-text-empty"),
        ("soon", "duration-text-malformed"),
        ("30", "duration-text-malformed"),
        ("1.5d", "duration-text-malformed"),
        ("18446744073709551615s", "duration-text-malformed"),
        ("d", "duration-text-malformed"),
        ("-3d", "duration-text-negative"),
        ("1y", "duration-text-calendar-unit"),
        ("2026-13-40", "duration-text-invalid-date"),
    ] {
        match parse_age("--older-than", text, NOW) {
            Err(ReclaimError::BadAge { flag, value, cause }) => {
                assert_eq!((flag.as_str(), value.as_str()), ("--older-than", text));
                assert_eq!(cause.code(), code, "{text}");
            }
            other => panic!("{text}: {other:?}"),
        }
    }
}

// A `now` whose nanoseconds run past a second carries them into its seconds.
#[test]
fn a_now_with_more_than_a_second_of_nanoseconds_carries_them() {
    let now = Timestamp {
        seconds: 10,
        nanos: 1_500_000_000,
    };
    let at = parse_age("--older-than", "1s", now).unwrap();
    assert_eq!((at.seconds, at.nanos), (10, 500_000_000));
}
