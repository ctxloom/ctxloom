//! What a write says beside its answer (step 10b texts.md §3): a removal's
//! path kept in the trash (G12). A notice, never a refusal. (A read's git
//! notices, a tree in motion and the home made a repository, went with the
//! git-free ruling, owner 2026-11-18.)

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

/// A notice of a write.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LibraryApiNotice {
    /// A removal took `path` into the trash `slot`, where it is kept 7 days
    /// (step 10b, G12; texts.md §3).
    Trashed { path: String, slot: String },
}

impl fmt::Display for LibraryApiNotice {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for LibraryApiNotice {
    fn code(&self) -> &str {
        match self {
            LibraryApiNotice::Trashed { .. } => code!("notice-trashed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            LibraryApiNotice::Trashed { path, slot } => vec![
                ("path", path.as_str().into()),
                ("slot", slot.as_str().into()),
            ],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every diagnostic with a message of this crate's.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(LibraryApiNotice::Trashed {
        path: ".ctxloom/content/bundles/v2/demo".to_string(),
        slot: ".ctxloom/state/trash/0195f1e2-0000-7000-8000-000000000000/".to_string(),
    })]
}

#[cfg(test)]
#[path = "notice.test.rs"]
mod tests;
