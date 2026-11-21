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

    /// A domain's typed refusal `refusal`, made here before any command is
    /// sent (a hand-off's checks, S10), under `status`, the status its
    /// message declares.
    pub fn typed(
        refusal: &angzarr_embedded::markup::TypedRefusal,
        status: angzarr_embedded::markup::BusinessStatus,
    ) -> ApiRefusal {
        ApiRefusal {
            code: angzarr_embedded::angzarr_router::error::GrpcCode::from(status) as i32,
            refusal: refusal.packed().clone(),
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
    /// Failed, in a diagnostic of its own (step 10a U2: a library that does
    /// not read): its code and its flattened args, which the
    /// client is told so it says it in that diagnostic's words.
    Diagnosed {
        code: String,
        args: Vec<v1::NeedArg>,
    },
    /// Not served by this daemon (yet): `CreateRun` for a TERMINAL run until
    /// U5 builds it; answered UNIMPLEMENTED.
    Unimplemented { method: String },
}

impl ApiError {
    /// A failure of `cause`, said in its own words.
    pub(crate) fn failed(cause: impl fmt::Debug) -> ApiError {
        ApiError::Failed {
            detail: format!("{cause:?}"),
        }
    }
}

impl ApiError {
    /// A failure said in `cause`'s own code and args.
    pub fn diagnosed(cause: &dyn Diagnostic) -> ApiError {
        ApiError::Diagnosed {
            code: cause.code().to_string(),
            args: run_application::client::flat_args(cause),
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
            ApiError::Diagnosed { code, .. } => code,
            ApiError::Unimplemented { .. } => code!("unimplemented"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ApiError::Refused(refused) => {
                vec![("ext_err_txt", refused.refusal.type_url.as_str().into())]
            }
            ApiError::Failed { detail } => vec![("ext_err_txt", detail.as_str().into())],
            ApiError::Diagnosed { args, .. } => args
                .iter()
                .map(|arg| (arg.name.as_str(), arg.value.as_str().into()))
                .collect(),
            ApiError::Unimplemented { method } => vec![("method", method.as_str().into())],
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
    /// A session whose hooks have delivered no transcript yet.
    TranscriptEmpty {
        name: String,
    },
    ProjectNotFound {
        name: String,
    },
    /// A run the daemon hosts no run under (U4).
    RunNotFound {
        name: String,
    },
    ProjectUnknown {
        cwd: String,
    },
    DaemonHosting {
        runs: u64,
        harps: Vec<String>,
    },
    /// A call to a daemon asked to drain (OD-54): its stamp and the
    /// sessions of the runs it still hosts.
    Draining {
        daemon: String,
        harps: Vec<String>,
    },
    /// A call of a client whose version is not the daemon's (owner,
    /// 2026-11-18): both stamps (`unknown` for a client that named none) and
    /// the client's kind (`cli`, `mcp`, `hook`).
    ClientVersion {
        client: String,
        daemon: String,
        kind: String,
    },
    /// A call that would create files under the daemon's home once that
    /// home's ctxloom directory is gone (owner, 2026-11-17): `home`.
    HomeGone {
        home: String,
    },
    /// A configured age (`session_purge_age`) that is neither an age nor a
    /// date.
    AgeInvalid {
        key: String,
        value: String,
    },
    /// An apply whose etag is not a number, so no preview answered it
    /// (step 10b U3).
    EtagInvalid {
        etag: String,
    },
    /// An apply whose preview's etag no longer matches what stands now
    /// (`v1::PlanStale`, step 10c U11): `seen` the preview's, `now` the
    /// current one.
    PlanStale {
        seen: String,
        now: String,
    },
    /// An apply that does not answer each need its preview asked
    /// (`v1::NeedsUnanswered`, step 10c U11).
    NeedsUnanswered {
        needs: Vec<String>,
    },
    /// A project's configuration that does not read.
    ConfigUnreadable {
        root: String,
        detail: String,
    },
    /// A launch of a child another launch of it is making now (its launch
    /// lock held, T6).
    LaunchInProgress {
        harp: String,
    },
    /// A wake to a primary nobody can wake: no ctxloom MCP server watches
    /// it (as with `--no-context`), so it takes an inform alone (the
    /// terminal wake; client-side: the domain refuses it untold).
    WakeInformOnly {
        harp: String,
    },
    /// An ask asked by a session the daemon hosts no run of (step 8 U4).
    SessionNotHosted {
        name: String,
    },
    /// An answer naming a harp that has two or more asks waiting (S29): its
    /// asks' names, oldest first (client-side: no service method refuses it).
    ApprovalAmbiguous {
        harp: String,
        asks: Vec<String>,
    },
    /// An answer naming a harp that has no ask waiting (Q-B).
    ApprovalNoneWaiting {
        harp: String,
    },
    /// An `allow-always` whose rule was written, and whose ask was settled
    /// before its answer reached it (Q3): the rule stays.
    ApprovalRuleKept {
        ask_id: String,
        how: String,
        rule: String,
        path: String,
    },
}

impl Diagnostic for ApiRejection {
    fn code(&self) -> &str {
        match self {
            ApiRejection::NameInvalid { .. } => code!("name-invalid"),
            ApiRejection::SessionNotFound { .. } => code!("session-not-found"),
            ApiRejection::TranscriptEmpty { .. } => code!("transcript-empty"),
            ApiRejection::ProjectNotFound { .. } => code!("project-not-found"),
            ApiRejection::RunNotFound { .. } => code!("run-not-found"),
            ApiRejection::ProjectUnknown { .. } => code!("project-unknown"),
            ApiRejection::DaemonHosting { .. } => code!("daemon-hosting"),
            ApiRejection::Draining { .. } => code!("draining"),
            ApiRejection::ClientVersion { .. } => code!("client-version"),
            ApiRejection::HomeGone { .. } => code!("home-gone"),
            ApiRejection::AgeInvalid { .. } => code!("age-invalid"),
            ApiRejection::EtagInvalid { .. } => code!("etag-invalid"),
            ApiRejection::PlanStale { .. } => code!("plan-stale"),
            ApiRejection::NeedsUnanswered { .. } => code!("needs-unanswered"),
            ApiRejection::ConfigUnreadable { .. } => code!("config-unreadable"),
            ApiRejection::LaunchInProgress { .. } => code!("launch-in-progress"),
            ApiRejection::WakeInformOnly { .. } => code!("wake-inform-only"),
            ApiRejection::SessionNotHosted { .. } => code!("session-not-hosted"),
            ApiRejection::ApprovalAmbiguous { .. } => code!("approval-ambiguous"),
            ApiRejection::ApprovalNoneWaiting { .. } => code!("approval-none-waiting"),
            ApiRejection::ApprovalRuleKept { .. } => code!("approval-rule-kept"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ApiRejection::NameInvalid { name, reason } => vec![
                ("name", name.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            ApiRejection::SessionNotFound { name }
            | ApiRejection::TranscriptEmpty { name }
            | ApiRejection::ProjectNotFound { name }
            | ApiRejection::RunNotFound { name }
            | ApiRejection::SessionNotHosted { name } => {
                vec![("name", name.as_str().into())]
            }
            ApiRejection::ApprovalAmbiguous { harp, asks } => vec![
                ("harp", harp.as_str().into()),
                ("count", (asks.len() as u64).into()),
                ("asks", asks.join(", ").into()),
            ],
            ApiRejection::ApprovalNoneWaiting { harp } => vec![("harp", harp.as_str().into())],
            ApiRejection::ApprovalRuleKept {
                ask_id,
                how,
                rule,
                path,
            } => vec![
                ("ask_id", ask_id.as_str().into()),
                ("how", how.as_str().into()),
                ("rule", rule.as_str().into()),
                ("path", path.as_str().into()),
            ],
            ApiRejection::ProjectUnknown { cwd } => vec![("cwd", cwd.as_str().into())],
            ApiRejection::LaunchInProgress { harp } | ApiRejection::WakeInformOnly { harp } => {
                vec![("harp", harp.as_str().into())]
            }
            ApiRejection::DaemonHosting { runs, harps } => {
                vec![("runs", (*runs).into()), ("harps", harps.join(", ").into())]
            }
            ApiRejection::Draining { daemon, harps } => vec![
                ("daemon", daemon.as_str().into()),
                ("harps", harps.join(", ").into()),
            ],
            ApiRejection::ClientVersion {
                client,
                daemon,
                kind,
            } => vec![
                ("client", client.as_str().into()),
                ("daemon", daemon.as_str().into()),
                ("kind", kind.as_str().into()),
            ],
            ApiRejection::HomeGone { home } => vec![("home", home.as_str().into())],
            ApiRejection::AgeInvalid { key, value } => {
                vec![
                    ("key", key.as_str().into()),
                    ("value", value.as_str().into()),
                ]
            }
            ApiRejection::EtagInvalid { etag } => vec![("etag", etag.as_str().into())],
            ApiRejection::PlanStale { seen, now } => {
                vec![("seen", seen.as_str().into()), ("now", now.as_str().into())]
            }
            ApiRejection::NeedsUnanswered { needs } => vec![("needs", needs.join(", ").into())],
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
    /// A removal's (`DeleteSession`'s `RemovalRefusal`), in the words of
    /// the use case that refused it.
    Removal(RemovalRejection),
    /// The runs domain's (a child's launch judged before its hand-off is
    /// written, S10).
    Runs(runs::RunRejection),
    /// The trust domain's (a grant whose preview is stale, step 8 U1).
    Trust(trust::TrustRejection),
    /// The approvals domain's (an answer refused, step 8 U4).
    Approvals(approvals::ApprovalsRejection),
    /// The library domain's (a read of a tree outside git, or one git
    /// ignores: step 10a U2).
    Library(library_tree::LibraryTreeRejection),
    /// A library read's use case's (E5): its own diagnostic, by the typed
    /// refusal's type.
    LibraryRead(Report),
    /// A use case's refusal over the plan's fatal findings (a strict
    /// `materialize`, step 10c U10: `SetupFatalFindings`): its own
    /// diagnostic, the command's exit 21.
    FatalFindings(Report),
}

impl Rejection {
    /// What a person is told.
    pub fn report(&self) -> Report {
        match self {
            Rejection::Api(api) => Report::of(api),
            Rejection::Sessions(sessions) => Report::of(sessions),
            Rejection::Removal(removal) => Report::of(removal),
            Rejection::Runs(runs) => Report::of(runs),
            Rejection::Trust(trust) => Report::of(trust),
            Rejection::Approvals(approvals) => Report::of(approvals),
            Rejection::Library(library) => Report::of(library),
            Rejection::LibraryRead(read) | Rejection::FatalFindings(read) => read.clone(),
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
    pub fn of(refused: &v1::RemovalRefusal, harp: &str) -> Option<RemovalRejection> {
        use sessions_application::Refusal;
        let harp = harp.to_string();
        Some(
            match v1::removal_refusal::Reason::try_from(refused.reason).ok()? {
                v1::removal_refusal::Reason::NotProvenDead => {
                    RemovalRejection::Session(Refusal::NotProvenDead {
                        harp,
                        reason: refused.detail.clone(),
                        verb: sessions_application::REMOVE_VERB.to_string(),
                    })
                }
                v1::removal_refusal::Reason::Kept => {
                    RemovalRejection::Session(Refusal::Kept { harp })
                }
                v1::removal_refusal::Reason::Retained => {
                    RemovalRejection::Session(Refusal::Retained {
                        harp,
                        detail: refused.detail.clone(),
                    })
                }
                v1::removal_refusal::Reason::LockHeld => {
                    RemovalRejection::Session(Refusal::LockHeld {
                        harp,
                        lock: refused.detail.clone(),
                    })
                }
                v1::removal_refusal::Reason::WorktreesKeep => RemovalRejection::KeepsSession {
                    harp,
                    spared: refused.spared,
                    skipped: refused.skipped,
                },
                v1::removal_refusal::Reason::Unspecified => return None,
            },
        )
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
        "ctxloom.v1.TranscriptEmpty" => decode::<v1::TranscriptEmpty>(refusal)
            .and_then(|r| api(ApiRejection::TranscriptEmpty { name: r.name })),
        "ctxloom.v1.ProjectNotFound" => decode::<v1::ProjectNotFound>(refusal)
            .and_then(|r| api(ApiRejection::ProjectNotFound { name: r.name })),
        "ctxloom.v1.SessionNotHosted" => decode::<v1::SessionNotHosted>(refusal)
            .and_then(|r| api(ApiRejection::SessionNotHosted { name: r.name })),
        "ctxloom.v1.RunNotFound" => decode::<v1::RunNotFound>(refusal)
            .and_then(|r| api(ApiRejection::RunNotFound { name: r.name })),
        "ctxloom.v1.ProjectUnknown" => decode::<v1::ProjectUnknown>(refusal)
            .and_then(|r| api(ApiRejection::ProjectUnknown { cwd: r.cwd })),
        "ctxloom.v1.DaemonHosting" => decode::<v1::DaemonHosting>(refusal).and_then(|r| {
            api(ApiRejection::DaemonHosting {
                runs: r.runs,
                harps: r.harps,
            })
        }),
        "ctxloom.v1.DaemonDraining" => decode::<v1::DaemonDraining>(refusal).and_then(|r| {
            api(ApiRejection::Draining {
                daemon: r.version,
                harps: r.harps,
            })
        }),
        "ctxloom.v1.ClientVersionMismatch" => client_version(refusal).and_then(api),
        "ctxloom.v1.DaemonHomeGone" => decode::<v1::DaemonHomeGone>(refusal)
            .and_then(|r| api(ApiRejection::HomeGone { home: r.home })),
        "ctxloom.v1.AgeInvalid" => decode::<v1::AgeInvalid>(refusal).and_then(|r| {
            api(ApiRejection::AgeInvalid {
                key: r.key,
                value: r.value,
            })
        }),
        "ctxloom.v1.EtagInvalid" => decode::<v1::EtagInvalid>(refusal)
            .and_then(|r| api(ApiRejection::EtagInvalid { etag: r.etag })),
        "ctxloom.v1.PlanStale" => decode::<v1::PlanStale>(refusal).and_then(|r| {
            api(ApiRejection::PlanStale {
                seen: r.seen,
                now: r.now,
            })
        }),
        "ctxloom.v1.NeedsUnanswered" => decode::<v1::NeedsUnanswered>(refusal)
            .and_then(|r| api(ApiRejection::NeedsUnanswered { needs: r.need_ids })),
        "ctxloom.v1.ConfigUnreadable" => decode::<v1::ConfigUnreadable>(refusal).and_then(|r| {
            api(ApiRejection::ConfigUnreadable {
                root: r.root,
                detail: r.detail,
            })
        }),
        "ctxloom.v1.RemovalRefusal" => decode::<v1::RemovalRefusal>(refusal).and_then(|r| {
            let harp = r
                .plan
                .as_ref()
                .map(|plan| plan.harp.clone())
                .filter(|harp| !harp.is_empty())
                .unwrap_or_else(|| r.name.rsplit('/').next().unwrap_or_default().to_string());
            RemovalRejection::of(&r, &harp).map(Rejection::Removal)
        }),
        "ctxloom.sessions.v1.ProjectIdentityMismatch" => {
            decode::<wire::ProjectIdentityMismatch>(refusal).and_then(|r| {
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
        // A setup use case's (step 10c U10): its own diagnostic, by its
        // code and args.
        "ctxloom.v1.SetupDeclined" => decode::<v1::SetupDeclined>(refusal)
            .map(|r| Rejection::LibraryRead(cause_report(r.cause))),
        "ctxloom.v1.SetupFatalFindings" => decode::<v1::SetupFatalFindings>(refusal)
            .map(|r| Rejection::FatalFindings(cause_report(r.cause))),
        _ => domain_rejection(refusal)
            .or_else(|| crate::library_refusals::rejection(refusal).map(Rejection::LibraryRead)),
    }
}

/// The diagnostic a refusal's `cause` is, by its code and its args.
fn cause_report(cause: Option<v1::Cause>) -> Report {
    let cause = cause.unwrap_or_default();
    run_application::client::report_of(&cause.code, &cause.args)
}

/// A setup use case's error (step 10c U10: `materialize`, `manage
/// uninstall`, `doctor --fix`), of the class `exit` it says of itself, as
/// the call answers it: a decline is its typed refusal (`SetupDeclined`),
/// fatal findings theirs (`SetupFatalFindings`), each carrying the
/// diagnostic as its cause; a failure is said in its own words.
pub fn setup_error(error: &dyn Diagnostic, exit: Exit) -> ApiError {
    let cause = || {
        Some(v1::Cause {
            code: error.code().to_string(),
            args: run_application::client::flat_args(error),
        })
    };
    if exit == Exit::FATAL_FINDINGS {
        return ApiError::Refused(ApiRefusal::of(&v1::SetupFatalFindings { cause: cause() }));
    }
    if exit == Exit::REFUSED {
        return ApiError::Refused(ApiRefusal::of(&v1::SetupDeclined { cause: cause() }));
    }
    ApiError::diagnosed(error)
}

/// `refusal`, as the domain message its type names, typed: the first of
/// `types` it is.
macro_rules! typed_as {
    ($refusal:expr, [$($t:ty),* $(,)?]) => {{
        let refusal = $refusal;
        None
            $(.or_else(|| {
                angzarr_embedded::markup::decode_message::<$t>(refusal)
                    .ok()
                    .flatten()
                    .map(|message| angzarr_embedded::markup::TypedRefusal::of(&message))
            }))*
    }};
}

/// A refusal of the sessions or the runs domain's commands a client's write
/// (a child verb's, S2) or a hand-off's checks (S10) was answered with, as
/// that domain's own rejection; none for any other.
fn domain_rejection(refusal: &Any) -> Option<Rejection> {
    let sessions: Option<angzarr_embedded::markup::TypedRefusal> = typed_as!(
        refusal,
        [
            sessions::SessionUnknown,
            sessions::NoSession,
            sessions::SessionIsClosed,
            sessions::OwnerIsClosed,
            sessions::NotOwner,
            sessions::NoOpenIncarnation,
            sessions::DelegationLimit,
            sessions::UntrustedTarget,
            sessions::UrgencyUnsupported,
        ]
    );
    if let Some(typed) = sessions {
        return sessions::SessionRejection::try_from(&typed)
            .ok()
            .map(Rejection::Sessions);
    }
    let ran: Option<angzarr_embedded::markup::TypedRefusal> = typed_as!(
        refusal,
        [
            runs::NoCredential,
            runs::FileOnlyCredential,
            runs::UnknownEngine,
            runs::UntrustedProjectSettings,
            runs::LaunchRefusals,
        ]
    );
    if let Some(ran) = ran.and_then(|typed| runs::RunRejection::try_from(&typed).ok()) {
        return Some(Rejection::Runs(ran));
    }
    let trusted: Option<angzarr_embedded::markup::TypedRefusal> =
        typed_as!(refusal, [trust::PlanStale]);
    if let Some(trusted) = trusted.and_then(|typed| trust::TrustRejection::try_from(&typed).ok()) {
        return Some(Rejection::Trust(trusted));
    }
    let answered: Option<angzarr_embedded::markup::TypedRefusal> = typed_as!(
        refusal,
        [
            approvals::ApprovalUnknown,
            approvals::ApprovalIsSettled,
            approvals::OptionNotOffered,
            approvals::MessageNotRelayed,
        ]
    );
    if let Some(answered) =
        answered.and_then(|typed| approvals::ApprovalsRejection::try_from(&typed).ok())
    {
        return Some(Rejection::Approvals(answered));
    }
    let library: Option<angzarr_embedded::markup::TypedRefusal> = typed_as!(
        refusal,
        [
            library_tree::PlanRejected,
            // A write's (step 10b U3).
            library_tree::ResourceExists,
            library_tree::NoSuchResource,
            library_tree::BuiltinReadOnly,
            library_tree::OneChangePerCall,
            library_tree::PlanStale,
        ]
    );
    library
        .and_then(|typed| library_tree::LibraryTreeRejection::try_from(&typed).ok())
        .map(Rejection::Library)
}

/// A library read's refusal (step 10a U2) as the daemon answers it: the
/// library domain's own typed refusal, under the status its message
/// declares, so one code reaches the person, read or write.
pub fn library_refusal(rejection: &library_tree::LibraryTreeRejection) -> ApiRefusal {
    use library_tree::{LibraryTreeRejection as Why, Refusal};
    fn typed<M: angzarr_embedded::markup::DomainMessage + Refusal>(message: &M) -> ApiRefusal {
        ApiRefusal::typed(
            &angzarr_embedded::markup::TypedRefusal::of(message),
            M::REFUSAL_STATUS,
        )
    }
    match rejection.clone() {
        Why::PlanRejected { code, args } => typed(&library_tree::PlanRejected { code, args }),
        Why::PlanStale { path, seen, now } => typed(&library_tree::PlanStale { path, seen, now }),
        Why::ResourceExists { path } => typed(&library_tree::ResourceExists { path }),
        Why::NoSuchResource { path } => typed(&library_tree::NoSuchResource { path }),
        Why::BuiltinReadOnly { bundle } => typed(&library_tree::BuiltinReadOnly { bundle }),
        Why::OneChangePerCall { paths } => typed(&library_tree::OneChangePerCall { paths }),
        Why::NoSuchSlot { request_id } => typed(&library_tree::NoSuchSlot { request_id }),
        Why::SlotNotDue {
            request_id,
            age_days,
        } => typed(&library_tree::SlotNotDue {
            request_id,
            age_days,
        }),
    }
}

/// A `ClientVersionMismatch`'s rejection: both stamps (`unknown` for a
/// client that named none) and the client's kind.
fn client_version(refusal: &Any) -> Option<ApiRejection> {
    decode::<v1::ClientVersionMismatch>(refusal).map(|r| ApiRejection::ClientVersion {
        client: Some(r.client_version)
            .filter(|version| !version.is_empty())
            .unwrap_or_else(|| "unknown".to_string()),
        daemon: r.daemon_version,
        kind: client_kind(r.kind).to_string(),
    })
}

/// A client's kind as a person reads it.
fn client_kind(kind: i32) -> &'static str {
    match v1::client::Kind::try_from(kind) {
        Ok(v1::client::Kind::Cli) => "cli",
        Ok(v1::client::Kind::Mcp) => "mcp",
        Ok(v1::client::Kind::Hook) => "hook",
        _ => "unnamed",
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
        Box::new(ApiRejection::RunNotFound {
            name: "projects/calm-otter/runs/0195f0a4-7b1e-7000-8000-000000000001".to_string(),
        }),
        Box::new(ApiError::Unimplemented {
            method: "CreateRun(TERMINAL)".to_string(),
        }),
        Box::new(ApiRejection::TranscriptEmpty {
            name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        }),
        Box::new(ApiRejection::ProjectUnknown {
            cwd: "/tmp".to_string(),
        }),
        Box::new(ApiRejection::DaemonHosting {
            runs: 1,
            harps: vec!["brisk-copper-moth".to_string()],
        }),
        Box::new(ApiRejection::Draining {
            daemon: "v0.1.0-abc".to_string(),
            harps: vec!["brisk-copper-moth".to_string()],
        }),
        Box::new(ApiRejection::ClientVersion {
            client: "v0.1.1-def".to_string(),
            daemon: "v0.1.0-abc".to_string(),
            kind: "cli".to_string(),
        }),
        Box::new(ApiRejection::HomeGone {
            home: "/home/u/.ctxloom".to_string(),
        }),
        Box::new(ApiRejection::AgeInvalid {
            key: "session_purge_age".to_string(),
            value: "soon".to_string(),
        }),
        Box::new(ApiRejection::EtagInvalid {
            etag: "abc".to_string(),
        }),
        Box::new(ApiRejection::PlanStale {
            seen: "5d3c".to_string(),
            now: "9a1e".to_string(),
        }),
        Box::new(ApiRejection::NeedsUnanswered {
            needs: vec!["confirm".to_string()],
        }),
        Box::new(ApiRejection::ConfigUnreadable {
            root: "/work/calm".to_string(),
            detail: "it does not parse".to_string(),
        }),
        Box::new(ApiRejection::LaunchInProgress {
            harp: "brisk-copper-moth".to_string(),
        }),
        Box::new(ApiRejection::WakeInformOnly {
            harp: "amber-quiet-heron".to_string(),
        }),
        Box::new(ApiRejection::SessionNotHosted {
            name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        }),
        Box::new(ApiRejection::ApprovalAmbiguous {
            harp: "brisk-copper-moth".to_string(),
            asks: vec!["ask-tidy-wren".to_string(), "ask-pale-moth".to_string()],
        }),
        Box::new(ApiRejection::ApprovalNoneWaiting {
            harp: "brisk-copper-moth".to_string(),
        }),
        Box::new(ApiRejection::ApprovalRuleKept {
            ask_id: "ask-tidy-wren".to_string(),
            how: "expired".to_string(),
            rule: "Bash(rm -rf build/)".to_string(),
            path: "/work/calm/.ctxloom/config.yaml".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "refusal.test.rs"]
pub(crate) mod tests;

/// A library use case's error (step 10a U2) as a read answers it: an
/// adapter's failure is a failure; a decline is its typed refusal (E5).
/// Only a decline is typed (`library_refusals::typed`: every one it types
/// exits refused, which its tests hold it to).
pub(crate) fn app_error(error: &library_application::AppError) -> ApiError {
    match crate::library_refusals::typed(error) {
        Some(refused) => ApiError::Refused(refused),
        // A failure said in its own words, its code and args (step 10b
        // U9), as a deps failure is: a served import whose source is not
        // there names it, as in process.
        None => ApiError::diagnosed(error),
    }
}

/// A deps use case's error (step 10a U2, `remote list|show`, `deps list`)
/// as a read answers it: an adapter's or a file's failure is a failure in
/// its own words ([`ApiError::diagnosed`]); a read model's decline (a
/// lock or a registry that does not read) is its own typed refusal
/// (`ctxloom.v1.Deps*`, E5, `library_refusals::read_decline`); any other
/// decline (a write's, 10b) rides as `LibraryDeclined`, its cause the
/// decline's own code and args.
pub fn deps_error(error: &deps_application::AppError) -> ApiError {
    use exit_code::ExitClass;
    let declined = |d: &dyn angzarr_diagnostic::Diagnostic| {
        ApiError::Refused(ApiRefusal::of(&daemon_proto::v1::LibraryDeclined {
            cause: Some(daemon_proto::v1::Cause {
                code: d.code().to_string(),
                args: run_application::client::flat_args(d),
            }),
        }))
    };
    match error {
        deps_application::AppError::Declined(cause) => {
            crate::library_refusals::read_decline(cause.as_ref())
                .map_or_else(|| declined(cause.as_ref()), ApiError::Refused)
        }
        _ if error.exit() == exit_code::Exit::REFUSED => declined(error),
        // A failure (a clone that cannot be browsed, a file that does not
        // read) said in its own words (step 10a U2: `remote show`'s
        // `deps-application-browse`), exit 10 as in process.
        _ => ApiError::diagnosed(error),
    }
}
