//! Distillation's pure half (plan step 5; owner T30, S51): a distilled form
//! names its source, the check of an answer, and the request a distiller is
//! handed.
//!
//! A `.distilled.md` records, in its front matter under [`SOURCE_DIGEST_KEY`],
//! the SHA-256 of the `.md` it was distilled from. Each time a distilled form
//! would be used its source is hashed again and compared ([`state`]): only a
//! match is [`DistilledState::Fresh`]; a mismatch, or no recorded digest (a
//! file from before T30, an inline document's form), is
//! [`DistilledState::Stale`], and the original is served. No verdict is kept.

/// The front-matter key of a `.distilled.md` that holds its source's
/// SHA-256, lowercase hex.
pub const SOURCE_DIGEST_KEY: &str = "source_sha256";

/// The floor an answer must clear, in hundredths of its source's length: an
/// answer under 2% of what was sent is taken for a truncated one.
pub const FLOOR_PERCENT: usize = 2;

/// Whether an item has a distilled form, and whether it still matches its
/// source.
#[derive(Clone, Copy, Default, PartialEq, Eq, Debug)]
pub enum DistilledState {
    /// No distilled form.
    #[default]
    None,
    /// Its recorded source digest is the source's now.
    Fresh,
    /// It records another digest, or none.
    Stale,
}

impl DistilledState {
    /// The word `fragment show` and doctor say it by.
    pub fn word(self) -> &'static str {
        match self {
            DistilledState::None => "none",
            DistilledState::Fresh => "fresh",
            DistilledState::Stale => "stale",
        }
    }
}

/// The state of a distilled form `distilled` (empty: none) that recorded
/// `recorded` as its source's digest, against `source`, the digest of the
/// source as it is now.
pub fn state(distilled: &str, recorded: &str, source: &str) -> DistilledState {
    let _ = (recorded, source);
    if distilled.is_empty() {
        DistilledState::None
    } else {
        DistilledState::Fresh
    }
}

/// The digest a source file is recorded by: the SHA-256 of its bytes, hex.
pub fn source_digest(source: &[u8]) -> String {
    let _ = source;
    String::new()
}

/// Whether `answer` is implausibly short for `source`: under
/// [`FLOOR_PERCENT`] of its length, both counted in characters, trimmed.
/// "Too short" means something only against what was sent, so the floor is
/// proportional, never absolute.
pub fn implausibly_short(source: &str, answer: &str) -> bool {
    let _ = (source, answer);
    false
}

/// What a distiller is asked: the distill prompt (`ctxloom#prompts/distill`,
/// owner ruling 128), then the item's body inside the `content_to_compress`
/// tags the prompt names.
pub fn request(instruction: &str, body: &str) -> String {
    let _ = (instruction, body);
    String::new()
}

/// The `.distilled.md` of `answer`, distilled from a source whose digest is
/// `digest`: the digest in its front matter, then the answer, trimmed, with
/// one final newline.
pub fn distilled_file(digest: &str, answer: &str) -> String {
    let _ = (digest, answer);
    String::new()
}

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
