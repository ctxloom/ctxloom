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

/// What a SESSION's name may not begin with: an approval ask's names do
/// (step 8, Q7), so a word beginning it names an ask, never a session.
pub const RESERVED_PREFIX: &str = "ask-";

/// The reason a session name beginning [`RESERVED_PREFIX`] is refused
/// (texts.md §5b).
pub const RESERVED_REASON: &str = "reserved: names beginning ask- are an approval ask's";

/// Whether `name` can be a NEW session's name: [`validate`]'s path rules,
/// and not the ask form (Q7). A session already named so keeps its name
/// (Q-B): nothing re-judges an existing harp.
pub fn validate_session_name(name: &str) -> Result<(), SessionRejection> {
    validate(name)?;
    if name.starts_with(RESERVED_PREFIX) {
        return Err(SessionRejection::HarpInvalid {
            harp: name.to_string(),
            reason: RESERVED_REASON.to_string(),
        });
    }
    Ok(())
}

#[cfg(test)]
#[path = "harp.test.rs"]
mod tests;
