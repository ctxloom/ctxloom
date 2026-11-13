//! A refusal of the daemon's API (S11): a typed refusal message and the
//! gRPC code its `(refusal_status)` declares, read from the generated
//! constant (the daemon's own refusals, [`daemon_proto::Refusal`]) or from
//! the kernel's rejection (a domain's, as its generated layer refused it).
//! No status is spelled here.

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
    NameInvalid {
        name: String,
        reason: String,
    },
    SessionNotFound {
        name: String,
    },
    ProjectNotFound {
        name: String,
    },
    ProjectUnknown {
        cwd: String,
    },
    DaemonHosting {
        runs: u64,
        harps: Vec<String>,
    },
    /// A configured age (`session_purge_age`) that is neither an age nor a
    /// date.
    AgeInvalid {
        key: String,
        value: String,
    },
    /// A project's configuration that does not read.
    ConfigUnreadable {
        root: String,
        detail: String,
    },
}

impl Diagnostic for ApiRejection {
    fn code(&self) -> &str {
        match self {
            ApiRejection::NameInvalid { .. } => code!("name-invalid"),
            ApiRejection::SessionNotFound { .. } => code!("session-not-found"),
            ApiRejection::ProjectNotFound { .. } => code!("project-not-found"),
            ApiRejection::ProjectUnknown { .. } => code!("project-unknown"),
            ApiRejection::DaemonHosting { .. } => code!("daemon-hosting"),
            ApiRejection::AgeInvalid { .. } => code!("age-invalid"),
            ApiRejection::ConfigUnreadable { .. } => code!("config-unreadable"),
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
            ApiRejection::AgeInvalid { key, value } => {
                vec![
                    ("key", key.as_str().into()),
                    ("value", value.as_str().into()),
                ]
            }
            ApiRejection::ConfigUnreadable { root, detail } => vec![
                ("root", root.as_str().into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
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
    /// A removal's (`DeleteSession`'s `RemovalRefused`), in the words of
    /// the use case that refused it.
    Removal(RemovalRejection),
}

impl Rejection {
    /// What a person is told.
    pub fn report(&self) -> Report {
        match self {
            Rejection::Api(api) => Report::of(api),
            Rejection::Sessions(sessions) => Report::of(sessions),
            Rejection::Removal(removal) => Report::of(removal),
        }
    }
}

/// Why a removal was refused, as a person is told it: the session's own
/// refusal (its lock, its keep, its retention: sessions-application's
/// words), or a worktree that keeps it (isolation-application's).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RemovalRejection {
    Session(sessions_application::Refusal),
    KeepsSession {
        harp: String,
        spared: u64,
        skipped: u64,
    },
}

impl RemovalRejection {
    /// `refused`'s reason, for the session `harp`; none for a reason this
    /// crate does not know.
    pub fn of(refused: &v1::RemovalRefused, harp: &str) -> Option<RemovalRejection> {
        use sessions_application::Refusal;
        let harp = harp.to_string();
        Some(match v1::RemovalRefusal::try_from(refused.reason).ok()? {
            v1::RemovalRefusal::NotProvenDead => {
                RemovalRejection::Session(Refusal::NotProvenDead {
                    harp,
                    reason: refused.detail.clone(),
                    verb: sessions_application::REMOVE_VERB.to_string(),
                })
            }
            v1::RemovalRefusal::Kept => RemovalRejection::Session(Refusal::Kept { harp }),
            v1::RemovalRefusal::Retained => RemovalRejection::Session(Refusal::Retained {
                harp,
                detail: refused.detail.clone(),
            }),
            v1::RemovalRefusal::WorktreesKeep => RemovalRejection::KeepsSession {
                harp,
                spared: refused.spared,
                skipped: refused.skipped,
            },
            v1::RemovalRefusal::Unspecified => return None,
        })
    }
}

impl Diagnostic for RemovalRejection {
    fn code(&self) -> &str {
        match self {
            RemovalRejection::Session(refusal) => refusal.code(),
            // isolation-application's, its words in its catalog.
            RemovalRejection::KeepsSession { .. } => "isolation-application-keeps-session",
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            RemovalRejection::Session(refusal) => refusal.args(),
            RemovalRejection::KeepsSession {
                harp,
                spared,
                skipped,
            } => vec![
                ("harp", harp.as_str().into()),
                ("spared", (*spared).into()),
                ("skipped", (*skipped).into()),
            ],
        }
    }
}

impl fmt::Display for RemovalRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for RemovalRejection {}

/// A refused removal is a decline (ruling 152).
impl ExitClass for RemovalRejection {
    fn exit(&self) -> Exit {
        Exit::REFUSED
    }
}

/// The rejection a typed refusal is, by its type; `None` for one this
/// crate does not know.
pub fn rejection_of(refusal: &Any) -> Option<Rejection> {
    use sessions_proto::v1 as wire;
    let api = |rejection| Some(Rejection::Api(rejection));
    let ours = |rejection| Some(Rejection::Sessions(rejection));
    let name = refusal.type_url.rsplit('/').next().unwrap_or_default();
    match name {
        "ctxloom.v1.NameInvalid" => decode::<v1::NameInvalid>(refusal).and_then(|r| {
            api(ApiRejection::NameInvalid {
                name: r.name,
                reason: r.reason,
            })
        }),
        "ctxloom.v1.SessionNotFound" => decode::<v1::SessionNotFound>(refusal)
            .and_then(|r| api(ApiRejection::SessionNotFound { name: r.name })),
        "ctxloom.v1.ProjectNotFound" => decode::<v1::ProjectNotFound>(refusal)
            .and_then(|r| api(ApiRejection::ProjectNotFound { name: r.name })),
        "ctxloom.v1.ProjectUnknown" => decode::<v1::ProjectUnknown>(refusal)
            .and_then(|r| api(ApiRejection::ProjectUnknown { cwd: r.cwd })),
        "ctxloom.v1.DaemonHosting" => decode::<v1::DaemonHosting>(refusal).and_then(|r| {
            api(ApiRejection::DaemonHosting {
                runs: r.runs,
                harps: r.harps,
            })
        }),
        "ctxloom.v1.AgeInvalid" => decode::<v1::AgeInvalid>(refusal).and_then(|r| {
            api(ApiRejection::AgeInvalid {
                key: r.key,
                value: r.value,
            })
        }),
        "ctxloom.v1.ConfigUnreadable" => decode::<v1::ConfigUnreadable>(refusal).and_then(|r| {
            api(ApiRejection::ConfigUnreadable {
                root: r.root,
                detail: r.detail,
            })
        }),
        "ctxloom.v1.RemovalRefused" => decode::<v1::RemovalRefused>(refusal).and_then(|r| {
            let harp = r
                .plan
                .as_ref()
                .map(|plan| plan.harp.clone())
                .filter(|harp| !harp.is_empty())
                .unwrap_or_else(|| r.name.rsplit('/').next().unwrap_or_default().to_string());
            RemovalRejection::of(&r, &harp).map(Rejection::Removal)
        }),
        "ctxloom.sessions.v1.ProjectIdentityChanged" => {
            decode::<wire::ProjectIdentityChanged>(refusal).and_then(|r| {
                ours(sessions::SessionRejection::ProjectIdentityChanged {
                    project: r.project,
                    root: r.root,
                    registered_commit: r.registered_commit,
                    found_commit: r.found_commit,
                })
            })
        }
        "ctxloom.sessions.v1.ProjectHarpMismatch" => decode::<wire::ProjectHarpMismatch>(refusal)
            .and_then(|r| {
                ours(sessions::SessionRejection::ProjectHarpMismatch {
                    project: r.project,
                    expected: r.expected,
                })
            }),
        "ctxloom.sessions.v1.UnnamedProject" => {
            decode::<wire::UnnamedProject>(refusal).and_then(|r| {
                ours(sessions::SessionRejection::ProjectNotNamed {
                    project_id: r.project_id,
                })
            })
        }
        _ => None,
    }
}

fn decode<T: Message + Default>(refusal: &Any) -> Option<T> {
    T::decode(refusal.value.as_slice()).ok()
}

/// One value of every diagnostic here.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
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
        Box::new(ApiRejection::AgeInvalid {
            key: "session_purge_age".to_string(),
            value: "soon".to_string(),
        }),
        Box::new(ApiRejection::ConfigUnreadable {
            root: "/work/calm".to_string(),
            detail: "it does not parse".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
