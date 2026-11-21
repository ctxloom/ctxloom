//! A SESSION, or the USER, as the daemon's client (slice 3, T20; slice 4
//! U3, S2, S19): what ctxloom's MCP server asks of the daemon for the
//! session whose engine started it, and what the user's child verbs
//! (`session launch|send|clear|close`) ask of it. A session's identity is its
//! resource name, as the engine's environment carries it (`CTXLOOM_SESSION`,
//! s3-Q3); its project is that name's (`projects/{p}`, OD-2: never resolved
//! from a working directory, which may be a checkout). The user's project is
//! the one her command resolved, and she names no requester (D25: the user's
//! authority).
//!
//! The reads are the service's (`ListSessions`, `GetSession`,
//! `GetTranscript`, `AssembleContext`). A write is the domain's command sent
//! through the coordinator's `HandleSync` (S1 amended): its cover the
//! target's stream (the UUIDv5 of its name, B3), its `requester` the caller
//! session (an address, taken as sent: the session tree's owner rule
//! decides; none for the user), its evidence computed here from the target
//! as the daemon has it (s3-Q4: `sessions_application::urgencies_of`), and
//! its command id keyed by an id minted ONCE per call, so its one retry on a
//! new daemon (S8) is a DUPLICATE. The answer is the target's session as the
//! session index projected it after the write, or, where the projection did
//! not come back (a duplicate), as `GetSession` reads it.
//!
//! A launch (T6, S10) is `CreateHandOff` first, which the daemon refuses
//! before it writes anything (an owner nobody recorded, a child with no
//! credential), then the owner's `LaunchChild`; a refused `LaunchChild` is
//! followed by `DeleteHandOff`, so a refused launch leaves nothing.

use std::sync::Arc;

use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use run_ports::RunLocks;
use sessions::{
    ClearChild, CloseSession, LaunchChild, ProjectName, RequestRestart, RestartReason, SendMessage,
    SessionName, SessionRef, TargetTrust, Urgency,
};

use crate::client::CallError;
use crate::refusal::{ApiRejection, Rejection};
use crate::RequestIds;

/// Every project's sessions, as a `ListSessions` parent.
const EVERY_PROJECT: &str = "projects/-";

/// The read model a session command's answer is projected by.
const SESSION_INDEX: &str = "session-index";

/// The cause a close from a tool is recorded with.
const TOOL_CLOSE: &str = "closed by its owner";

/// The cause a close from the user's own command is recorded with.
const USER_CLOSE: &str = "closed by the user";

/// Why a run is stopped by `session stop` (S19).
const STOP_REASON: &str = "stopped by the user";

/// The id of a close's one need, its cascade's confirmation (D16).
pub const CLOSE_NEED: &str = "confirm";

/// How many open children an owner may hold when nothing says (the
/// delegation limit's evidence, D-N14; no config key yet).
pub const DELEGATION_LIMIT: u32 = 4;

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

pub(crate) use on_daemon;

/// A session, or the user, as the daemon's client.
pub struct Caller<D> {
    pub(crate) daemons: D,
    /// The project a target harp is of.
    pub(crate) project: ProjectName,
    /// The session this caller is; none: the user.
    me: Option<SessionName>,
    /// What every request made on its user's behalf carries of this client
    /// (owner, 2026-11-18): its directory, its whole environment (S7) and
    /// which client it is.
    context: v1::Caller,
    pub(crate) ids: RequestIds,
    /// Mints a child's harp where a launch names none.
    names: Option<RequestIds>,
    /// The launch locks a launch holds from before its `CreateHandOff` to
    /// after its `LaunchChild` (T6); none, a launch holds none.
    locks: Option<Arc<dyn RunLocks>>,
    /// What a launch into another project (`--project <dir>`, the tool's
    /// `project`, step 8 Q1) registers that project with: the directory as
    /// given, resolved where the caller resolves it (the CLI's working
    /// directory; a session's project root, Q-C); none, such a launch is
    /// refused.
    projects: Option<Arc<ProjectResolver>>,
}

/// A directory's project as `ResolveProject` registers it: refused
/// `daemon-api-project-unknown` for a directory in no ctxloom project.
pub type ProjectResolver =
    dyn Fn(&str) -> Result<v1::ResolveProjectRequest, CallError> + Send + Sync;

/// The name of the launch lock of the child `child` (its stable id), among
/// the run locks: what a launch holds while it is made, and the daemon's
/// sweep probes for a hand-off still marked in launch.
pub fn launch_lock_name(child: &str) -> String {
    format!("launch-{child}")
}

/// What a write came to: the target as the daemon has it after, the id the
/// write was sent under (a message's, a clear's; a close's and a launch's
/// are the session's), and whether this call applied it (false: it was
/// applied before, its command a DUPLICATE).
#[derive(Clone, Debug, PartialEq)]
pub struct Written {
    pub session: v1::Session,
    pub id: String,
    pub applied: bool,
}

/// A child's launch, as asked.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Launch {
    /// The owner: a session's harp in this project, or its full name; none,
    /// the caller itself (a session launching its own child).
    pub owner: Option<String>,
    /// The child's harp; none, one minted.
    pub harp: Option<String>,
    /// The child's engine; empty, the one a child is hosted on.
    pub engine: String,
    /// The agent it is planned for; empty, the project's default.
    pub agent: String,
    /// Its first task; empty, none.
    pub task: String,
    /// The directory of the ctxloom project it works in (step 8 Q1); none,
    /// the owner's.
    pub project: Option<String>,
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

/// A launch lock that could not be taken: one held is another launch of
/// the same child, under way (refused); any other, failed.
fn launch_unheld(harp: &str, error: session_ports::LockError) -> CallError {
    match error {
        session_ports::LockError::Held(_) => {
            CallError::Refused(Rejection::Api(ApiRejection::LaunchInProgress {
                harp: harp.to_string(),
            }))
        }
        session_ports::LockError::Failed(report) => CallError::LaunchLock {
            detail: report.to_string(),
        },
    }
}

/// The sessions domain's refusal of a message or a clear to the closed
/// session `target`, as the daemon would refuse it. Said here from the
/// target as the daemon has it: the coordinator's status carries a typed
/// refusal's type and not its object, so the domain's own refusal would
/// reach this client without the session it names.
fn closed_refusal(target: &SessionName) -> Called {
    let closed = sessions::SessionIsClosed {
        harp: target.harp().to_string(),
    };
    let refusal = crate::ApiRefusal::typed(
        &angzarr_embedded::markup::TypedRefusal::of(&closed),
        <sessions::SessionIsClosed as sessions::Refusal>::REFUSAL_STATUS,
    );
    Called::Refused {
        code: refusal.code,
        refusal: refusal.refusal,
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
    /// through `daemons`, as `context` (its directory, its whole environment, S7, and which client it is), each
    /// write's id minted by `ids`.
    pub fn new(
        daemons: D,
        session: &str,
        context: v1::Caller,
        ids: RequestIds,
    ) -> Result<Caller<D>, CallError> {
        let session = SessionName::parse(session)
            .map_err(|rejection| not_a_name(session, &reason_of(&rejection)))?;
        Ok(Caller {
            daemons,
            project: session.project(),
            me: Some(session),
            context,
            ids,
            names: None,
            locks: None,
            projects: None,
        })
    }

    /// The USER in the project `project` names (`projects/{p}`, as her
    /// command resolved it): her commands name no requester (D25).
    pub fn user(
        daemons: D,
        project: &str,
        context: v1::Caller,
        ids: RequestIds,
    ) -> Result<Caller<D>, CallError> {
        let project = ProjectName::parse(project)
            .map_err(|rejection| not_a_name(project, &reason_of(&rejection)))?;
        Ok(Caller {
            daemons,
            project,
            me: None,
            context,
            ids,
            names: None,
            locks: None,
            projects: None,
        })
    }

    /// This caller, a launch into another project registering it through
    /// `projects` (step 8 Q1).
    #[must_use]
    pub fn resolving(self, projects: Arc<ProjectResolver>) -> Caller<D> {
        Caller {
            projects: Some(projects),
            ..self
        }
    }

    /// This caller, each launch holding its launch lock among `locks` (T6:
    /// taken at once or not at all, so a second launch of one child while the
    /// first is made is refused).
    #[must_use]
    pub fn holding(self, locks: Arc<dyn RunLocks>) -> Caller<D> {
        Caller {
            locks: Some(locks),
            ..self
        }
    }

    /// This caller, a launch that names no child's harp minting one with
    /// `names`.
    #[must_use]
    pub fn naming(self, names: RequestIds) -> Caller<D> {
        Caller {
            names: Some(names),
            ..self
        }
    }

    /// The session this caller is; none, the user.
    pub fn session(&self) -> Option<&SessionName> {
        self.me.as_ref()
    }

    /// What this caller's requests carry of it.
    pub(crate) fn context(&self) -> &v1::Caller {
        &self.context
    }

    /// `own`, a request's own caller (a tool's presentable), with what this
    /// caller says that it does not.
    fn filled(&self, own: Option<&v1::Caller>) -> v1::Caller {
        let mut filled = own.cloned().unwrap_or_default();
        filled.fill_from(&self.context);
        filled
    }

    /// `target`: a session of this caller's project by its harp, or any
    /// session by its full name.
    pub fn target(&self, target: &str) -> Result<SessionName, CallError> {
        let parsed = match target.contains('/') {
            true => SessionName::parse(target),
            false => self.project.session(target),
        };
        parsed.map_err(|rejection| not_a_name(target, &reason_of(&rejection)))
    }

    /// `session_list`: this project's sessions (`all`: every project's).
    pub async fn list_sessions(&self, all: bool) -> Result<v1::ListSessionsResponse, CallError> {
        let parent = match all {
            true => EVERY_PROJECT.to_string(),
            false => self.project.to_string(),
        };
        let request = v1::ListSessionsRequest {
            parent,
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

    /// `child_close`'s preview (step 10c U11, D16; S40: a domain write is
    /// previewed by its resource's `Get`): a close of `target` that takes
    /// open children with it (the cascade closes each, and each of theirs)
    /// asks to confirm, naming every one, nearest first; a close that takes
    /// none asks nothing.
    pub async fn close_needs(&self, target: &str) -> Result<Vec<v1::Need>, CallError> {
        let name = self.target(target)?;
        let top = on_daemon!(self, |api| self.read(api, &name).await)?;
        let mut next: std::collections::VecDeque<String> = top.children.into();
        // The target itself is never one it takes with it (a family that
        // names its owner back).
        let mut seen = std::collections::BTreeSet::from([name.to_string()]);
        let mut taken = Vec::new();
        while let Some(child) = next.pop_front() {
            if !seen.insert(child.clone()) {
                continue;
            }
            let child = SessionName::parse(&child)
                .map_err(|rejection| not_a_name(&child, &reason_of(&rejection)))?;
            let held = on_daemon!(self, |api| self.read(api, &child).await)?;
            if held.closed {
                continue;
            }
            taken.push(child.harp().to_string());
            next.extend(held.children);
        }
        if taken.is_empty() {
            return Ok(Vec::new());
        }
        let quoted: Vec<String> = taken.iter().map(|harp| format!("\"{harp}\"")).collect();
        let arg = |name: &str, value: String| v1::NeedArg {
            name: name.to_string(),
            value,
        };
        Ok(vec![v1::Need {
            id: CLOSE_NEED.to_string(),
            kind: Some(v1::need::Kind::Confirm(v1::Confirm {
                text: format!(
                    "Close \"{}\" and, with it, {}?",
                    name.harp(),
                    quoted.join(", ")
                ),
                code: String::new(),
                args: vec![
                    arg("target", name.harp().to_string()),
                    arg("children", taken.join(", ")),
                ],
            })),
            optional: false,
        }])
    }

    /// `DeleteSession` of this caller's project's session `harp`, as
    /// `asked` (its preview, or the apply of one), under an id minted here
    /// and in the caller's environment (step 10c U11: the test build's
    /// tool presents its needs).
    pub async fn delete_session(
        &self,
        harp: &str,
        asked: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, CallError> {
        let request = v1::DeleteSessionRequest {
            name: self.target(harp)?.to_string(),
            request_id: (self.ids)(),
            caller: Some(self.filled(asked.caller.as_ref())),
            ..asked
        };
        on_daemon!(self, |api| api.delete_session(request.clone()).await)
    }

    /// `CreateFragment` in the bundle `bundle` of this caller's project, as
    /// `asked`, under an id minted here and in the caller's environment
    /// (step 10c U11: the test build's tool presents its Edit need).
    pub async fn create_fragment(
        &self,
        bundle: &str,
        asked: v1::CreateFragmentRequest,
    ) -> Result<v1::CreateFragmentResponse, CallError> {
        let request = v1::CreateFragmentRequest {
            parent: format!(
                "{}{}",
                self.project,
                library_api::names::bundle_target(bundle)
            ),
            request_id: (self.ids)(),
            caller: Some(self.filled(asked.caller.as_ref())),
            ..asked
        };
        on_daemon!(self, |api| api.create_fragment(request.clone()).await)
    }

    /// `context_assemble`: the fragments `fragments` (and those of `tags`)
    /// of this caller's project, assembled in the daemon from its
    /// registered root, in the caller's environment; what it asked for and
    /// the catalog does not hold said among its notices (the tool's answer
    /// is the message alone, so a name nobody has is said, never refused).
    pub async fn assemble(
        &self,
        fragments: Vec<String>,
        tags: Vec<String>,
    ) -> Result<v1::AssembledContext, CallError> {
        let request = v1::AssembleContextRequest {
            name: self.project.to_string(),
            fragments,
            tags,
            caller: Some(self.context.clone()),
            ..v1::AssembleContextRequest::default()
        };
        let mut assembled: v1::AssembledContext =
            on_daemon!(self, |api| api.assemble_context(request.clone()).await)?;
        let missing = missing_notices(&assembled);
        assembled.notices.extend(missing);
        Ok(assembled)
    }

    /// The caller, as the `requester` of its commands: its project's id
    /// (the daemon's `GetProject`) and its own stable id; none for the user,
    /// who names none (D25).
    async fn requester(&self, api: &D::Api) -> Result<Option<SessionRef>, Called> {
        let Some(me) = &self.me else {
            return Ok(None);
        };
        let project = self.project_of(api).await?;
        Ok(Some(SessionRef {
            project_id: project.uid,
            session: me.uid(),
        }))
    }

    /// `called`, a refusal with its requester told: the coordinator names a
    /// refused command's refusal by its type alone (the status carries no
    /// object), so a `NotOwner` arrives with no requester and reads
    /// "session  is not this session's owner". The requester is this caller
    /// itself, whose id it knows. Any other call is as it came.
    fn naming_requester(&self, called: Called) -> Called {
        let (Called::Refused { code, refusal }, Some(me)) = (&called, &self.me) else {
            return called;
        };
        if !refusal.type_url.ends_with("/ctxloom.sessions.v1.NotOwner") || !refusal.value.is_empty()
        {
            return called;
        }
        let named = sessions_proto::v1::NotOwner {
            requester: me.uid(),
        };
        match angzarr_embedded::Any::from_msg(&named) {
            Ok(refusal) => Called::Refused {
                code: *code,
                refusal,
            },
            Err(_) => called,
        }
    }

    /// This caller's project, as the daemon has it.
    async fn project_of(&self, api: &D::Api) -> Result<v1::Project, Called> {
        api.get_project(v1::GetProjectRequest {
            name: self.project.to_string(),
        })
        .await
    }

    /// `command` to `target`'s stream through the coordinator, keyed `key`,
    /// answered with the target as the index projected it (as `GetSession`
    /// reads it when no projection came back), and whether it was applied
    /// now.
    pub(crate) async fn write<M: angzarr_embedded::markup::DomainMessage>(
        &self,
        api: &D::Api,
        target: &SessionName,
        command: &M,
        key: &str,
    ) -> Result<(v1::Session, bool), Called> {
        let cover = angzarr_embedded::v1::Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(target.root()),
            edition: None,
        };
        let book = sessions::keyed_book(cover, command, key);
        let synced = api
            .handle_sync(book, vec![SESSION_INDEX.to_string()])
            .await
            .map_err(|called| self.naming_requester(called))?;
        let session = match synced.projection::<v1::Session>(SESSION_INDEX) {
            Some(session) => session,
            None => self.read(api, target).await?,
        };
        Ok((session, synced.applied))
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
    ) -> Result<Option<(v1::Session, bool)>, Called> {
        let held = self.read(api, target).await?;
        if held.closed {
            return Err(closed_refusal(target));
        }
        // The terminal wake: a primary nobody watches takes an inform alone;
        // its sender is told so here, in its own words (none: refused).
        if urgency == Urgency::Wake && held.owner.is_empty() && !held.wakeable {
            return Ok(None);
        }
        let command = SendMessage {
            message_id: message_id.to_string(),
            urgency,
            text: text.to_string(),
            requester: self.requester(api).await?,
            supported: sessions_application::urgencies_of(
                !held.owner.is_empty(),
                &held.engine,
                held.wakeable,
            ),
        };
        self.write(api, target, &command, message_id)
            .await
            .map(Some)
    }

    /// `child_send`: `text` to the child `target`, at `urgency`, under an id
    /// minted here (the same on the retry).
    pub async fn send(
        &self,
        target: &str,
        urgency: Urgency,
        text: &str,
    ) -> Result<Written, CallError> {
        self.send_as(target, urgency, text, None).await
    }

    /// `session send`: `text` to `target`, at `urgency`, under `id` (none:
    /// one minted here, the same on the retry), so the same id sent again is
    /// one message.
    pub async fn send_as(
        &self,
        target: &str,
        urgency: Urgency,
        text: &str,
        id: Option<&str>,
    ) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let message_id = match id.filter(|id| !id.is_empty()) {
            Some(id) => id.to_string(),
            None => (self.ids)(),
        };
        let sent = on_daemon!(self, |api| self
            .sent(api, &name, &message_id, urgency, text)
            .await)?;
        let Some((session, applied)) = sent else {
            return Err(CallError::Refused(Rejection::Api(
                ApiRejection::WakeInformOnly {
                    harp: name.harp().to_string(),
                },
            )));
        };
        Ok(Written {
            session,
            id: message_id,
            applied,
        })
    }

    async fn closed(
        &self,
        api: &D::Api,
        target: &SessionName,
        key: &str,
    ) -> Result<(v1::Session, bool), Called> {
        let cause = match self.me {
            Some(_) => TOOL_CLOSE,
            None => USER_CLOSE,
        };
        // A session closed already is told so, whoever closed it (the
        // close records nothing on it).
        let was_closed = self.read(api, target).await?.closed;
        let command = CloseSession {
            cause: cause.to_string(),
            requester: self.requester(api).await?,
        };
        let (session, applied) = self.write(api, target, &command, key).await?;
        Ok((session, applied && !was_closed))
    }

    /// `child_close`, `session close`: `target` closed, keyed by its
    /// session, so a second close is the first's duplicate.
    pub async fn close(&self, target: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let key = format!("close:{}", name.uid());
        let (session, applied) = on_daemon!(self, |api| self.closed(api, &name, &key).await)?;
        Ok(Written {
            session,
            id: name.uid(),
            applied,
        })
    }

    /// `session stop` (S19, OD-57): the run `target` is on now asked to
    /// stop, keyed by that run; the session stays open.
    pub async fn stop(&self, target: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let (session, applied) = on_daemon!(self, |api| self.stopped(api, &name).await)?;
        Ok(Written {
            id: name.uid(),
            session,
            applied,
        })
    }

    /// One stop of `target`'s current run on `api`: its run as that daemon
    /// has it, keyed `stop:<session>:<incarnation>`.
    async fn stopped(
        &self,
        api: &D::Api,
        target: &SessionName,
    ) -> Result<(v1::Session, bool), Called> {
        let incarnation = self.read(api, target).await?.incarnation;
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(runs::run_root(&target.uid(), incarnation)),
            edition: None,
        };
        let key = format!("stop:{}:{incarnation}", target.uid());
        let command = runs::RequestStop {
            reason: STOP_REASON.to_string(),
        };
        // A run's command: no session projection to wait for.
        let applied = api
            .handle(sessions::keyed_book(cover, &command, &key))
            .await?;
        Ok((self.read(api, target).await?, applied))
    }

    /// `session restart` (step 10e Stage 2a): the session `target` asked to
    /// restart its engine with resume. The SESSION decides whether one is
    /// owed (`RequestRestart`); the daemon that hosts the engine acts on what
    /// it records. Keyed `restart:<session>:<incarnation>:operator`, so a
    /// replay is the first's duplicate. A terminal session is refused: its
    /// engine is its own `ctxloom run` process's, not the daemon's.
    pub async fn restart(&self, target: &str, note: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let asked = on_daemon!(self, |api| self.restarted(api, &name, note).await)?;
        let Some((session, applied)) = asked else {
            return Err(CallError::Refused(Rejection::Api(
                ApiRejection::PrimaryNotHosted {
                    harp: name.harp().to_string(),
                },
            )));
        };
        Ok(Written {
            id: name.uid(),
            session,
            applied,
        })
    }

    async fn restarted(
        &self,
        api: &D::Api,
        target: &SessionName,
        note: &str,
    ) -> Result<Option<(v1::Session, bool)>, Called> {
        let held = self.read(api, target).await?;
        if held.owner.is_empty() {
            return Ok(None);
        }
        let command = RequestRestart {
            reason: RestartReason::Operator,
            note: note.to_string(),
        };
        let key = format!("restart:{}:{}:operator", target.uid(), held.incarnation);
        self.write(api, target, &command, &key).await.map(Some)
    }

    async fn cleared(
        &self,
        api: &D::Api,
        target: &SessionName,
        clear_id: &str,
    ) -> Result<(v1::Session, bool), Called> {
        if self.read(api, target).await?.closed {
            return Err(closed_refusal(target));
        }
        let command = ClearChild {
            clear_id: clear_id.to_string(),
            requester: self.requester(api).await?,
        };
        self.write(api, target, &command, clear_id).await
    }

    /// `child_clear`, `session clear`: the child `target`'s conversation
    /// cleared, under an id minted here.
    pub async fn clear(&self, target: &str) -> Result<Written, CallError> {
        let name = self.target(target)?;
        let clear_id = (self.ids)();
        let (session, applied) = on_daemon!(self, |api| self.cleared(api, &name, &clear_id).await)?;
        Ok(Written {
            session,
            id: clear_id,
            applied,
        })
    }

    /// `child_launch`, `session launch` (T6, S10): a child of `launch`'s
    /// owner (none: this caller), its launch lock held (a second launch of
    /// it meanwhile refused), its environment handed to the daemon first
    /// (`CreateHandOff`, refused before anything is written), then the
    /// owner's `LaunchChild` on the engine the daemon planned it on, keyed
    /// by the owner and the child, so its retry on a new daemon is one
    /// launch. A refused `LaunchChild` takes its hand-off back
    /// (`DeleteHandOff`). Answered with the child as the
    /// daemon has it (as launched, until its daemon has minted it).
    pub async fn launch(&self, launch: &Launch) -> Result<Written, CallError> {
        let owner = match (&launch.owner, &self.me) {
            (Some(owner), _) => self.target(owner)?,
            (None, Some(me)) => me.clone(),
            (None, None) => return Err(not_a_name("", "shape")),
        };
        let harp = match (&launch.harp, &self.names) {
            // A name chosen may not take an ask's form (step 8, Q7), refused
            // before anything is written.
            (Some(harp), _) => {
                sessions::validate_session_name(harp)
                    .map_err(|rejection| CallError::Refused(Rejection::Sessions(rejection)))?;
                harp.clone()
            }
            (None, Some(names)) => names(),
            (None, None) => String::new(),
        };
        // The project the child works in: the one the launch names, as the
        // daemon registers it (step 8 Q1), else its owner's.
        let target = match &launch.project {
            Some(dir) => Some(self.registered(dir).await?),
            None => None,
        };
        let project = match &target {
            Some(target) => ProjectName::parse(&target.name)
                .map_err(|rejection| not_a_name(&target.name, &reason_of(&rejection)))?,
            None => owner.project(),
        };
        let child = project
            .session(&harp)
            .map_err(|rejection| not_a_name(&harp, &reason_of(&rejection)))?;
        // Held for the launch's whole life, its retry on a new daemon too,
        // and let go once its `LaunchChild` is answered.
        let _held = match &self.locks {
            Some(locks) => Some(
                locks
                    .hold_run(&launch_lock_name(&child.uid()))
                    .map_err(|error| launch_unheld(&harp, error))?,
            ),
            None => None,
        };
        let request_id = (self.ids)();
        let into = LaunchInto {
            owner: &owner,
            child: &child,
            target: target.as_ref(),
        };
        let (session, applied) = on_daemon!(self, |api| self
            .launched(api, &into, &launch.engine, launch, &request_id)
            .await)?;
        Ok(Written {
            session,
            id: child.uid(),
            applied,
        })
    }

    /// The project at the directory `dir` (a launch's `--project`), as the
    /// daemon registers it: its name, id and root.
    async fn registered(&self, dir: &str) -> Result<v1::Project, CallError> {
        let resolve = self.projects.as_ref().ok_or_else(|| {
            CallError::Refused(Rejection::Api(ApiRejection::ProjectUnknown {
                cwd: dir.to_string(),
            }))
        })?;
        let request = resolve(dir)?;
        on_daemon!(self, |api| api.resolve_project(request.clone()).await)
    }

    /// One launch of `into`'s child for its owner on `api`.
    async fn launched(
        &self,
        api: &D::Api,
        into: &LaunchInto<'_>,
        engine: &str,
        launch: &Launch,
        request_id: &str,
    ) -> Result<(v1::Session, bool), Called> {
        let (owner, child) = (into.owner, into.child);
        let parent = owner.project().to_string();
        let hand_off = api
            .create_hand_off(v1::CreateHandOffRequest {
                parent: parent.clone(),
                hand_off_id: child.uid(),
                owner: owner.harp().to_string(),
                engine: engine.to_string(),
                agent: launch.agent.clone(),
                project: into
                    .target
                    .map(|target| target.name.clone())
                    .unwrap_or_default(),
                request_id: request_id.to_string(),
                caller: Some(self.context.clone()),
            })
            .await?;
        // The engine it is planned on: asked, else its agent's (the
        // daemon's answer names it).
        let engine = match (hand_off.engine.is_empty(), engine.is_empty()) {
            (false, _) => hand_off.engine.clone(),
            (true, false) => engine.to_string(),
            (true, true) => session_policies::CHILD_ENGINE.to_string(),
        };
        let launched = self.launch_child(api, into, &engine, &launch.task).await;
        let applied = match launched {
            Ok(applied) => applied,
            Err(refused @ Called::Refused { .. }) => {
                let _ = api
                    .delete_hand_off(v1::DeleteHandOffRequest {
                        name: hand_off.name,
                        request_id: request_id.to_string(),
                    })
                    .await;
                return Err(refused);
            }
            Err(other) => return Err(other),
        };
        let session = match self.read(api, child).await {
            Ok(session) => session,
            // Not minted yet: the child as launched.
            Err(Called::Refused { .. }) => v1::Session {
                name: child.to_string(),
                uid: child.uid(),
                engine,
                owner: owner.to_string(),
                ..v1::Session::default()
            },
            Err(other) => return Err(other),
        };
        Ok((session, applied))
    }

    /// The owner's `LaunchChild` of `into`'s child: whether it was applied
    /// now. The child's project, its root and the target's trust are the
    /// daemon's answers (D8): its owner's project and root, or the project
    /// the launch named, registered, and whether the user trusts it.
    async fn launch_child(
        &self,
        api: &D::Api,
        into: &LaunchInto<'_>,
        engine: &str,
        task: &str,
    ) -> Result<bool, Called> {
        let (owner, child) = (into.owner, into.child);
        let own = api
            .get_project(v1::GetProjectRequest {
                name: owner.project().to_string(),
            })
            .await?;
        let held = self.read(api, owner).await?;
        let (project_id, root, trust) = match into.target {
            Some(target) if target.uid != own.uid => {
                let trusted = api
                    .get_trust(v1::GetTrustRequest {
                        name: trust::TrustName::of(child.project()).to_string(),
                    })
                    .await?
                    .trusted;
                (
                    target.uid.clone(),
                    target.root.clone(),
                    TargetTrust {
                        same_project: false,
                        trusted,
                    },
                )
            }
            // Its owner's project: its trust is unread.
            _ => (
                own.uid,
                held.project_root,
                TargetTrust {
                    same_project: true,
                    trusted: false,
                },
            ),
        };
        let command = LaunchChild {
            child_project_id: project_id,
            child_session: child.uid(),
            child_root: root,
            harp: child.harp().to_string(),
            engine: engine.to_string(),
            trust,
            delegation_limit: DELEGATION_LIMIT,
            requester: self.requester(api).await?,
            prompt: task.to_string(),
        };
        let key = format!("launch:{}:{}", owner.uid(), child.uid());
        Ok(self.write(api, owner, &command, &key).await?.1)
    }
}

/// A launch's owner and child, and the project it named (none: the
/// owner's).
struct LaunchInto<'a> {
    owner: &'a SessionName,
    child: &'a SessionName,
    target: Option<&'a v1::Project>,
}

/// The child verbs a command sends as the user (`session launch|send|clear|
/// close`, S2, S19), as the CLI calls them.
#[allow(async_fn_in_trait)]
pub trait ChildWrites {
    /// `session launch`: a child of `launch`'s owner.
    async fn launch(&self, launch: &Launch) -> Result<Written, CallError>;

    /// `session send`: `text` to `target` at `urgency`, under `id` (none:
    /// one minted).
    async fn send_as(
        &self,
        target: &str,
        urgency: Urgency,
        text: &str,
        id: Option<&str>,
    ) -> Result<Written, CallError>;

    /// `session clear`: `target`'s conversation started again.
    async fn clear(&self, target: &str) -> Result<Written, CallError>;

    /// `session close`: `target` closed, with its children.
    async fn close(&self, target: &str) -> Result<Written, CallError>;

    /// `session stop`: `target`'s current run stopped, `target` left open.
    async fn stop(&self, target: &str) -> Result<Written, CallError>;

    /// `session restart`: `target`'s engine restarted with resume, at the end
    /// of its turn in flight.
    async fn restart(&self, target: &str, note: &str) -> Result<Written, CallError>;
}

impl<D: Daemons> ChildWrites for Caller<D> {
    async fn launch(&self, launch: &Launch) -> Result<Written, CallError> {
        Caller::launch(self, launch).await
    }

    async fn send_as(
        &self,
        target: &str,
        urgency: Urgency,
        text: &str,
        id: Option<&str>,
    ) -> Result<Written, CallError> {
        Caller::send_as(self, target, urgency, text, id).await
    }

    async fn clear(&self, target: &str) -> Result<Written, CallError> {
        Caller::clear(self, target).await
    }

    async fn close(&self, target: &str) -> Result<Written, CallError> {
        Caller::close(self, target).await
    }

    async fn stop(&self, target: &str) -> Result<Written, CallError> {
        Caller::stop(self, target).await
    }

    async fn restart(&self, target: &str, note: &str) -> Result<Written, CallError> {
        Caller::restart(self, target, note).await
    }
}

/// What an assembly was asked for and the catalog does not hold, as the
/// notices the MCP tool says (`daemon-api-assembly-missing-fragment` and
/// `-tag`, en-US beside the code).
pub fn missing_notices(assembled: &v1::AssembledContext) -> Vec<v1::Notice> {
    let missing = |code: &str, name: &str, what: String| v1::Notice {
        code: code.to_string(),
        args: vec![v1::NeedArg {
            name: "name".to_string(),
            value: name.to_string(),
        }],
        text: what,
    };
    let fragments = assembled.missing_fragments.iter().map(|name| {
        missing(
            "daemon-api-assembly-missing-fragment",
            name,
            format!("no fragment is named {name}"),
        )
    });
    let tags = assembled.missing_tags.iter().map(|name| {
        missing(
            "daemon-api-assembly-missing-tag",
            name,
            format!("no fragment is tagged {name}"),
        )
    });
    fragments.chain(tags).collect()
}

#[cfg(test)]
#[path = "caller.test.rs"]
mod tests;
