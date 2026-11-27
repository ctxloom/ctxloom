//! A session's calls for the terminal wake (owner ruling 2026-11-18): what
//! its ctxloom MCP server asks of the daemon for its OWN session (watch it,
//! issue and disarm its wake), and what its prompt and tool hooks record
//! (the messages they delivered, the wake a prompt was). Each write is its
//! own stream's command, keyed by the session and what it names, so a
//! redelivery is a DUPLICATE.

use daemon_ports::{Called, DaemonApi, Daemons, SessionSignals};
use daemon_proto::v1;
use sessions::{DeliverMessages, DisarmWake, IssueWake, SessionName};

use crate::caller::{on_daemon, Caller};
use crate::client::CallError;
use daemon_api_contract::{ApiRejection, Rejection};

impl<D: Daemons> Caller<D> {
    /// This caller's own session; the user has none to watch or wake.
    fn own(&self) -> Result<&SessionName, CallError> {
        self.session().ok_or_else(|| {
            CallError::Refused(Rejection::Api(ApiRejection::NameInvalid {
                name: String::new(),
                reason: "no session".to_string(),
            }))
        })
    }

    /// `WatchSession` of this caller's own session.
    pub async fn watch_own(&self) -> Result<Box<dyn SessionSignals>, CallError> {
        let request = v1::WatchSessionRequest {
            name: self.own()?.to_string(),
        };
        on_daemon!(self, |api| api.watch_session(request.clone()).await)
    }

    /// Its wake `nonce` issued, keyed `wake:<session>:<nonce>`; whether
    /// this call applied it.
    pub async fn issue_wake(&self, nonce: &str) -> Result<bool, CallError> {
        let me = self.own()?;
        let key = format!("wake:{}:{nonce}", me.uid());
        let command = IssueWake {
            nonce: nonce.to_string(),
        };
        let (_, applied) = on_daemon!(self, |api| self.write(api, me, &command, &key).await)?;
        Ok(applied)
    }

    /// Its wake `nonce` disarmed (nothing if it is not outstanding), keyed
    /// `disarm:<session>:<nonce>`.
    pub async fn disarm_wake(&self, nonce: &str) -> Result<(), CallError> {
        let me = self.own()?;
        let key = format!("disarm:{}:{nonce}", me.uid());
        let command = DisarmWake {
            nonce: nonce.to_string(),
        };
        on_daemon!(self, |api| self.write(api, me, &command, &key).await)?;
        Ok(())
    }

    /// `DeliverHookContext` of this caller's own session (10c, the lean
    /// hook RPC): all its hook `carried_by` carries, in ONE call: its
    /// pending messages of `urgencies` (with the wake `nonce` its prompt
    /// is, empty for none), offered (marking nothing delivered: the hook
    /// acknowledges after it printed), then what it is
    /// owed, within `budget` characters (0: the whole), at `cwd`.
    pub async fn hook_context(
        &self,
        carried_by: &str,
        (urgencies, nonce): (&[&str], &str),
        budget: usize,
        cwd: &str,
    ) -> Result<v1::HookContext, CallError> {
        let request = v1::DeliverHookContextRequest {
            name: self.own()?.to_string(),
            carried_by: carried_by.to_string(),
            urgencies: urgencies.iter().map(ToString::to_string).collect(),
            wake_nonce: nonce.to_string(),
            budget: u32::try_from(budget).unwrap_or(u32::MAX),
            caller: Some(v1::Caller {
                cwd: cwd.to_string(),
                ..self.context().clone()
            }),
        };
        on_daemon!(self, |api| api.deliver_hook_context(request.clone()).await)
    }

    /// `AcknowledgeHookDelivery` of this caller's own session (message
    /// delivery, 2026-11-19): its hook printed and flushed the context
    /// `hook_context` offered, so `ids` (its `offered`) are recorded
    /// delivered and the wake `nonce` its prompt was (empty: none)
    /// redeemed; the ids this call recorded. Idempotent.
    pub async fn acknowledge_hook_delivery(
        &self,
        ids: &[String],
        nonce: &str,
        cwd: &str,
    ) -> Result<Vec<String>, CallError> {
        let request = v1::AcknowledgeHookDeliveryRequest {
            name: self.own()?.to_string(),
            message_ids: ids.to_vec(),
            wake_nonce: nonce.to_string(),
            caller: Some(v1::Caller {
                cwd: cwd.to_string(),
                ..self.context().clone()
            }),
        };
        let acknowledged = on_daemon!(self, |api| api
            .acknowledge_hook_delivery(request.clone())
            .await)?;
        Ok(acknowledged.delivered)
    }

    /// The messages `ids` its hook delivered, and the wake `nonce` its
    /// prompt was (empty for none), keyed `deliver:<session>:<nonce>:<ids,
    /// comma-joined>`; whether this call applied it.
    pub async fn deliver(&self, ids: &[String], nonce: &str) -> Result<bool, CallError> {
        let me = self.own()?;
        let key = format!("deliver:{}:{nonce}:{}", me.uid(), ids.join(","));
        let command = DeliverMessages {
            message_ids: ids.to_vec(),
            wake_nonce: nonce.to_string(),
        };
        let (_, applied) = on_daemon!(self, |api| self.write(api, me, &command, &key).await)?;
        Ok(applied)
    }
}
