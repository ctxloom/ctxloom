//! A session's title: the first thing its user asked, read from its
//! transcript's prompt entries.

use daemon_proto::v1::{log_entry::Entry as Arm, LogEntry};

/// The longest a title is, in bytes (Go's `FirstLineSummary`).
pub const TITLE_BYTES: usize = 80;

/// `s` trimmed to its first line and capped at [`TITLE_BYTES`] bytes, never
/// splitting a character: the one-line form a session's title takes.
pub fn first_line_summary(s: &str) -> String {
    let s = s.trim();
    let first = s.split('\n').next().unwrap_or_default().trim();
    truncate_bytes(first, TITLE_BYTES).to_string()
}

/// `s` cut to at most `max` bytes, backed off to the nearest character
/// boundary (Go's `textutil.TruncateBytes`).
fn truncate_bytes(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut end = max;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    &s[..end]
}

/// The title of the session whose transcript is `entries`: its first prompt
/// that says anything, cut to one line ([`first_line_summary`]). `None` when
/// the transcript holds no such prompt: a renderer chooses its own
/// placeholder.
pub fn title_of(entries: &[LogEntry]) -> Option<String> {
    entries.iter().find_map(|entry| match &entry.entry {
        Some(Arm::Prompt(prompt)) if !prompt.text.trim().is_empty() => {
            Some(first_line_summary(&prompt.text))
        }
        _ => None,
    })
}

#[cfg(test)]
#[path = "title.test.rs"]
mod tests;
