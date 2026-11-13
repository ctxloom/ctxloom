//! A SESSION as the daemon's client (slice 3, T20): what ctxloom's MCP
//! server asks of the daemon for the session whose engine started it. Its
//! identity is the session's resource name, as the engine's environment
//! carries it (`CTXLOOM_SESSION`, s3-Q3); its project is that name's
//! (`projects/{p}`, OD-2: never resolved from a working directory, which may
//! be a checkout).
//!
//! The reads are the service's (`ListSessions`, `GetSession`,
//! `GetTranscript`, `AssembleContext`). A write is the domain's command sent
//! through the coordinator's `HandleSync` (S1 amended): its cover the
//! target's stream (the UUIDv5 of its name, B3), its `requester` the caller
//! session (an address, taken as sent: the session tree's owner rule
//! decides), its evidence computed here from the target as the daemon has
//! it (s3-Q4: `sessions_application::urgencies_of`), and its command id
//! keyed by an id minted ONCE per call, so its one retry on a new daemon
//! (S8) is a DUPLICATE. The answer is the target's session as the
//! session index projected it after the write, or, where the projection
//! did not come back (a duplicate), as `GetSession` reads it.

use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use sessions::{ClearChild, CloseSession, SendMessage, SessionName, SessionRef, Urgency};

use crate::client::CallError;
use crate::refusal::{ApiRejection, Rejection};
use crate::RequestIds;

/// Every project's sessions, as a `ListSessions` parent.
const EVERY_PROJECT: &str = "projects/-";

/// The read model a session command's answer is projected by.
const SESSION_INDEX: &str = "session-index";

/// The cause a close from a tool is recorded with.
const TOOL_CLOSE: &str = "closed by its owner";

/// `$body` (an `async` expression of `$api`, a daemon's API) on a reached
/// daemon, once more on the daemon reached again if this one could not be
/// reached (S8). A macro, not an `AsyncFn`: the futures stay `Send` (an
/// async closure's do not, rustc's limit), which the MCP server needs.
macro_rules! on_daemon {
    ($self:ident, |$api:ident| $body:expr) => {{
        let first = $self.daemons.reach().await.map_err(CallError::Unreached)?;
        let answered = {
            let $api = &first;
            $body
        };
        match answered {
            Err(Called::Unreachable { .. }) => {
                let again = $self.daemons.reach().await.map_err(CallError::Unreached)?;
                let $api = &again;
                $body.map_err(CallError::from)
            }
            answered => answered.map_err(CallError::from),
        }
    }};
}

/// A session, as the daemon's client.
pub struct Caller<D> {
    daemons: D,
    session: SessionName,
    env: v1::EnvVars,
    ids: RequestIds,
}

/// What a write came to: the target as the daemon has it after, and the
/// id the write was sent under (a message's, a clear's; a close's is its
/// session's).
#[derive(Clone, Debug, PartialEq)]
pub struct Written {
    pub session: v1::Session,
    pub id: String,
}

/// `name`, refused as no session's name.
fn not_a_name(name: &str, reason: &str) -> CallError {
    CallError::Refused(Rejection::Api(ApiRejection::NameInvalid {
        name: name.to_string(),
        reason: reason.to_string(),
    }))
}

/// The reason a name was refused.
fn reason_of(rejection: &sessions::SessionRejection) -> String {
    match rejection {
        sessions::SessionRejection::NameMalformed { reason, .. } => reason.clone(),
        sessions::SessionRejection::HarpInvalid { reason, .. } => reason.clone(),
        _ => "shape".to_string(),
    }
}

/// The urgency `word` names (`inform`, `wake`, `steer`).
pub fn urgency_of(word: &str) -> Option<Urgency> {
    match word {
        "inform" => Some(Urgency::Inform),
        "wake" => Some(Urgency::Wake),
        "steer" => Some(Urgency::Steer),
        _ => None,
    }
}

impl<D: Daemons> Caller<D> {
    /// The session `session` names (its resource name), reaching its daemon
    /// through `daemons`, with the caller's environment `env` (S7), each
    /// write's id minted by `ids`.
    pub fn new(
        daemons: D,
        session: &str,
        env: v1::EnvVars,
        ids: RequestIds,
    ) -> Result<Caller<D>, CallError> {
        let session = SessionName::parse(session)
            .map_err(|rejection| not_a_name(session, &reason_of(&rejection)))?;
        Ok(Caller {
            daemons,
            session,
            env,
            ids,
        })
    }

    /// The session this caller is.
    pub fn session(&self) -> &SessionName {
        &self.session
    }

    /// `target`: a session of this caller's project by its harp, or any
    /// session by its full name.
    pub fn target(&self, target: &str) -> Result<SessionName, CallError> {
        let parsed = match target.contains('/') {
            true => SessionName::parse(target),
            false => self.session.project().session(target),
        };
        parsed.map_err(|rejection| not_a_name(target, &reason_of(&rejection)))
    }

    /// `session_list`: this project's sessions (`all`: every project's).
    pub async fn list_sessions(&self, all: bool) -> Result<v1::ListSessionsResponse, CallError> {
        let parent = match all {
            true => EVERY_PROJECT.to_string(),
            false => self.session.project().to_string(),
        };
        let request = v1::ListSessionsRequest {
            parent,
            env: Some(self.env.clone()),
            ..v1::ListSessionsRequest::default()
        };
        on_daemon!(self, |api| api.list_sessions(request.clone()).await)
    }

    /// `session_show`: the session `target` names.
    pub async fn get_session(&self, target: &str) -> Result<v1::Session, CallError> {
        let name = self.target(target)?;
        on_daemon!(self, |api| self.read(api, &name).await)
    }

    async fn read(&self, api: &D::Api, name: &SessionName) -> Result<v1::Session, Called> {
        api.get_session(v1::GetSessionRequest {
            name: name.to_string(),
            env: Some(self.env.clone()),
        })
        .await
    }

    /// `transcript_show`: the transcript of the session `target` names.
    pub async fn get_transcript(&self, target: &str) -> Result<v1::Transcript, CallError> {
        let name = self.target(target)?;
        let request = v1::GetTranscriptRequest {
            name: format!("{name}/transcript"),
        };
        on_daemon!(self, |api| api.get_transcript(request.clone()).await)
    }

    /// `context_assemble`: the fragments `fragments` (and those of `tags`)
    /// of this caller's project, assembled in the daemon from its
    /// registered root, in the caller's environment.
    pub async fn assemble(
        &self,
        fragments: Vec<String>,
        tags: Vec<String>,
    ) -> Result<v1::AssembledContext, CallError> {
        let request = v1::AssembleContextRequest {
            name: self.session.project().to_string(),
            fragments,
            tags,
            env: Some(self.env.clone()),
            ..v1::AssembleContextRequest::default()
        };
        on_daemon!(self, |api| api.assemble_context(request.clone()).await)
    }

    /// The caller, as the `requester` of its commands: its project's id
    /// (the daemon's `GetProject`) and its own stable id.
    async fn requester(&self, api: &D::Api) -> Result<SessionRef, Called> {
        let project = api
            .get_project(v1::GetProjectRequest {
                name: self.session.project().to_string(),
            })
            .await?;
        Ok(SessionRef {
            project_id: project.uid,
            session: self.session.uid(),
        })
    }

    /// `command` to `target`'s stream through the coordinator, keyed `key`,
    /// answered with the target as the index projected it (as `GetSession`
    /// reads it when no projection came back).
    async fn write<M: angzarr_embedded::markup::DomainMessage>(
        &self,
        api: &D::Api,
        target: &SessionName,
        command: &M,
        key: &str,
    ) -> Result<v1::Session, Called> {
        let cover = angzarr_embedded::v1::Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(target.root()),
            edition: None,
        };
        let book = sessions::keyed_book(cover, command, key);
        let synced = api
            .handle_sync(book, vec![SESSION_INDEX.to_string()])
            .await?;
        match synced.projection::<v1::Session>(SESSION_INDEX) {
            Some(session) => Ok(session),
            None => self.read(api, target).await,
        }
    }

    /// One send of `message_id` to `target` on `api`: its evidence the
    /// target's as that daemon has it.
    async fn sent(
        &self,
        api: &D::Api,
        target: &SessionName,
        message_id: &str,
        urgency: Urgency,
        text: &str,
    ) -> Result<v1::Session, Called> {
        let held = self.read(api, target).await?;
        let command = SendMessage {
            message_id: message_id.to_string(),
            urgency,
            text: text.to_string(),
            requester: Some(self.requester(api).await?),
            supported: sessions_application::urgencies_of(!held.owner.is_empty(), &held.engine),
        };
        self.write(api, target, &command, message_id).await
    }

    /// `child_send`: `text` to the child `target`, at `urgency`, under an id
    /// minted here (the same on the retry).
    pub async fn send(
        &self,
        target: &str,
        urgency: Urgency,
        text: &str,
    ) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let message_id = (self.ids)();
        let session: v1::Session = on_daemon!(self, |api| self
            .sent(api, &name, &message_id, urgency, text)
            .await)?;
        Ok(Written {
            session,
            id: message_id,
        })
    }

    async fn closed(
        &self,
        api: &D::Api,
        target: &SessionName,
        key: &str,
    ) -> Result<v1::Session, Called> {
        let command = CloseSession {
            cause: TOOL_CLOSE.to_string(),
            requester: Some(self.requester(api).await?),
        };
        self.write(api, target, &command, key).await
    }

    /// `child_close`: the child `target` closed, keyed by its session, so
    /// a second close is the first's duplicate.
    pub async fn close(&self, target: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let key = format!("close:{}", name.uid());
        let session: v1::Session = on_daemon!(self, |api| self.closed(api, &name, &key).await)?;
        Ok(Written {
            session,
            id: name.uid(),
        })
    }

    async fn cleared(
        &self,
        api: &D::Api,
        target: &SessionName,
        clear_id: &str,
    ) -> Result<v1::Session, Called> {
        let command = ClearChild {
            clear_id: clear_id.to_string(),
            requester: Some(self.requester(api).await?),
        };
        self.write(api, target, &command, clear_id).await
    }

    /// `child_clear`: the child `target`'s conversation cleared, under an
    /// id minted here.
    pub async fn clear(&self, target: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let clear_id = (self.ids)();
        let session: v1::Session =
            on_daemon!(self, |api| self.cleared(api, &name, &clear_id).await)?;
        Ok(Written {
            session,
            id: clear_id,
        })
    }
}

#[cfg(test)]
#[path = "caller.test.rs"]
mod tests;
