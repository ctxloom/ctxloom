//! What a harp may be called: Go's `harp.Validate`. A harp is minted, never
//! chosen by a human (S42, S55), so Go's `ValidateRename` bound is gone.

use crate::SessionRejection;

/// Whether `name` can be a session's name at all (Go's `harp.Validate`): it
/// becomes a path component, so it is not empty, `.` or `..`, carries no path
/// separator (`/`, `\` or `:`) or control character, and has no whitespace at
/// either end.
pub fn validate(name: &str) -> Result<(), SessionRejection> {
    let invalid = |reason: &str| SessionRejection::HarpInvalid {
        harp: name.to_string(),
        reason: reason.to_string(),
    };
    if name.is_empty() {
        return Err(invalid("empty"));
    }
    if name == "." || name == ".." {
        return Err(invalid("directory"));
    }
    if name.contains(['/', '\\', ':']) {
        return Err(invalid("separator"));
    }
    if name.chars().any(|c| c < '\u{20}' || c == '\u{7f}') {
        return Err(invalid("control"));
    }
    if name.trim() != name {
        return Err(invalid("whitespace"));
    }
    Ok(())
}

#[cfg(test)]
#[path = "harp.test.rs"]
mod tests;
