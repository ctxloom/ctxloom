//! What an engine adapter says of a tool call, readably, through the engine
//! extension (`<engine>.summary`, `<engine>.result`, `<engine>.exit`): ONE
//! meaningful line of the call's input, the first lines of its result, and a
//! shell command's exit code. Every text is MASKED before it is recorded
//! ([`masked`]) and bounded ([`SUMMARY_CUT`], [`RESULT_LINES`]). Pure: the
//! adapters decide which field of which tool is the summary; this is what is
//! done with it.

use std::collections::BTreeMap;

/// A summary, and each line of a result, is cut at this many characters.
pub const SUMMARY_CUT: usize = 120;
/// A result is its first this many (non-empty) lines.
pub const RESULT_LINES: usize = 5;
/// Past this many characters a line is not read further, before it is masked
/// (a bound on the work, not on what is kept).
const READ_BOUND: usize = 1000;

/// The masking seam: `text` with every block that looks like a random token
/// replaced by its marker (`[masked:N]`, rust-support's `secret-mask`). Every text an adapter records goes
/// through it first, and nothing is cut before it, so a token is never kept
/// in part.
pub fn masked(text: &str) -> String {
    secret_mask::mask(text).text
}

/// `line` masked, then cut at [`SUMMARY_CUT`] characters (an ellipsis marks
/// the cut).
fn tidy(line: &str) -> String {
    let bounded: String = line.trim().chars().take(READ_BOUND).collect();
    let masked = masked(&bounded);
    if masked.chars().count() <= SUMMARY_CUT {
        return masked;
    }
    let mut cut: String = masked.chars().take(SUMMARY_CUT).collect();
    cut.push('\u{2026}');
    cut
}

/// The first non-empty line of `text`, masked and cut: a summary. Empty when
/// the text has none.
pub fn summary_line(text: &str) -> String {
    text.lines()
        .map(str::trim)
        .find(|line| !line.is_empty())
        .map(tidy)
        .unwrap_or_default()
}

/// The first [`RESULT_LINES`] non-empty lines of `text`, each masked and cut,
/// joined by newlines. Empty when the text has none.
pub fn result_lines(text: &str) -> String {
    text.lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .take(RESULT_LINES)
        .map(tidy)
        .collect::<Vec<_>>()
        .join("\n")
}

/// A search's summary: its `pattern` (quoted when `quoted`) and, when given,
/// `in <path>`. Empty without a pattern.
pub fn search_summary(pattern: &str, path: &str, quoted: bool) -> String {
    let pattern = summary_line(pattern);
    if pattern.is_empty() {
        return String::new();
    }
    let pattern = if quoted {
        format!("\"{pattern}\"")
    } else {
        pattern
    };
    match summary_line(path).as_str() {
        "" => pattern,
        path => format!("{pattern} in {path}"),
    }
}

/// The engine data of one call: `<engine>.summary` and `<engine>.result`
/// where they say something, and `<engine>.exit` for a command's exit code.
pub fn engine_data(
    engine: &str,
    summary: &str,
    result: &str,
    exit: Option<i64>,
) -> BTreeMap<String, String> {
    let mut data = BTreeMap::new();
    if !summary.is_empty() {
        data.insert(format!("{engine}.summary"), summary.to_string());
    }
    if !result.is_empty() {
        data.insert(format!("{engine}.result"), result.to_string());
    }
    if let Some(code) = exit {
        data.insert(format!("{engine}.exit"), code.to_string());
    }
    data
}

#[cfg(test)]
#[path = "tool_view.test.rs"]
mod tests;
