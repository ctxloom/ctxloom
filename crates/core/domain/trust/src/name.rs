//! A trust's resource name, `projects/{harp}/trust` (D2, S58: a singleton
//! sub-resource of its project, AIP-156), and its stream root.

use std::fmt;

use sessions::ProjectName;

/// The singleton's segment under its project.
const TRUST: &str = "trust";

/// `projects/{harp}/trust`.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct TrustName {
    project: ProjectName,
}

impl TrustName {
    /// The trust of the project `project`.
    pub fn of(project: ProjectName) -> TrustName {
        TrustName { project }
    }

    /// `name` read as `projects/{harp}/trust`; none for any other shape.
    pub fn parse(name: &str) -> Option<TrustName> {
        let project = name.strip_suffix(TRUST)?.strip_suffix('/')?;
        ProjectName::parse(project).ok().map(TrustName::of)
    }

    /// The project it is the trust of.
    pub fn project(&self) -> &ProjectName {
        &self.project
    }

    /// Its stream's root: the UUIDv5 of the name, as every ctxloom root is
    /// ([`sessions::root_of`]), so a harp's trust is always the same stream
    /// (harps never change, S42).
    pub fn root(&self) -> angzarr_embedded::v1::Uuid {
        trust_root(&self.to_string())
    }
}

impl fmt::Display for TrustName {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}/{TRUST}", self.project)
    }
}

/// `projects/{harp}/trust`, the trust of the project `harp` names.
pub fn trust_name(project: &ProjectName) -> String {
    TrustName::of(project.clone()).to_string()
}

/// The stream root of the trust named `name`: the UUIDv5 of the name.
pub fn trust_root(name: &str) -> angzarr_embedded::v1::Uuid {
    angzarr_embedded::v1::Uuid::from_bytes(*sessions::root_of(name).as_bytes())
}

#[cfg(test)]
#[path = "name.test.rs"]
mod tests;
