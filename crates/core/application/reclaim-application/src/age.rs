//! `--older-than` and `--purge-older-than`: an age before now (`30d`, `1h30m`,
//! `1.5d`) or a date (`2026-01-01`, UTC midnight), as an instant. The grammar
//! is `duration-text`'s; this converts at the edge between the application's
//! [`Timestamp`] and the std instants it speaks. The core's purity rules ban
//! naming `SystemTime` (the clock's type); the instants here are pure values
//! derived from `UNIX_EPOCH` and the caller's `now`, never read from a clock,
//! so they are only inferred, never named.

use std::time::{Duration, UNIX_EPOCH};

use duration_text::{DurationError, DurationText};
use sessions_application::Timestamp;

use crate::ReclaimError;

/// `value`, the argument of `flag`, as an instant relative to `now`.
pub fn parse_age(flag: &str, value: &str, now: Timestamp) -> Result<Timestamp, ReclaimError> {
    let bad = |cause| ReclaimError::BadAge {
        flag: flag.to_string(),
        value: value.to_string(),
        cause,
    };
    let overflow = || DurationError::Overflow {
        text: value.trim().to_string(),
    };
    // `now`'s whole seconds as an instant (from, or before the epoch, back
    // from, the epoch). Its nanoseconds never pass through the std instant,
    // whose resolution is the platform's (100 ns on Windows, which would drop
    // a `now` of 7 ns to 0): a span's cutoff gets them back below, exactly.
    let extra = u64::try_from(now.nanos).map_err(|_| bad(overflow()))?;
    let whole = Duration::from_secs(now.seconds.unsigned_abs());
    let at = if now.seconds >= 0 {
        UNIX_EPOCH.checked_add(whole)
    } else {
        UNIX_EPOCH.checked_sub(whole)
    }
    .ok_or_else(|| bad(overflow()))?;
    let text = DurationText::new(at);
    let cutoff = text.cutoff(value).map_err(bad)?;
    // The cutoff in whole seconds from the epoch: whole, as `now` was made
    // whole and duration-text's spans and dates are whole seconds.
    let seconds = match cutoff.duration_since(UNIX_EPOCH) {
        Ok(after) => i64::try_from(after.as_secs()).ok(),
        Err(before) => i64::try_from(before.duration().as_secs())
            .ok()
            .map(i64::wrapping_neg),
    };
    // A span is counted back from `now`, nanoseconds and all (they count
    // forward from the whole seconds, as in protobuf); a date is its
    // midnight, whatever `now` is.
    let extra = if text.parse(value).is_ok() { extra } else { 0 };
    seconds
        .and_then(|seconds| {
            Some(Timestamp {
                seconds: seconds.checked_add(i64::try_from(extra / NANOS).ok()?)?,
                nanos: i32::try_from(extra % NANOS).ok()?,
            })
        })
        .ok_or_else(|| bad(overflow()))
}

/// Nanoseconds in a second.
const NANOS: u64 = 1_000_000_000;
