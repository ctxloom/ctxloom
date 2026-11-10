//! The session records port: the writer of `.ctxloom/sessions/<harp>/`.
//!
//! File-level and format-free: it puts bytes at a relative path under a
//! session's directory and reads them back. What the files hold (the
//! metadata, the records, the transcript) is the projector's to decide.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

/// A session's directory in the project, file by file.
pub trait SessionRecords: Send {
    /// The absolute path of `harp`'s directory.
    fn dir(&self, harp: &str) -> String;

    /// Make `rel` hold `bytes`, creating the directories above it.
    fn write(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError>;

    /// Add `bytes` to the end of `rel`, creating it (and the directories
    /// above it) when absent.
    fn append(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError>;

    /// What `rel` holds; `None` when it is not there.
    fn read(&mut self, harp: &str, rel: &str) -> Result<Option<Vec<u8>>, RecordsError>;

    /// Whether anything is at `harp`'s directory (defect 3: a mint never
    /// takes a harp whose directory exists, however the store came to have
    /// no claim on it). One that cannot be looked at counts as there.
    ///
    /// RED (defect 3): a stub that finds nothing.
    fn exists(&mut self, harp: &str) -> bool {
        let _ = harp;
        false
    }
}

/// Whether `rel` stays inside a session's directory: non-empty, relative,
/// slash-separated, with no `.`, `..` or empty part. An implementation checks
/// every path it is given with this.
pub fn check_rel(rel: &str) -> Result<(), RecordsError> {
    let sound = !rel.contains('\\')
        && !rel.is_empty()
        && rel
            .split('/')
            .all(|part| !part.is_empty() && part != "." && part != "..");
    if sound {
        Ok(())
    } else {
        Err(RecordsError::BadPath {
            rel: rel.to_string(),
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RecordsError {
    /// A path that is not inside a session's directory.
    BadPath { rel: String },
    /// The system refused.
    Io {
        harp: String,
        rel: String,
        ext_err_txt: String,
    },
}

impl fmt::Display for RecordsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for RecordsError {
    fn code(&self) -> &str {
        match self {
            RecordsError::BadPath { .. } => code!("records-bad-path"),
            RecordsError::Io { .. } => code!("records-io"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            RecordsError::BadPath { rel } => vec![("rel", rel.as_str().into())],
            RecordsError::Io {
                harp,
                rel,
                ext_err_txt,
            } => vec![
                ("harp", harp.as_str().into()),
                ("rel", rel.as_str().into()),
                ("ext_err_txt", ext_err_txt.as_str().into()),
            ],
        }
    }
}

#[cfg(test)]
#[path = "records.test.rs"]
mod tests;
