//! A refusal of the daemon's API (S11): a typed refusal message and the
//! gRPC code its `(refusal_status)` declares, read from the generated
//! constant (the daemon's own refusals, [`daemon_proto::Refusal`]) or from
//! the kernel's rejection (a domain's, as its generated layer refused it).
//! No status is spelled here.

// RED: the use cases are not built yet.
#![allow(unused_imports, dead_code, unused_variables)]

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};
use angzarr_embedded::{Any, Rejected};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use prost::Message;

/// A refused call: its code and its typed refusal.
#[derive(Clone, Debug, PartialEq)]
pub struct ApiRefusal {
    /// The `google.rpc.Code` number the refusal's status is.
    pub code: i32,
    /// The typed refusal message, packed.
    pub refusal: Any,
}

impl ApiRefusal {
    /// The daemon's own refusal `message`, under the status it declares.
    pub fn of<T: daemon_proto::Refusal>(message: &T) -> ApiRefusal {
        ApiRefusal {
            code: T::REFUSAL_STATUS,
            refusal: Any::from_msg(message).expect("a refusal encodes"),
        }
    }

    /// A domain's refusal, as the kernel rejected the command.
    pub fn rejected(rejected: &Rejected) -> ApiRefusal {
        ApiRefusal {
            code: rejected.as_coded().grpc as i32,
            refusal: rejected.refusal_object().clone(),
        }
    }
}

/// Why a use case of the API did not answer.
#[derive(Clone, Debug, PartialEq)]
pub enum ApiError {
    /// Refused: the typed refusal, answered under its status.
    Refused(ApiRefusal),
    /// Something broke (the store, the kernel): an internal failure.
    Failed { detail: String },
}

impl ApiError {
    /// A failure of `cause`, said in its own words.
    pub(crate) fn failed(cause: impl fmt::Debug) -> ApiError {
        ApiError::Failed {
            detail: format!("{cause:?}"),
        }
    }
}

impl From<ApiRefusal> for ApiError {
    fn from(refusal: ApiRefusal) -> ApiError {
        ApiError::Refused(refusal)
    }
}

impl fmt::Display for ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for ApiError {}

impl Diagnostic for ApiError {
    fn code(&self) -> &str {
        match self {
            ApiError::Refused(_) => code!("refused"),
            ApiError::Failed { .. } => code!("failed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ApiError::Refused(refused) => {
                vec![("ext_err_txt", refused.refusal.type_url.as_str().into())]
            }
            ApiError::Failed { detail } => vec![("ext_err_txt", detail.as_str().into())],
        }
    }
}

/// The daemon's own refusals, as a person is told them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ApiRejection {
    NameInvalid { name: String, reason: String },
    SessionNotFound { name: String },
    ProjectNotFound { name: String },
    ProjectUnknown { cwd: String },
    DaemonHosting { runs: u64, harps: Vec<String> },
}

impl Diagnostic for ApiRejection {
    fn code(&self) -> &str {
        match self {
            ApiRejection::NameInvalid { .. } => code!("name-invalid"),
            ApiRejection::SessionNotFound { .. } => code!("session-not-found"),
            ApiRejection::ProjectNotFound { .. } => code!("project-not-found"),
            ApiRejection::ProjectUnknown { .. } => code!("project-unknown"),
            ApiRejection::DaemonHosting { .. } => code!("daemon-hosting"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ApiRejection::NameInvalid { name, reason } => vec![
                ("name", name.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            ApiRejection::SessionNotFound { name } | ApiRejection::ProjectNotFound { name } => {
                vec![("name", name.as_str().into())]
            }
            ApiRejection::ProjectUnknown { cwd } => vec![("cwd", cwd.as_str().into())],
            ApiRejection::DaemonHosting { runs, harps } => {
                vec![("runs", (*runs).into()), ("harps", harps.join(", ").into())]
            }
        }
    }
}

impl fmt::Display for ApiRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for ApiRejection {}

/// Every refusal is a decline (ruling 152).
impl ExitClass for ApiRejection {
    fn exit(&self) -> Exit {
        Exit::REFUSED
    }
}

/// A typed refusal, as the rejection of its own crate.
#[derive(Clone, Debug, PartialEq)]
pub enum Rejection {
    /// The daemon's own.
    Api(ApiRejection),
    /// The sessions domain's (a project's registration, a name's claim).
    Sessions(sessions::SessionRejection),
}

impl Rejection {
    /// What a person is told.
    pub fn report(&self) -> Report {
        match self {
            Rejection::Api(api) => Report::of(api),
            Rejection::Sessions(sessions) => Report::of(sessions),
        }
    }
}

/// The rejection a typed refusal is, by its type; `None` for one this
/// crate does not know.
pub fn rejection_of(refusal: &Any) -> Option<Rejection> {
    let _ = refusal;
    None
}

fn decode<T: Message + Default>(refusal: &Any) -> Option<T> {
    T::decode(refusal.value.as_slice()).ok()
}

/// One value of every diagnostic here.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    let _ = decode::<v1::NameInvalid>;
    vec![
        Box::new(ApiError::Failed {
            detail: "the store".to_string(),
        }),
        Box::new(ApiError::Refused(ApiRefusal::of(&v1::ProjectNotFound {
            name: "projects/calm-otter".to_string(),
        }))),
        Box::new(ApiRejection::NameInvalid {
            name: "projects/calm-otter/x".to_string(),
            reason: "shape".to_string(),
        }),
        Box::new(ApiRejection::SessionNotFound {
            name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        }),
        Box::new(ApiRejection::ProjectNotFound {
            name: "projects/calm-otter".to_string(),
        }),
        Box::new(ApiRejection::ProjectUnknown {
            cwd: "/tmp".to_string(),
        }),
        Box::new(ApiRejection::DaemonHosting {
            runs: 1,
            harps: vec!["brisk-copper-moth".to_string()],
        }),
    ]
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
