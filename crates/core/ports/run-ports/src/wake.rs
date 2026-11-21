//! The terminal wake (owner ruling 2026-11-18): what a primary terminal
//! session's ctxloom MCP server posts on its engine's messaging socket to
//! start a turn in an idle TUI, and how the turn's prompt hook knows the
//! prompt was one. The line is a pure trigger carrying a nonce, never a
//! message: the hook delivers the session's pending messages.

/// What a wake line starts with.
const PREFIX: &str = "ctxloom: wake ";

/// What a wake line ends with.
const SUFFIX: &str = " (messages delivered at turn start)";

/// How many hex digits a nonce is.
pub const NONCE_DIGITS: usize = 16;

/// The prompt a wake with `nonce` starts its turn with.
pub fn wake_line(nonce: &str) -> String {
    format!("{PREFIX}{nonce}{SUFFIX}")
}

/// The nonce, when `prompt` IS a wake line (the whole prompt, a trailing
/// newline aside); none for any other prompt, a human's that quotes one
/// among them.
pub fn wake_nonce(prompt: &str) -> Option<&str> {
    let nonce = prompt
        .strip_suffix('\n')
        .unwrap_or(prompt)
        .strip_prefix(PREFIX)?
        .strip_suffix(SUFFIX)?;
    let shaped = nonce.len() == NONCE_DIGITS
        && nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
    shaped.then_some(nonce)
}

/// Where a wake is posted: the engine's own messaging endpoint, as the
/// engine exported it to the process that posts (claude's
/// `CLAUDE_CODE_MESSAGING_SOCKET`). A failure is said, never a token.
pub trait WakePost: Send + Sync {
    fn post(&self, line: &str) -> Result<(), String>;
}

#[cfg(test)]
#[path = "wake.test.rs"]
mod tests;
