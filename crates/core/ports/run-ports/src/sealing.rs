//! The sealing port: a secret that rides a message (an environment's value,
//! an MCP server's header, a settings file's `env`) is sealed to the key of
//! the PROJECT it belongs to, so no event, log or store reader sees it in the
//! clear (owner rulings 2026-11-25 and 2026-11-27: one key per project, held
//! by the daemon, from that project's `.env`). The key is the consuming
//! code's to give: this port is what the core asks of it, and an adapter
//! holds the keys. The `field` names what a value is for (a variable's name,
//! a server's header) and is bound to the sealed value, so it cannot be moved
//! to another field.

use std::any::Any;
use std::fmt;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use coded_diagnostic::{code, Arg, Diagnostic};

/// Seals and opens a field's secret, with the key of `project` (its harp).
pub trait Sealing: Send + Sync {
    /// `plain` sealed for `field`, with `project`'s key.
    fn seal(&self, _project: &str, field: &str, plain: &[u8]) -> Result<Vec<u8>, SealingError>;

    /// What `seal` made of `field`'s value for `project`, opened.
    fn open(&self, _project: &str, field: &str, sealed: &[u8]) -> Result<Vec<u8>, SealingError>;
}

/// A seal key as the project's records name it: its id (derived from its
/// public half) and a fingerprint of it. Never the key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct KeyInfo {
    pub id: String,
    pub fingerprint: String,
}

/// A project's seal keys, opaque: the current key, the earlier ones that may
/// still open what they sealed, and the material the sealer adapter that made
/// them uses. Its `Debug` withholds the keys. Clones share the one set, and
/// with it whether its keys were announced.
#[derive(Clone)]
pub struct KeySet {
    current: KeyInfo,
    earlier: Vec<KeyInfo>,
    material: Arc<dyn Any + Send + Sync>,
    announced: Arc<AtomicBool>,
}

impl KeySet {
    /// A set of `current` and `earlier`, its `material` what the sealer that
    /// reads it understands.
    pub fn new(
        current: KeyInfo,
        earlier: Vec<KeyInfo>,
        material: Arc<dyn Any + Send + Sync>,
    ) -> KeySet {
        KeySet {
            current,
            earlier,
            material,
            announced: Arc::new(AtomicBool::new(false)),
        }
    }

    pub fn current(&self) -> &KeyInfo {
        &self.current
    }

    pub fn earlier(&self) -> &[KeyInfo] {
        &self.earlier
    }

    /// The adapter's own material, for the sealer that made it.
    pub fn material(&self) -> &Arc<dyn Any + Send + Sync> {
        &self.material
    }

    /// Whether this set is announced for the first time: true once, however
    /// many clones ask (the keys' ids are then told to the project's stream).
    pub fn announce(&self) -> bool {
        !self.announced.swap(true, Ordering::SeqCst)
    }
}

impl fmt::Debug for KeySet {
    /// The ids alone: never a key.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "KeySet(current {}, {} earlier)",
            self.current.id,
            self.earlier.len()
        )
    }
}

/// What the application knows of a project that its keys depend on: where its
/// live `.env` is (the registered root, which a git worktree shares with its
/// primary checkout) and whether the user trusts it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProjectEvidence {
    pub root: String,
    pub trusted: bool,
}

/// Where the application learns of a project (by harp or id): none for one
/// the daemon has not registered.
pub trait ProjectFacts: Send + Sync {
    fn evidence(&self, project: &str) -> Option<ProjectEvidence>;
}

/// A project's seal keys: the driven adapter reads them from where the
/// project keeps them (its `.env`), only for a trusted project. A project
/// with none refuses with [`SealingError::NoKey`], naming it and the file.
pub trait ProjectSecrets: Send + Sync {
    fn key_set(&self, project: &str, evidence: &ProjectEvidence) -> Result<KeySet, SealingError>;
}

/// Seals and opens with a [`KeySet`]: the driven adapter that holds the
/// crypto and its randomness. An id the set does not hold is
/// [`SealingError::UnknownKey`] (its `project` empty: the caller names it).
pub trait Sealer: Send + Sync {
    fn seal(&self, keys: &KeySet, field: &str, plain: &[u8]) -> Result<Vec<u8>, SealingError>;
    fn open(&self, keys: &KeySet, field: &str, sealed: &[u8]) -> Result<Vec<u8>, SealingError>;
}

/// Where a project's key ids are told to its stream (a fact through the
/// coordinator): fire and forget, a resend being a DUPLICATE.
pub trait KeysSeen: Send + Sync {
    fn seen(&self, project: &str, keys: &KeySet);
}

/// Why a value could not be sealed or opened.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SealingError {
    /// The project has no seal key (its `.env` is not there, names none, or
    /// the project is not trusted): nothing can be sealed or opened, and
    /// nothing is sent in the clear instead.
    NoKey { project: String, file: String },
    /// The key given is not a key: the project and its file, never the value.
    BadKey { project: String, file: String },
    /// The value was sealed to a key the project's set does not hold (its
    /// `.env` lost or replaced it): the key's id and the project.
    UnknownKey { project: String, key_id: String },
    /// The sealing itself failed.
    Seal { field: String, ext_err_txt: String },
    /// The value did not open: another key, another field, or damage.
    Open { field: String, ext_err_txt: String },
}

impl fmt::Display for SealingError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for SealingError {}

impl Diagnostic for SealingError {
    fn code(&self) -> &str {
        match self {
            SealingError::NoKey { .. } => code!("sealing-no-key"),
            SealingError::BadKey { .. } => code!("sealing-bad-key"),
            SealingError::UnknownKey { .. } => code!("sealing-unknown-key"),
            SealingError::Seal { .. } => code!("sealing-seal"),
            SealingError::Open { .. } => code!("sealing-open"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            SealingError::NoKey { project, file } | SealingError::BadKey { project, file } => {
                vec![
                    ("project", project.clone().into()),
                    ("file", file.clone().into()),
                ]
            }
            SealingError::UnknownKey { project, key_id } => vec![
                ("project", project.clone().into()),
                ("key_id", key_id.clone().into()),
            ],
            SealingError::Seal { field, ext_err_txt }
            | SealingError::Open { field, ext_err_txt } => vec![
                ("field", field.clone().into()),
                ("ext_err_txt", ext_err_txt.clone().into()),
            ],
        }
    }
}

/// A sealing for tests of the crates that only carry sealed values: it frames
/// the bytes reversed behind a marker, bound to the field (a project is told
/// apart by its harp or its id alike, so it binds none). It hides nothing
/// and exists under `test-support` alone.
#[cfg(any(test, feature = "test-support"))]
#[derive(Clone, Copy, Debug, Default)]
pub struct TestSealing;

#[cfg(any(test, feature = "test-support"))]
impl Sealing for TestSealing {
    fn seal(&self, _project: &str, field: &str, plain: &[u8]) -> Result<Vec<u8>, SealingError> {
        let mut sealed = format!("test-sealed:{field}:").into_bytes();
        sealed.extend(plain.iter().rev());
        Ok(sealed)
    }

    fn open(&self, _project: &str, field: &str, sealed: &[u8]) -> Result<Vec<u8>, SealingError> {
        let head = format!("test-sealed:{field}:").into_bytes();
        match sealed.strip_prefix(head.as_slice()) {
            Some(rest) => Ok(rest.iter().rev().copied().collect()),
            None => Err(SealingError::Open {
                field: field.to_string(),
                ext_err_txt: "not sealed for this field".to_string(),
            }),
        }
    }
}

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let project = "calm-otter".to_string();
    let file = "/work/demo/.env".to_string();
    let field = "ANTHROPIC_API_KEY".to_string();
    let ext_err_txt = "authentication failed".to_string();
    vec![
        Box::new(SealingError::NoKey {
            project: project.clone(),
            file: file.clone(),
        }),
        Box::new(SealingError::BadKey {
            project: project.clone(),
            file,
        }),
        Box::new(SealingError::UnknownKey {
            project,
            key_id: "k-1a2b3c4d".to_string(),
        }),
        Box::new(SealingError::Seal {
            field: field.clone(),
            ext_err_txt: ext_err_txt.clone(),
        }),
        Box::new(SealingError::Open { field, ext_err_txt }),
    ]
}

#[cfg(test)]
#[path = "sealing.test.rs"]
mod tests;
