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

use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use sessions::ProjectName;
use trust::{GrantTrust, RevokeTrust, TrustName, TRUST_DOMAIN};

use crate::caller::{on_daemon, Caller};
use crate::client::CallError;
use crate::refusal::{ApiError, ApiRefusal};

/// The read model a trust command's answer is projected by.
pub const TRUST_INDEX: &str = "trust-index";

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

/// What a grant or a revoke came to: the trust as the daemon has it after,
/// and whether this call recorded anything (false: it was trusted, or not,
/// already).
#[derive(Clone, Debug, PartialEq)]
pub struct Trusted {
    pub trust: v1::Trust,
    pub applied: bool,
}

/// The user's trust verbs (`ctxloom trust`, `trust --undo`, `trust list`),
/// as the CLI calls them.
#[allow(async_fn_in_trait)]
pub trait TrustWrites {
    /// `GetTrust`: this project's trust, previewed.
    async fn get_trust(&self) -> Result<v1::Trust, CallError>;

    /// `trust`: this project trusted, as the preview tagged `etag` showed
    /// it, by `user`.
    async fn grant_trust(&self, etag: &str, user: &str) -> Result<Trusted, CallError>;

    /// `trust --undo`: this project's trust taken back by `user`.
    async fn revoke_trust(&self, user: &str) -> Result<Trusted, CallError>;

    /// `trust list`: the projects trusted now.
    async fn list_trusts(&self) -> Result<v1::ListTrustsResponse, CallError>;
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

impl<D: Daemons> Caller<D> {
    /// This caller's project's trust.
    fn trust_name(&self) -> TrustName {
        TrustName::of(self.project.clone())
    }

    async fn previewed(&self, api: &D::Api) -> Result<v1::Trust, Called> {
        api.get_trust(v1::GetTrustRequest {
            name: self.trust_name().to_string(),
        })
        .await
    }

    /// `trust`'s preview: this project's trust, what it would allow, and
    /// the etag a grant names.
    pub async fn get_trust(&self) -> Result<v1::Trust, CallError> {
        on_daemon!(self, |api| self.previewed(api).await)
    }

    /// `trust list`: the projects the user trusts now.
    pub async fn list_trusts(&self) -> Result<v1::ListTrustsResponse, CallError> {
        let request = v1::ListTrustsRequest::default();
        on_daemon!(self, |api| api.list_trusts(request.clone()).await)
    }

    /// The project's id, as the daemon has it.
    async fn project_id(&self, api: &D::Api, project: &ProjectName) -> Result<String, Called> {
        Ok(api
            .get_project(v1::GetProjectRequest {
                name: project.to_string(),
            })
            .await?
            .uid)
    }

    /// `command` to this project's trust stream through the coordinator,
    /// keyed `key`, answered with the trust as the index projected it (as
    /// `GetTrust` reads it when no projection came back).
    async fn trust_write<M: angzarr_embedded::markup::DomainMessage>(
        &self,
        api: &D::Api,
        command: &M,
        key: &str,
    ) -> Result<Trusted, Called> {
        let cover = angzarr_embedded::v1::Cover {
            domain: TRUST_DOMAIN.to_string(),
            root: Some(self.trust_name().root()),
            edition: None,
        };
        let book = sessions::keyed_book(cover, command, key);
        let synced = api.handle_sync(book, vec![TRUST_INDEX.to_string()]).await?;
        let trust = match synced.projection::<v1::Trust>(TRUST_INDEX) {
            Some(trust) => trust,
            None => self.previewed(api).await?,
        };
        Ok(Trusted {
            trust,
            applied: synced.applied,
        })
    }

    async fn granted(
        &self,
        api: &D::Api,
        etag: &str,
        user: &str,
        key: &str,
    ) -> Result<Trusted, Called> {
        // What is there now, as the daemon reads it just before the grant
        // (D13: client-computed, nothing stamps yet).
        let now = self.previewed(api).await?;
        let command = GrantTrust {
            name: self.trust_name().to_string(),
            project_id: self.project_id(api, &self.project).await?,
            etag: etag.to_string(),
            current_etag: now.etag,
            actor: trust::Actor {
                user: user.to_string(),
            },
        };
        self.trust_write(api, &command, key).await
    }

    /// `trust`: this project trusted by `user`, as the preview tagged
    /// `etag` showed it; refused `trust-plan-stale` when what it would allow
    /// has changed since. Keyed by one id per call, so its retry on a new
    /// daemon is the same grant (S8).
    pub async fn grant_trust(&self, etag: &str, user: &str) -> Result<Trusted, CallError> {
        let key = format!("trust:{}", (self.ids)());
        on_daemon!(self, |api| self.granted(api, etag, user, &key).await)
    }

    async fn revoked(&self, api: &D::Api, user: &str, key: &str) -> Result<Trusted, Called> {
        let command = RevokeTrust {
            name: self.trust_name().to_string(),
            project_id: self.project_id(api, &self.project).await?,
            actor: trust::Actor {
                user: user.to_string(),
            },
        };
        self.trust_write(api, &command, key).await
    }

    /// `trust --undo`: this project's trust taken back by `user`.
    pub async fn revoke_trust(&self, user: &str) -> Result<Trusted, CallError> {
        let key = format!("untrust:{}", (self.ids)());
        on_daemon!(self, |api| self.revoked(api, user, &key).await)
    }
}

impl<D: Daemons> TrustWrites for Caller<D> {
    async fn get_trust(&self) -> Result<v1::Trust, CallError> {
        Caller::get_trust(self).await
    }

    async fn grant_trust(&self, etag: &str, user: &str) -> Result<Trusted, CallError> {
        Caller::grant_trust(self, etag, user).await
    }

    async fn revoke_trust(&self, user: &str) -> Result<Trusted, CallError> {
        Caller::revoke_trust(self, user).await
    }

    async fn list_trusts(&self) -> Result<v1::ListTrustsResponse, CallError> {
        Caller::list_trusts(self).await
    }
}

#[cfg(test)]
#[path = "trust.test.rs"]
mod tests;
