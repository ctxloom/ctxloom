//! How long ago, as the screen's state cell says it.

use std::time::Duration;

/// `age` as one number and the unit of the largest whole unit it holds,
/// rounded down: `12s`, `3m`, `5h`, `2d`. Days are the largest unit.
///
/// Temporary, until `duration-text`'s age formatter is adopted; this is the
/// one place the format lives.
pub fn age(age: Duration) -> String {
    let seconds = age.as_secs();
    match seconds {
        0..60 => format!("{seconds}s"),
        60..3600 => format!("{}m", seconds / 60),
        3600..86_400 => format!("{}h", seconds / 3600),
        _ => format!("{}d", seconds / 86_400),
    }
}

#[cfg(test)]
#[path = "age.test.rs"]
mod tests;
