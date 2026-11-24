//! Why a command to a session, or to a name's claim, was refused.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum SessionRejection {
    /// A mint on a stream that already holds a session.
    SessionExists { harp: String },
    /// A command to a stream holding no session.
    NoSession,
    /// A harp that cannot be a session's name; `reason` is one of `empty`,
    /// `directory`, `separator`, `control`, `whitespace` (Go's
    /// `harp.Validate`).
    HarpInvalid { harp: String, reason: String },
    /// A resource name that is not one (owner, 2026-11-12, slice 2 B3):
    /// `reason` is `shape`, or the harp grammar's for the segment.
    NameMalformed { name: String, reason: String },
    /// A claim on a name another session holds.
    NamesTaken { harp: String },
    /// A claim on a project harp another project holds (S54).
    ProjectNameTaken { harp: String },
    /// A registration of a project that has no harp yet.
    ProjectNotNamed { project_id: String },
    /// A recheck of a registered project whose repository's root commit is
    /// not the one registered (owner, 2026-11-12): `project` is the
    /// registered project's resource name.
    ProjectIdentityChanged {
        project: String,
        root: String,
        registered_commit: String,
        found_commit: String,
    },
    /// A recheck that expected another harp than the registered project's.
    ProjectHarpMismatch { project: String, expected: String },
    /// A release of a name nobody holds.
    NoClaim,
    /// A release by a session that does not hold the name.
    NotHolder { harp: String },
    /// A command from a session that is not the target's owner (D25, S-f);
    /// `requester` is its stable id.
    NotOwner { requester: String },
    /// A message, a clear or a wake to a closed session (K1).
    SessionClosed { harp: String },
    /// A child launched by a closed owner.
    OwnerClosed { harp: String },
    /// A child launched by an owner with no incarnation running.
    NoOpenIncarnation { harp: String },
    /// A child past the owner's delegation limit.
    DelegationLimit { limit: u64, open: u64 },
    /// A child in another repository that has not passed the trust check.
    UntrustedTarget { root: String },
    /// An urgency the recipient's engine cannot honour (S-c).
    UrgencyUnsupported { urgency: String },
    /// A wake while another is outstanding (the terminal wake).
    WakeOutstanding { nonce: String },
    /// A resume of a name no session holds (owner ruling 115).
    SessionUnknown { harp: String },
    /// A resume of a session `session remove` destroyed.
    SessionDestroyed { harp: String },
    /// A resume of a running session: its lock is held, or its last
    /// incarnation has not ended.
    SessionRunning { harp: String },
    /// A resume of a session whose engine bound no native session.
    NoNativeSession { harp: String },
    /// A restart of a session with no incarnation open (Stage 2a).
    NoRunToRestart { harp: String },
    /// A resume on another engine than the session's.
    EngineMismatch {
        harp: String,
        engine: String,
        asked: String,
    },
    /// A resume asked for another workspace axis than the session's.
    WorkspaceMismatch {
        harp: String,
        workspace: String,
        asked: String,
    },
    /// A re-host of a child re-hosted `limit` times or more in the last
    /// hour (S47): `recent` is how many.
    RehostBudgetSpent {
        harp: String,
        limit: u32,
        recent: u32,
    },
    /// A removal of a session whose lock does not prove its owner ended,
    /// without `--even-if-live` (O2); `reason` says why.
    SessionLive { harp: String, reason: String },
    /// A removal of a session marked keep (O2).
    SessionKept { harp: String },
    /// A removal within the transcript retention (O2); `detail` says how.
    WithinRetention { harp: String, detail: String },
    /// A removal of a session whose held lock keeps its directory from
    /// being moved (D18, Windows), `--even-if-live` or not; `lock` is its
    /// path.
    SessionLockHeld { harp: String, lock: String },
    /// A removal a spared or skipped worktree keeps from being whole (O2).
    KeepsSession {
        harp: String,
        spared: u64,
        skipped: u64,
    },
}

/// Its key and args, never English (angzarr decision 14.37).
impl fmt::Display for SessionRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl Diagnostic for SessionRejection {
    fn code(&self) -> &str {
        match self {
            SessionRejection::SessionExists { .. } => code!("session-exists"),
            SessionRejection::NoSession => code!("no-session"),
            SessionRejection::HarpInvalid { .. } => code!("harp-invalid"),
            SessionRejection::NameMalformed { .. } => code!("name-malformed"),
            SessionRejection::NamesTaken { .. } => code!("names-taken"),
            SessionRejection::ProjectNameTaken { .. } => code!("project-name-taken"),
            SessionRejection::ProjectNotNamed { .. } => code!("project-not-named"),
            SessionRejection::ProjectIdentityChanged { .. } => code!("project-identity-changed"),
            SessionRejection::ProjectHarpMismatch { .. } => code!("project-harp-mismatch"),
            SessionRejection::NoClaim => code!("no-claim"),
            SessionRejection::NotHolder { .. } => code!("not-holder"),
            SessionRejection::NotOwner { .. } => code!("not-owner"),
            SessionRejection::SessionClosed { .. } => code!("session-closed"),
            SessionRejection::OwnerClosed { .. } => code!("owner-closed"),
            SessionRejection::NoOpenIncarnation { .. } => code!("no-open-incarnation"),
            SessionRejection::DelegationLimit { .. } => code!("delegation-limit"),
            SessionRejection::UntrustedTarget { .. } => code!("untrusted-target"),
            SessionRejection::UrgencyUnsupported { .. } => code!("urgency-unsupported"),
            SessionRejection::WakeOutstanding { .. } => code!("wake-outstanding"),
            SessionRejection::SessionUnknown { .. } => code!("session-unknown"),
            SessionRejection::SessionDestroyed { .. } => code!("session-destroyed"),
            SessionRejection::SessionRunning { .. } => code!("session-running"),
            SessionRejection::NoNativeSession { .. } => code!("no-native-session"),
            SessionRejection::NoRunToRestart { .. } => code!("no-run-to-restart"),
            SessionRejection::EngineMismatch { .. } => code!("engine-mismatch"),
            SessionRejection::WorkspaceMismatch { .. } => code!("workspace-mismatch"),
            SessionRejection::RehostBudgetSpent { .. } => code!("rehost-budget-spent"),
            SessionRejection::SessionLive { .. } => code!("session-live"),
            SessionRejection::SessionKept { .. } => code!("session-kept"),
            SessionRejection::WithinRetention { .. } => code!("within-retention"),
            SessionRejection::KeepsSession { .. } => code!("keeps-session"),
            SessionRejection::SessionLockHeld { .. } => code!("session-lock-held"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            SessionRejection::SessionExists { harp }
            | SessionRejection::NamesTaken { harp }
            | SessionRejection::ProjectNameTaken { harp }
            | SessionRejection::NotHolder { harp }
            | SessionRejection::SessionClosed { harp }
            | SessionRejection::OwnerClosed { harp }
            | SessionRejection::NoOpenIncarnation { harp }
            | SessionRejection::SessionUnknown { harp }
            | SessionRejection::SessionDestroyed { harp }
            | SessionRejection::SessionRunning { harp }
            | SessionRejection::SessionKept { harp }
            | SessionRejection::NoRunToRestart { harp }
            | SessionRejection::NoNativeSession { harp } => vec![("harp", harp.as_str().into())],
            SessionRejection::SessionLive { harp, reason }
            | SessionRejection::HarpInvalid { harp, reason } => vec![
                ("harp", harp.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            SessionRejection::SessionLockHeld { harp, lock } => vec![
                ("harp", harp.as_str().into()),
                ("lock", lock.as_str().into()),
            ],
            SessionRejection::WithinRetention { harp, detail } => vec![
                ("harp", harp.as_str().into()),
                ("detail", detail.as_str().into()),
            ],
            SessionRejection::KeepsSession {
                harp,
                spared,
                skipped,
            } => vec![
                ("harp", harp.as_str().into()),
                ("spared", (*spared).into()),
                ("skipped", (*skipped).into()),
            ],
            SessionRejection::NotOwner { requester } => {
                vec![("requester", requester.as_str().into())]
            }
            SessionRejection::DelegationLimit { limit, open } => {
                vec![("limit", (*limit).into()), ("open", (*open).into())]
            }
            SessionRejection::UntrustedTarget { root } => vec![("root", root.as_str().into())],
            SessionRejection::UrgencyUnsupported { urgency } => {
                vec![("urgency", urgency.as_str().into())]
            }
            SessionRejection::WakeOutstanding { nonce } => vec![("nonce", nonce.as_str().into())],
            SessionRejection::EngineMismatch {
                harp,
                engine,
                asked,
            } => vec![
                ("harp", harp.as_str().into()),
                ("engine", engine.as_str().into()),
                ("asked", asked.as_str().into()),
            ],
            SessionRejection::WorkspaceMismatch {
                harp,
                workspace,
                asked,
            } => vec![
                ("harp", harp.as_str().into()),
                ("workspace", workspace.as_str().into()),
                ("asked", asked.as_str().into()),
            ],
            SessionRejection::RehostBudgetSpent {
                harp,
                limit,
                recent,
            } => vec![
                ("harp", harp.as_str().into()),
                ("limit", u64::from(*limit).into()),
                ("recent", u64::from(*recent).into()),
            ],
            SessionRejection::NameMalformed { name, reason } => vec![
                ("name", name.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            SessionRejection::ProjectNotNamed { project_id } => {
                vec![("project_id", project_id.as_str().into())]
            }
            SessionRejection::ProjectIdentityChanged {
                project,
                root,
                registered_commit,
                found_commit,
            } => vec![
                ("project", project.as_str().into()),
                ("root", root.as_str().into()),
                ("registered_commit", registered_commit.as_str().into()),
                ("found_commit", found_commit.as_str().into()),
            ],
            SessionRejection::ProjectHarpMismatch { project, expected } => vec![
                ("project", project.as_str().into()),
                ("expected", expected.as_str().into()),
            ],
            SessionRejection::NoSession | SessionRejection::NoClaim => Vec::new(),
        }
    }
}

/// This crate's en-US messages, its name first: what an edge's catalog
/// embeds to render its diagnostics.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every rejection, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let harp = || "amber-swift-owl".to_string();
    vec![
        Box::new(SessionRejection::SessionExists { harp: harp() }),
        Box::new(SessionRejection::NoSession),
        Box::new(SessionRejection::HarpInvalid {
            harp: "a/b".to_string(),
            reason: "separator".to_string(),
        }),
        Box::new(SessionRejection::NameMalformed {
            name: "projects/calm-otter/owl".to_string(),
            reason: "shape".to_string(),
        }),
        Box::new(SessionRejection::NamesTaken { harp: harp() }),
        Box::new(SessionRejection::ProjectNameTaken { harp: harp() }),
        Box::new(SessionRejection::ProjectNotNamed {
            project_id: "calm-otter".to_string(),
        }),
        Box::new(SessionRejection::ProjectIdentityChanged {
            project: "projects/calm-otter".to_string(),
            root: "/work/calm".to_string(),
            registered_commit: "a".repeat(40),
            found_commit: "b".repeat(40),
        }),
        Box::new(SessionRejection::ProjectHarpMismatch {
            project: "projects/calm-otter".to_string(),
            expected: "far-heron".to_string(),
        }),
        Box::new(SessionRejection::NoClaim),
        Box::new(SessionRejection::NotHolder { harp: harp() }),
        Box::new(SessionRejection::NotOwner {
            requester: "0b0e5f4e-7c1d-4f6a-9d3e-2a1b0c9d8e7f".to_string(),
        }),
        Box::new(SessionRejection::SessionClosed { harp: harp() }),
        Box::new(SessionRejection::OwnerClosed { harp: harp() }),
        Box::new(SessionRejection::NoOpenIncarnation { harp: harp() }),
        Box::new(SessionRejection::DelegationLimit { limit: 2, open: 2 }),
        Box::new(SessionRejection::UntrustedTarget {
            root: "/work/other".to_string(),
        }),
        Box::new(SessionRejection::UrgencyUnsupported {
            urgency: "steer".to_string(),
        }),
        Box::new(SessionRejection::WakeOutstanding {
            nonce: "0123456789abcdef".to_string(),
        }),
        Box::new(SessionRejection::SessionUnknown { harp: harp() }),
        Box::new(SessionRejection::SessionDestroyed { harp: harp() }),
        Box::new(SessionRejection::SessionRunning { harp: harp() }),
        Box::new(SessionRejection::NoNativeSession { harp: harp() }),
        Box::new(SessionRejection::NoRunToRestart { harp: harp() }),
        Box::new(SessionRejection::EngineMismatch {
            harp: harp(),
            engine: "claude-code".to_string(),
            asked: "opencode".to_string(),
        }),
        Box::new(SessionRejection::WorkspaceMismatch {
            harp: harp(),
            workspace: "worktree".to_string(),
            asked: "none".to_string(),
        }),
        Box::new(SessionRejection::RehostBudgetSpent {
            harp: harp(),
            limit: 60,
            recent: 60,
        }),
        Box::new(SessionRejection::SessionLive {
            harp: harp(),
            reason: "its lock is held: the owner is alive and running".to_string(),
        }),
        Box::new(SessionRejection::SessionKept { harp: harp() }),
        Box::new(SessionRejection::WithinRetention {
            harp: harp(),
            detail: "it ended 2026-11-18T03:00:00Z".to_string(),
        }),
        Box::new(SessionRejection::KeepsSession {
            harp: harp(),
            spared: 1,
            skipped: 0,
        }),
        Box::new(SessionRejection::SessionLockHeld {
            harp: harp(),
            lock: "C:/work/.ctxloom/sessions/amber-swift-owl/session.lock".to_string(),
        }),
    ]
}
