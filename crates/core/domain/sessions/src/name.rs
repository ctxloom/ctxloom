//! The resource names a session and a project are addressed by, and the
//! stream roots they derive (owner, 2026-11-12, slice 2 B3/re-root).
//!
//! A session is `projects/{project}/sessions/{session}` and a project
//! `projects/{project}`, each segment a harp (the grammar is
//! [`validate`](crate::validate)'s, not AIP-122's charset). This is the ONE
//! place they are formatted and parsed. A stream's root is the UUIDv5 of its
//! resource name in [`NAMESPACE`], so the name and the root always agree and
//! either finds the other without a lookup.

use std::fmt;

use crate::{validate, SessionRejection};

/// The namespace every stream root of ctxloom is a UUIDv5 in (minted once,
/// 2026-11-12). It NEVER changes: every root in every store (a session's, a
/// name's claim, a project's) is derived from it, so a new value would
/// orphan every stream that exists.
pub const NAMESPACE: uuid::Uuid = uuid::Uuid::from_u128(0x1473_f25e_0851_4e01_8741_8105_5551_414f);

const PROJECTS: &str = "projects";
const SESSIONS: &str = "sessions";

/// The UUIDv5 of `name` in [`NAMESPACE`].
pub fn root_of(name: &str) -> uuid::Uuid {
    let _ = name;
    uuid::Uuid::nil()
}

fn wire(id: uuid::Uuid) -> angzarr_embedded::v1::Uuid {
    angzarr_embedded::v1::Uuid::from_bytes(*id.as_bytes())
}

/// The root of the stream keyed by the project id `project_id` (the
/// `projects` aggregate, which mints the project's harp and so cannot be
/// rooted on it): the UUIDv5 of the id in [`NAMESPACE`]. An id holds no
/// `/`, so this is never a resource name's root.
pub fn project_id_root(project_id: &str) -> angzarr_embedded::v1::Uuid {
    wire(root_of(project_id))
}

/// `projects/{project}`.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct ProjectName {
    project: String,
}

/// `projects/{project}/sessions/{session}`.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct SessionName {
    project: String,
    session: String,
}

/// `name` refused: `reason` is `shape`, or the harp grammar's reason for
/// the segment that is no harp.
fn malformed(name: &str, reason: &str) -> SessionRejection {
    SessionRejection::NameMalformed {
        name: name.to_string(),
        reason: reason.to_string(),
    }
}

/// `segment` of `name`, a harp, or `name` refused for it.
fn segment(name: &str, segment: &str) -> Result<String, SessionRejection> {
    match validate(segment) {
        Ok(()) => Ok(segment.to_string()),
        Err(SessionRejection::HarpInvalid { reason, .. }) => Err(malformed(name, &reason)),
        Err(other) => Err(other),
    }
}

impl ProjectName {
    /// The project named by the harp `project`.
    pub fn new(project: &str) -> Result<ProjectName, SessionRejection> {
        validate(project)?;
        Ok(ProjectName {
            project: project.to_string(),
        })
    }

    /// `name` read as `projects/{project}`.
    pub fn parse(name: &str) -> Result<ProjectName, SessionRejection> {
        let _ = segment;
        Err(malformed(name, "red"))
    }

    /// The project's harp.
    pub fn harp(&self) -> &str {
        &self.project
    }

    /// The session `session` of this project.
    pub fn session(&self, session: &str) -> Result<SessionName, SessionRejection> {
        SessionName::new(&self.project, session)
    }

    /// The UUIDv5 of the name.
    pub fn id(&self) -> uuid::Uuid {
        root_of(&self.to_string())
    }

    /// The name's stream root.
    pub fn root(&self) -> angzarr_embedded::v1::Uuid {
        wire(self.id())
    }
}

impl fmt::Display for ProjectName {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let _ = (PROJECTS, &self.project);
        write!(f, "")
    }
}

impl SessionName {
    /// The session `session` of the project `project`, both harps.
    pub fn new(project: &str, session: &str) -> Result<SessionName, SessionRejection> {
        validate(project)?;
        validate(session)?;
        Ok(SessionName {
            project: project.to_string(),
            session: session.to_string(),
        })
    }

    /// `name` read as `projects/{project}/sessions/{session}`.
    pub fn parse(name: &str) -> Result<SessionName, SessionRejection> {
        Err(malformed(name, "red"))
    }

    /// The project's harp.
    pub fn project_harp(&self) -> &str {
        &self.project
    }

    /// The session's own harp.
    pub fn harp(&self) -> &str {
        &self.session
    }

    /// The project's name.
    pub fn project(&self) -> ProjectName {
        ProjectName {
            project: self.project.clone(),
        }
    }

    /// The UUIDv5 of the name: the session's stable id.
    pub fn id(&self) -> uuid::Uuid {
        root_of(&self.to_string())
    }

    /// The stable id's text (hyphenated), as the session's records carry it.
    pub fn uid(&self) -> String {
        self.id().hyphenated().to_string()
    }

    /// The name's stream root: the session's, and its name claim's.
    pub fn root(&self) -> angzarr_embedded::v1::Uuid {
        wire(self.id())
    }
}

impl fmt::Display for SessionName {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let _ = (SESSIONS, &self.session);
        write!(f, "")
    }
}

#[cfg(test)]
#[path = "name.test.rs"]
mod tests;
