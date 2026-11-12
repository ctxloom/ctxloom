//! Why the store could not be opened for a project: the project's id was
//! refused (the shared `project-dir` crate's check), or the opener failed
//! (`sqlite-store-opener`'s), each reported under its crate's code and
//! catalog; or the project still has a store of its own from before the one
//! store (T17), this crate's own.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use angzarr_embedded::angzarr_ports::{Arg as PortArg, Cause};
use angzarr_embedded::StoreError;
use project_dir::ProjectDirError;
use sqlite_store_opener::OpenError;

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum StoreOpenError {
    /// The project's id is not one plain name: no path was made from it.
    UnsafeProject(ProjectDirError),
    /// The opener refused or failed.
    Open(OpenError),
    /// The project still has a store of its own, at `path`, from before the
    /// one store (T17, S48): refused, nothing imported; the fix is to remove
    /// it.
    PerProjectStore { path: String },
    /// The project's directory, which lists it, could not be made.
    ProjectDirUnmade { path: String, detail: String },
}

/// This crate's en-US messages.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

impl fmt::Display for StoreOpenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for StoreOpenError {}

impl Diagnostic for StoreOpenError {
    fn code(&self) -> &str {
        match self {
            StoreOpenError::UnsafeProject(unsafe_id) => unsafe_id.static_code(),
            StoreOpenError::Open(error) => error.code(),
            StoreOpenError::PerProjectStore { .. } => code!("per-project-store"),
            StoreOpenError::ProjectDirUnmade { .. } => code!("project-dir-unmade"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            StoreOpenError::UnsafeProject(unsafe_id) => unsafe_id.args(),
            StoreOpenError::Open(error) => error.args(),
            StoreOpenError::PerProjectStore { path } => vec![("path", path.as_str().into())],
            StoreOpenError::ProjectDirUnmade { path, detail } => vec![
                ("path", path.as_str().into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
        }
    }
}

impl StoreOpenError {
    /// The error as the store's own: how the port carries it in a
    /// `StoreError`.
    pub fn into_store_error(self) -> StoreError {
        match self {
            StoreOpenError::UnsafeProject(unsafe_id) => StoreError::Unavailable(Cause::new(
                unsafe_id.static_code(),
                unsafe_id
                    .id()
                    .map(|id| ("id", PortArg::Str(id.to_string()))),
            )),
            StoreOpenError::Open(error) => error.into_store_error(),
            StoreOpenError::PerProjectStore { path } => StoreError::Unavailable(Cause::new(
                code!("per-project-store"),
                [("path", PortArg::Str(path))],
            )),
            StoreOpenError::ProjectDirUnmade { path, detail } => {
                StoreError::Unavailable(Cause::new(
                    code!("project-dir-unmade"),
                    [
                        ("path", PortArg::Str(path)),
                        ("ext_err_txt", PortArg::Str(detail)),
                    ],
                ))
            }
        }
    }
}

/// One value of every error here, the opener's included, for a catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let mut all = sqlite_store_opener::diagnostic_samples();
    all.push(Box::new(StoreOpenError::PerProjectStore {
        path: "/home/u/.ctxloom/projects/p/ctxloom.db".to_string(),
    }));
    all.push(Box::new(StoreOpenError::ProjectDirUnmade {
        path: "/home/u/.ctxloom/projects/p".to_string(),
        detail: "permission denied".to_string(),
    }));
    all
}
