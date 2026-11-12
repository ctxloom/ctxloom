//! The session and the name claim, folded from their events, and their wire
//! forms.

use angzarr_embedded::{Timestamp, WireError};

use crate::{Origin, Rotation, SessionRef};

/// The sessions domain's name: every session stream's `Cover.domain`.
pub const SESSION_DOMAIN: &str = "sessions";

/// The name claims' domain: every claim stream's `Cover.domain`.
pub const NAMES_DOMAIN: &str = "session-names";

/// The projects' domain (S54): one stream per project, holding its harp.
pub const PROJECTS_DOMAIN: &str = "projects";

/// The project harps' claim domain (S54): one stream per harp text.
pub const PROJECT_NAMES_DOMAIN: &str = "project-names";

/// The session, folded from its events. Derived values (its title, its
/// state now) are computed when read, never stored.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SessionState {
    pub harp: String,
    pub project_id: String,
    pub project_dir: String,
    pub output_dir: String,
    pub engine: String,
    pub origin: Option<Origin>,
    pub native_id: String,
    pub transcript_path: String,
    pub engine_version: String,
    /// Every binding displaced, oldest first.
    pub rotations: Vec<Rotation>,
    pub kept: bool,
    pub started_at: Timestamp,
    pub ended_at: Option<Timestamp>,
    /// The session's depth in its tree (minimal lineage, owner ruling
    /// 2026-11-04, S5 review Q2 (b)); 0 for an owner.
    pub depth: u32,
    /// The run lifeline (plan 3.1): the highest incarnation the session has
    /// owed; 0 until the first.
    pub incarnation: u32,
    /// The incarnation owed or running and not yet ended, if any.
    pub open_incarnation: Option<u32>,
    /// Set by a stop order; no incarnation is owed after it.
    pub stopped: bool,
    /// The owner a child was minted for (D26); `None` for a top-level
    /// session.
    pub owner: Option<SessionRef>,
    /// Set by `SessionClosed`, terminal (K1): no message, clear or new
    /// incarnation after it.
    pub closed: bool,
    pub closed_cause: String,
    /// The children this session holds as an owner, oldest first: launched
    /// and not yet ended, failed or forgotten (K2).
    pub children: Vec<Child>,
}

impl From<SessionState> for sessions_proto::v1::SessionState {
    fn from(value: SessionState) -> Self {
        sessions_proto::v1::SessionState {
            harp: value.harp,
            project_id: value.project_id,
            project_dir: value.project_dir,
            output_dir: value.output_dir,
            engine: value.engine,
            origin: value.origin.map_or(0, i32::from),
            native_id: value.native_id,
            transcript_path: value.transcript_path,
            engine_version: value.engine_version,
            rotations: value
                .rotations
                .into_iter()
                .map(sessions_proto::v1::Rotation::from)
                .collect(),
            kept: value.kept,
            started_at: Some(value.started_at),
            ended_at: value.ended_at,
            depth: value.depth,
            incarnation: value.incarnation,
            open_incarnation: value.open_incarnation,
            stopped: value.stopped,
            owner: value.owner.map(sessions_proto::v1::SessionRef::from),
            closed: value.closed,
            closed_cause: value.closed_cause,
            children: value
                .children
                .into_iter()
                .map(sessions_proto::v1::Child::from)
                .collect(),
        }
    }
}

impl TryFrom<sessions_proto::v1::SessionState> for SessionState {
    type Error = WireError;

    fn try_from(value: sessions_proto::v1::SessionState) -> Result<Self, WireError> {
        let origin = match value.origin {
            0 => None,
            other => Some(
                Origin::try_from(other).map_err(|value| WireError::UnknownEnum {
                    message: "ctxloom.sessions.v1.SessionState",
                    field: "origin",
                    value,
                })?,
            ),
        };
        Ok(SessionState {
            harp: value.harp,
            project_id: value.project_id,
            project_dir: value.project_dir,
            output_dir: value.output_dir,
            engine: value.engine,
            origin,
            native_id: value.native_id,
            transcript_path: value.transcript_path,
            engine_version: value.engine_version,
            rotations: value
                .rotations
                .into_iter()
                .map(Rotation::try_from)
                .collect::<Result<Vec<_>, _>>()?,
            kept: value.kept,
            started_at: value.started_at.ok_or(WireError::Missing {
                message: "ctxloom.sessions.v1.SessionState",
                field: "started_at",
            })?,
            ended_at: value.ended_at,
            depth: value.depth,
            incarnation: value.incarnation,
            open_incarnation: value.open_incarnation,
            stopped: value.stopped,
            owner: value.owner.map(SessionRef::try_from).transpose()?,
            closed: value.closed,
            closed_cause: value.closed_cause,
            children: value
                .children
                .into_iter()
                .map(Child::try_from)
                .collect::<Result<Vec<_>, _>>()?,
        })
    }
}

/// One child an owner holds (K2): the child session and the directory it
/// works in. Hand-written: no command or event carries it whole, so the
/// markup generates none.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Child {
    pub session: Option<SessionRef>,
    pub root: String,
}

impl From<Child> for sessions_proto::v1::Child {
    fn from(value: Child) -> Self {
        sessions_proto::v1::Child {
            session: value.session.map(sessions_proto::v1::SessionRef::from),
            root: value.root,
        }
    }
}

impl TryFrom<sessions_proto::v1::Child> for Child {
    type Error = WireError;

    fn try_from(value: sessions_proto::v1::Child) -> Result<Self, WireError> {
        Ok(Child {
            session: value.session.map(SessionRef::try_from).transpose()?,
            root: value.root,
        })
    }
}

/// A name's claim, folded from its events: held by a session, or free.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NameClaimState {
    pub project_id: String,
    pub harp: String,
    /// The stable id of the holder; empty while the name is free.
    pub session: String,
    /// Whether the stream has history.
    pub claimed: bool,
}

impl From<NameClaimState> for sessions_proto::v1::NameClaimState {
    fn from(value: NameClaimState) -> Self {
        sessions_proto::v1::NameClaimState {
            project_id: value.project_id,
            harp: value.harp,
            session: value.session,
            claimed: value.claimed,
        }
    }
}

impl TryFrom<sessions_proto::v1::NameClaimState> for NameClaimState {
    type Error = WireError;

    fn try_from(value: sessions_proto::v1::NameClaimState) -> Result<Self, WireError> {
        Ok(NameClaimState {
            project_id: value.project_id,
            harp: value.harp,
            session: value.session,
            claimed: value.claimed,
        })
    }
}

/// A project, folded from its events (S54): its harp, empty until named.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ProjectState {
    pub project_id: String,
    pub harp: String,
}

impl From<ProjectState> for sessions_proto::v1::ProjectState {
    fn from(value: ProjectState) -> Self {
        sessions_proto::v1::ProjectState {
            project_id: value.project_id,
            harp: value.harp,
        }
    }
}

impl TryFrom<sessions_proto::v1::ProjectState> for ProjectState {
    type Error = WireError;

    fn try_from(value: sessions_proto::v1::ProjectState) -> Result<Self, WireError> {
        Ok(ProjectState {
            project_id: value.project_id,
            harp: value.harp,
        })
    }
}

/// A project harp's claim, folded from its events: held by a project, or
/// free (never yet claimed).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ProjectNameState {
    pub harp: String,
    /// The project that holds the harp; empty while it is free.
    pub project_id: String,
}

impl From<ProjectNameState> for sessions_proto::v1::ProjectNameState {
    fn from(value: ProjectNameState) -> Self {
        sessions_proto::v1::ProjectNameState {
            harp: value.harp,
            project_id: value.project_id,
        }
    }
}

impl TryFrom<sessions_proto::v1::ProjectNameState> for ProjectNameState {
    type Error = WireError;

    fn try_from(value: sessions_proto::v1::ProjectNameState) -> Result<Self, WireError> {
        Ok(ProjectNameState {
            harp: value.harp,
            project_id: value.project_id,
        })
    }
}
