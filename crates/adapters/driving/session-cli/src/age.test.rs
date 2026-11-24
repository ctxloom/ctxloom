//! The compact age: rounded down to the largest whole unit.

use std::time::Duration;

use super::age;

fn secs(n: u64) -> String {
    age(Duration::from_secs(n))
}

#[test]
fn test_under_a_minute_is_seconds() {
    assert_eq!(secs(0), "0s");
    assert_eq!(secs(12), "12s");
    assert_eq!(secs(59), "59s");
}

#[test]
fn test_a_minute_is_the_first_whole_minute() {
    assert_eq!(secs(60), "1m");
    assert_eq!(secs(179), "2m");
    assert_eq!(secs(3599), "59m");
}

#[test]
fn test_an_hour_is_the_first_whole_hour() {
    assert_eq!(secs(3600), "1h");
    assert_eq!(secs(3 * 3600 + 59 * 60), "3h");
    assert_eq!(secs(86_399), "23h");
}

#[test]
fn test_a_day_is_the_first_whole_day() {
    assert_eq!(secs(86_400), "1d");
    assert_eq!(secs(2 * 86_400 + 86_399), "2d");
    assert_eq!(secs(400 * 86_400), "400d");
}

#[test]
fn test_a_fraction_of_a_second_is_dropped() {
    assert_eq!(age(Duration::from_millis(59_999)), "59s");
}
