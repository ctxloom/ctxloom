//! A project's trust (step 8 U1, ruling 142): `GetTrust`'s preview (what
//! trusting the project would allow, read at its REGISTERED root, and an
//! etag of what it showed), `ListTrusts` (the projects trusted now, from the
//! daemon's warm trust index), and the user's grant and revoke, the trust
//! domain's commands through the coordinator's `HandleSync` on projector
//! `trust-index`.
//!
//! Nothing stamps yet (D13): the CLIENT sets the actor and both etags. The
//! grant's `current_etag` is the etag of a `GetTrust` the client asks just
//! before it sends, so what is trusted is what the preview showed, and a
//! change in between is refused `trust-plan-stale` by the aggregate.

use daemon_api_contract::{ApiError, ApiRefusal};
use daemon_proto::v1;
use trust::TrustName;

/// What trusting a project would allow, read as a process at its root
/// would read it: its project layer's project-only settings and a project
/// `ctxloom` bundle (`bundles/ctxloom`). The root composes it.
pub trait Trusts: Send + Sync {
    /// The settings the project at `root` holds back until it is trusted.
    fn settings(&self, root: &str) -> Result<Vec<v1::TrustSetting>, ApiError>;
}

/// The etag of a preview's `settings`: the UUIDv5 of each path and value in
/// order (sorted by the preview), each prefixed by its length so no two
/// lists say the same text, so the same settings always give the same tag
/// and any change gives another. Not a secret: it names nothing.
pub fn etag_of(settings: &[v1::TrustSetting]) -> String {
    let said: String = settings
        .iter()
        .map(|s| format!("{}:{}{}:{}", s.path.len(), s.path, s.value.len(), s.value))
        .collect();
    sessions::root_of(&format!("ctxloom:trust-etag:{said}"))
        .hyphenated()
        .to_string()
}

/// `settings`, sorted by their path: the order a preview shows them in.
pub(crate) fn sorted(mut settings: Vec<v1::TrustSetting>) -> Vec<v1::TrustSetting> {
    settings.sort_by(|a, b| a.path.cmp(&b.path).then_with(|| a.value.cmp(&b.value)));
    settings
}

/// `name`, refused as no trust's name.
pub(crate) fn not_a_trust(name: &str) -> ApiError {
    ApiError::from(ApiRefusal::of(&v1::NameInvalid {
        name: name.to_string(),
        reason: "shape".to_string(),
    }))
}

/// Where the daemon reads a trust: what a project's would allow, and the
/// warm index of what is trusted (its kernel's, S32).
#[derive(Clone)]
pub struct Trusting {
    pub trusts: std::sync::Arc<dyn Trusts>,
    pub index: trust_index::TrustIndexes,
}

impl<E: run_ports::Edge> crate::Api<E> {
    /// Where its trusts are read; none, `GetTrust` and `ListTrusts` fail.
    fn trusting_ports(&self) -> Result<&Trusting, ApiError> {
        self.trusting.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon reads no trust".to_string(),
        })
    }

    /// Whether the user trusts the project `project_id` now (step 8 U2):
    /// what a launch the daemon makes in it is planned with, so a trusted
    /// project's own settings apply and an untrusted one's are held back
    /// and refuse the launch that reads them. A daemon that reads no trust
    /// trusts nothing.
    pub(crate) fn trusts(&self, project_id: &str) -> bool {
        self.trusting
            .as_ref()
            .is_some_and(|trusting| trusting.index.index().trusts(project_id))
    }

    /// `GetTrust` (step 8 U1): the trust `request.name` names
    /// (`projects/{harp}/trust`), what trusting it would allow read at the
    /// project's REGISTERED root, the etag of that, and whether, by whom and
    /// since when it is trusted.
    pub async fn get_trust(&self, request: v1::GetTrustRequest) -> Result<v1::Trust, ApiError> {
        let name = TrustName::parse(&request.name).ok_or_else(|| not_a_trust(&request.name))?;
        let trusting = self.trusting_ports()?;
        self.parts.hosting.fresh().await;
        let row = self
            .parts
            .index
            .index()
            .project_named_by(name.project().harp());
        let root = row.map(|row| row.root).filter(|root| !root.is_empty());
        let root = root.ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                name: name.project().to_string(),
            }))
        })?;
        // Its tree observed before its settings are read there (step 10 U11).
        self.observed(Some((name.project().clone(), root.as_str())))
            .await?;
        let settings = sorted(trusting.trusts.settings(&root)?);
        let held = trusting.index.index().named(&name.to_string());
        let mut trust = held.map(|row| row.resource()).unwrap_or_default();
        trust.name = name.to_string();
        trust.etag = etag_of(&settings);
        trust.settings = settings;
        Ok(trust)
    }

    /// `ListTrusts` (step 8 U1): the projects the user trusts now, by name;
    /// a revoked trust is not listed.
    pub async fn list_trusts(
        &self,
        request: v1::ListTrustsRequest,
    ) -> Result<v1::ListTrustsResponse, ApiError> {
        let _ = request;
        let trusting = self.trusting_ports()?;
        self.parts.hosting.fresh().await;
        let trusts = trusting
            .index
            .index()
            .trusted_now()
            .iter()
            .map(trust_index::TrustRow::resource)
            .collect();
        Ok(v1::ListTrustsResponse {
            trusts,
            ..v1::ListTrustsResponse::default()
        })
    }
}

#[cfg(test)]
#[path = "trust.test.rs"]
mod tests;
