//! Why the wiring could not do what it was asked.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};
use ctxloom_store_sqlite::StoreOpenError;

/// This crate's en-US messages.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// Open for extension: a new variant is not a breaking change.
#[derive(Clone, PartialEq, Eq, Debug)]
#[non_exhaustive]
pub enum LibError {
    /// The home variable is not set (or is empty).
    NoHome { variable: String },
    /// A session id that is not a UUID.
    BadSession { session: String, detail: String },
    /// No runtime could be started to open the store.
    NoRuntime { detail: String },
    /// The project's store would not open.
    Store(StoreOpenError),
    /// The project could not be named (S54).
    Project(Report),
    /// The user's one daemon serves another ctxloom home than this
    /// command's: one daemon per OS user, never a second.
    DaemonOtherHome { home: String, daemon_home: String },
    /// A read in a ctxloom project nothing has named yet: only `run` and
    /// `init` mint a project id (owner ruling 2026-11-13), so a read is
    /// refused and writes nothing.
    ProjectUnnamed { root: String },
}

impl cli_support::ExitClass for LibError {
    /// A read in a project nothing has named is a decline (20); every other
    /// wiring error failed (10).
    fn exit(&self) -> cli_support::Exit {
        match self {
            LibError::ProjectUnnamed { .. } => cli_support::Exit::REFUSED,
            _ => cli_support::Exit::FAILED,
        }
    }
}

impl fmt::Display for LibError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for LibError {}

impl Diagnostic for LibError {
    fn code(&self) -> &str {
        match self {
            LibError::NoHome { .. } => code!("no-home"),
            LibError::BadSession { .. } => code!("bad-session"),
            LibError::NoRuntime { .. } => code!("no-runtime"),
            LibError::Store(_) => code!("store"),
            LibError::Project(cause) => cause.code(),
            LibError::DaemonOtherHome { .. } => code!("daemon-other-home"),
            LibError::ProjectUnnamed { .. } => code!("project-unnamed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            LibError::NoHome { variable } => vec![("variable", variable.as_str().into())],
            LibError::BadSession { session, detail } => vec![
                ("session", session.as_str().into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
            LibError::NoRuntime { detail } => vec![("ext_err_txt", detail.as_str().into())],
            LibError::Store(cause) => vec![("error", Arg::Nested(Box::new(cause.clone())))],
            LibError::Project(cause) => cause.args(),
            LibError::DaemonOtherHome { home, daemon_home } => vec![
                ("home", home.as_str().into()),
                ("daemon_home", daemon_home.as_str().into()),
            ],
            LibError::ProjectUnnamed { root } => vec![("root", root.as_str().into())],
        }
    }
}

/// One value of every error here, for a catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(LibError::NoHome {
            variable: "HOME".to_string(),
        }),
        Box::new(LibError::BadSession {
            session: "x".to_string(),
            detail: "invalid length".to_string(),
        }),
        Box::new(LibError::NoRuntime {
            detail: "too many open files".to_string(),
        }),
        Box::new(LibError::DaemonOtherHome {
            home: "/home/u/.ctxloom-other".to_string(),
            daemon_home: "/home/u".to_string(),
        }),
        Box::new(LibError::ProjectUnnamed {
            root: "/work/app".to_string(),
        }),
        Box::new(LibError::Store(StoreOpenError::Open(
            ctxloom_store_sqlite::OpenError::Unplaced {
                path: "/home/u/.ctxloom/projects/p/ctxloom.db".to_string(),
                detail: "no place on disk".to_string(),
            },
        ))),
        Box::new(LibError::Store(StoreOpenError::UnsafeProject(
            project_dir::ProjectDirError::IdSeparator {
                id: "../escape".to_string(),
            },
        ))),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
