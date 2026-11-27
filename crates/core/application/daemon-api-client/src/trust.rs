//! The client's half of a project's trust (step 8 U1): the user's grant and
//! revoke, the trust domain's commands through the coordinator's `HandleSync`
//! on projector `trust-index`, and `GetTrust` and `ListTrusts` as a
//! [`Caller`] asks them. Nothing stamps yet (D13): the CLIENT sets the actor
//! and both etags; the daemon's half is `daemon-api`'s `trust`.

use daemon_api_contract::TRUST_INDEX;
use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use sessions::ProjectName;
use trust::{GrantTrust, RevokeTrust, TrustName, TRUST_DOMAIN};

use crate::caller::{on_daemon, Caller};
use crate::client::CallError;

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
