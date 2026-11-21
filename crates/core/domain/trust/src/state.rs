//! A project's trust, folded from its events.

/// The trust domain's name: every trust stream's `Cover.domain`.
pub const TRUST_DOMAIN: &str = "trust";

/// A project's trust as its stream holds it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Trusted {
    /// `projects/{harp}/trust`.
    pub name: String,
    /// The project's id.
    pub project_id: String,
    pub trusted: bool,
    /// Who granted or revoked it last.
    pub user: String,
}
