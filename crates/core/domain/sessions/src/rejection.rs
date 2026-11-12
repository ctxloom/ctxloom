//! Why a command to a session, or to a name's claim, was refused.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

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
    /// A release of a name nobody holds.
    NoClaim,
    /// A release by a session that does not hold the name.
    NotHolder { harp: String },
    /// A stop ordered by a child (Go's I2); `depth` is the requester's.
    ChildCannotStop { depth: u64 },
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
    /// A resume of a name no session holds (owner ruling 115).
    SessionUnknown { harp: String },
    /// A resume of a session `session remove` destroyed.
    SessionDestroyed { harp: String },
    /// A resume of a running session: its lock is held, or its last
    /// incarnation has not ended.
    SessionRunning { harp: String },
    /// A resume of a session whose engine bound no native session.
    NoNativeSession { harp: String },
    /// A resume on another engine than the session's.
    EngineMismatch {
        harp: String,
        engine: String,
        asked: String,
    },
}

/// Its key and args, never English (angzarr decision 14.37).
impl fmt::Display for SessionRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
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
            SessionRejection::NoClaim => code!("no-claim"),
            SessionRejection::NotHolder { .. } => code!("not-holder"),
            SessionRejection::ChildCannotStop { .. } => code!("child-cannot-stop"),
            SessionRejection::NotOwner { .. } => code!("not-owner"),
            SessionRejection::SessionClosed { .. } => code!("session-closed"),
            SessionRejection::OwnerClosed { .. } => code!("owner-closed"),
            SessionRejection::NoOpenIncarnation { .. } => code!("no-open-incarnation"),
            SessionRejection::DelegationLimit { .. } => code!("delegation-limit"),
            SessionRejection::UntrustedTarget { .. } => code!("untrusted-target"),
            SessionRejection::UrgencyUnsupported { .. } => code!("urgency-unsupported"),
            SessionRejection::SessionUnknown { .. } => code!("session-unknown"),
            SessionRejection::SessionDestroyed { .. } => code!("session-destroyed"),
            SessionRejection::SessionRunning { .. } => code!("session-running"),
            SessionRejection::NoNativeSession { .. } => code!("no-native-session"),
            SessionRejection::EngineMismatch { .. } => code!("engine-mismatch"),
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
            | SessionRejection::NoNativeSession { harp } => vec![("harp", harp.as_str().into())],
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
            SessionRejection::EngineMismatch {
                harp,
                engine,
                asked,
            } => vec![
                ("harp", harp.as_str().into()),
                ("engine", engine.as_str().into()),
                ("asked", asked.as_str().into()),
            ],
            SessionRejection::HarpInvalid { harp, reason } => vec![
                ("harp", harp.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            SessionRejection::NameMalformed { name, reason } => vec![
                ("name", name.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
            SessionRejection::ChildCannotStop { depth } => vec![("depth", (*depth).into())],
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
        Box::new(SessionRejection::NoClaim),
        Box::new(SessionRejection::NotHolder { harp: harp() }),
        Box::new(SessionRejection::ChildCannotStop { depth: 1 }),
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
        Box::new(SessionRejection::SessionUnknown { harp: harp() }),
        Box::new(SessionRejection::SessionDestroyed { harp: harp() }),
        Box::new(SessionRejection::SessionRunning { harp: harp() }),
        Box::new(SessionRejection::NoNativeSession { harp: harp() }),
        Box::new(SessionRejection::EngineMismatch {
            harp: harp(),
            engine: "claude-code".to_string(),
            asked: "opencode".to_string(),
        }),
    ]
}
