//! The permission asks (step 8 U4, ruling 142): `CreateApproval` (D10: the
//! claude ask, which `ctxloom mcp serve` sends and the daemon records with
//! what only it knows), `GetApproval`, `ListApprovals` and `WaitApproval`
//! (the approval tool's wait), each read from the daemon's warm pending
//! approvals; and the client's half: the user's listing and answer
//! (`AnswerApproval` through the coordinator's `HandleSync` on projector
//! `pending-approvals`), and the MCP server's ask, wait and withdrawal.
//!
//! Every approvals call is served while the daemon drains (D6): an ask comes
//! only from a session the daemon already hosts.
//!
//! An ask's name is `ask-<adjective>-<noun>` (D9, Q7), minted outside the
//! core ([`AskNames`]) and claimed in `approval-names` under its ROOT's
//! project (Q-A). A word naming an ask begins `ask-`; any other is the
//! asking session's harp, which names an ask only while exactly one waits
//! (S29): two or more are `daemon-api-approval-ambiguous`, none
//! `daemon-api-approval-none-waiting`, both said here (no service method
//! refuses them).

use std::sync::Arc;

use angzarr_embedded::v1::Cover;
use angzarr_embedded::Failure;
use approvals::{
    ask_key, ask_name, ask_root, bounded, claude_options, fact_book, is_ask_name, name_root,
    AnswerApproval, ApprovalName, ApprovalsRejection, ClaimApprovalName, RecordApprovalAsked,
    RecordApprovalWithdrawn, SessionRef, APPROVALS_DOMAIN, APPROVAL_NAMES_DOMAIN,
};
use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use pending_approvals::{PendingApprovals, PENDING_APPROVALS};
use run_ports::Edge;

use crate::caller::{on_daemon, Caller};
use crate::client::CallError;
use crate::refusal::{ApiError, ApiRefusal, ApiRejection, Rejection};
use crate::server::Api;

/// How deep a session tree is walked for its root: past it, the tree is
/// taken to be cut (a session's owner names itself or a loop).
const TREE_DEPTH: usize = 64;

/// Fresh ask names to try, in order: harp-names' two-word mints
/// (`tidy-wren`), minted outside the core.
pub trait AskNames: Send + Sync {
    fn fresh(&self) -> Vec<String>;
}

/// Where the daemon records and reads its asks: the warm pending approvals
/// its kernel keeps (S32), and the names it mints them.
#[derive(Clone)]
pub struct Approving {
    pub index: PendingApprovals,
    pub names: Arc<dyn AskNames>,
}

/// An approval's state word, as a settled one is said
/// (`approvals-approval-settled`'s `how`): `answered (<option>)`,
/// `expired` or `withdrawn`; none while it waits.
pub fn settled_how(approval: &v1::Approval) -> Option<String> {
    match approval.state.as_str() {
        "answered" => Some(format!("answered ({})", approval.option)),
        "expired" | "withdrawn" => Some(approval.state.clone()),
        _ => None,
    }
}

/// The refusal of an answer to `approval`, settled already (as the
/// approvals domain says it), when it is settled.
pub fn settled_refusal(approval: &v1::Approval) -> Option<CallError> {
    settled_how(approval).map(|how| {
        CallError::Refused(Rejection::Approvals(ApprovalsRejection::ApprovalSettled {
            ask_id: ask_of(approval).to_string(),
            how,
        }))
    })
}

/// The ask and how it was settled, when `error` is an answer refused for
/// a settled ask.
pub fn settled_of(error: &CallError) -> Option<(String, String)> {
    match error {
        CallError::Refused(Rejection::Approvals(ApprovalsRejection::ApprovalSettled {
            ask_id,
            how,
        })) => Some((ask_id.clone(), how.clone())),
        _ => None,
    }
}

/// The stream `cover` as the book of its `pages`, read from its start: its
/// next sequence the one after the last page's.
fn book_of(
    cover: &Cover,
    pages: Vec<angzarr_embedded::v1::EventPage>,
) -> angzarr_embedded::v1::EventBook {
    let next = pages.last().map_or(0, |page| page.sequence + 1);
    angzarr_embedded::v1::EventBook {
        cover: Some(cover.clone()),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

/// The ask an approval's name ends with.
pub fn ask_of(approval: &v1::Approval) -> &str {
    approval.name.rsplit('/').next().unwrap_or_default()
}

/// The approvals domain's refusal `refusal`, made here before any command
/// is sent, under the status its message declares.
fn refused<T>(refusal: &T) -> ApiError
where
    T: approvals::Refusal + angzarr_embedded::markup::DomainMessage,
{
    ApiRefusal::typed(
        &angzarr_embedded::markup::TypedRefusal::of(refusal),
        <T as approvals::Refusal>::REFUSAL_STATUS,
    )
    .into()
}

fn unknown(ask: &str) -> ApiError {
    refused(&approvals::ApprovalUnknown {
        ask_id: ask.to_string(),
    })
}

impl<E: Edge> Api<E> {
    /// Where its asks are; none, the approvals calls fail.
    fn approving_ports(&self) -> Result<&Approving, ApiError> {
        self.approving.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon records no ask".to_string(),
        })
    }

    /// The session record `session` (its stable id) and its tree's root's,
    /// as the warm index holds them.
    fn tree_of(
        &self,
        session: &str,
    ) -> Result<(session_index::SessionRecord, session_index::SessionRecord), ApiError> {
        let index = self.parts.index.index();
        let record = index.find_by_id(session).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: session.to_string(),
            }))
        })?;
        let mut root = record.clone();
        for _ in 0..TREE_DEPTH {
            let Some(owner) = root
                .owner
                .as_ref()
                .and_then(|o| index.find_by_id(&o.session))
            else {
                break;
            };
            root = owner;
        }
        Ok((record, root))
    }

    /// `CreateApproval` (D10): the ask of the session `request.session`,
    /// which the daemon hosts, recorded with its run, engine, agent and
    /// `approval_timeout`, its root and claude's options; its name minted
    /// and claimed under its root's project (Q-A). A retry of the same
    /// (session, request id) is answered the approval it made.
    pub async fn create_approval(
        &self,
        request: v1::CreateApprovalRequest,
    ) -> Result<v1::Approval, ApiError> {
        let approving = self.approving_ports()?.clone();
        self.parts.hosting.fresh().await;
        if let Some(made) = approving
            .index
            .index()
            .asked_by(&request.session, &request.engine_request_id)
        {
            return Ok(made.resource());
        }
        let (record, root) = self.tree_of(&request.session)?;
        let (child_name, root_name, project) = {
            let index = self.parts.index.index();
            let project = index.project_harp(&root.project_id).map(str::to_string);
            (index.name_of(&record), index.name_of(&root), project)
        };
        let asker = self.parts.hosting.asker(&request.session).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotHosted {
                name: child_name.clone(),
            }))
        })?;
        let project = project.ok_or_else(|| ApiError::Failed {
            detail: format!("the project {} has no harp", root.project_id),
        })?;
        let stream = ask_root(&request.session, &asker.run, &request.engine_request_id);
        let ask = self
            .claim(&approving, &project, &root.project_id, stream)
            .await?;
        let input = bounded(&request.input_json);
        let fact = RecordApprovalAsked {
            ask_id: ask.ask.clone(),
            child: SessionRef {
                project_id: record.project_id.clone(),
                session: record.id.clone(),
            },
            run: asker.run.clone(),
            engine: asker.engine.clone(),
            root: SessionRef {
                project_id: root.project_id.clone(),
                session: root.id.clone(),
            },
            tool_name: request.tool_name.clone(),
            input_excerpt: input.excerpt,
            input_bytes: input.bytes,
            input_sha256: input.sha256,
            timeout_seconds: u32::try_from(asker.approval_timeout.as_secs()).unwrap_or(u32::MAX),
            engine_request_id: request.engine_request_id.clone(),
            options: claude_options(&request.tool_name, &request.input_json),
            name: ask.to_string(),
            child_name,
            root_name,
            agent: asker.agent.clone(),
        };
        let cover = Cover {
            domain: APPROVALS_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*stream.as_bytes())),
            edition: None,
        };
        let key = ask_key(&asker.run, &request.engine_request_id);
        match self
            .parts
            .edge
            .handle(fact_book(cover.clone(), &fact, &key))
            .await
        {
            Ok(_) => {}
            Err(Failure::Rejected(rejected)) => return Err(ApiRefusal::rejected(&rejected).into()),
            Err(other) => return Err(ApiError::failed(other)),
        }
        self.recorded(&cover).await
    }

    /// The ask the stream `cover` names, as its own pages say it now (the
    /// pending approvals' fold of them: one renderer), whatever the warm
    /// index has caught up with yet.
    async fn recorded(&self, cover: &Cover) -> Result<v1::Approval, ApiError> {
        let pages = self
            .parts
            .edge
            .events(cover, 0)
            .await
            .map_err(ApiError::failed)?;
        let book = book_of(cover, pages);
        let mut folded = pending_approvals::ApprovalIndex::new();
        folded.project(&book).map_err(ApiError::failed)?;
        let uid = cover
            .root
            .as_ref()
            .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
            .unwrap_or_default()
            .hyphenated()
            .to_string();
        folded
            .by_uid(&uid)
            .map(|row| row.resource())
            .ok_or_else(|| ApiError::Failed {
                detail: format!("the ask stream {uid} holds no ask"),
            })
    }

    /// A fresh ask name claimed for the ask stream `stream` under the
    /// project `project` (its harp; `project_id` its id): the first of the
    /// names offered that no other ask holds.
    async fn claim(
        &self,
        approving: &Approving,
        project: &str,
        project_id: &str,
        stream: uuid::Uuid,
    ) -> Result<ApprovalName, ApiError> {
        let approval = stream.hyphenated().to_string();
        for words in approving.names.fresh() {
            let name = ApprovalName {
                project: project.to_string(),
                ask: ask_name(&words),
            };
            let cover = Cover {
                domain: APPROVAL_NAMES_DOMAIN.to_string(),
                root: Some(angzarr_embedded::v1::Uuid::from_bytes(
                    *name_root(&name).as_bytes(),
                )),
                edition: None,
            };
            let claim = ClaimApprovalName {
                project_id: project_id.to_string(),
                ask_id: name.ask.clone(),
                approval: approval.clone(),
            };
            let key = format!("approval-name:{}:{approval}", name.ask);
            match self
                .parts
                .edge
                .handle(sessions::keyed_book(cover, &claim, &key))
                .await
            {
                Ok(_) => return Ok(name),
                Err(Failure::Rejected(_)) => continue,
                Err(other) => return Err(ApiError::failed(other)),
            }
        }
        Err(ApiError::Failed {
            detail: "no ask name offered was free".to_string(),
        })
    }

    /// `GetApproval`: the ask `request.name` names.
    pub async fn get_approval(
        &self,
        request: v1::GetApprovalRequest,
    ) -> Result<v1::Approval, ApiError> {
        let approving = self.approving_ports()?;
        let name = ApprovalName::parse(&request.name).ok_or_else(|| unknown(&request.name))?;
        self.parts.hosting.fresh().await;
        let row = approving.index.index().named(&name.to_string());
        row.map(|row| row.resource())
            .ok_or_else(|| unknown(&name.ask))
    }

    /// `ListApprovals`: the asks under the project `request.parent` (its
    /// roots'), oldest first; only under the root session `request.root`
    /// when it names one, and only those waiting when `request.waiting`.
    pub async fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> Result<v1::ListApprovalsResponse, ApiError> {
        let approving = self.approving_ports()?;
        self.parts.hosting.fresh().await;
        let root = Some(request.root.as_str()).filter(|root| !root.is_empty());
        let approvals = approving
            .index
            .index()
            .listed(&request.parent, root, request.waiting)
            .iter()
            .map(pending_approvals::ApprovalRow::resource)
            .collect();
        Ok(v1::ListApprovalsResponse {
            approvals,
            ..v1::ListApprovalsResponse::default()
        })
    }

    /// `WaitApproval` (S27): the ask `request.name` names, answered once it
    /// is settled, read afresh at each of the loop's rounds. It waits as
    /// long as its caller does: a caller that hangs up drops it.
    pub async fn wait_approval(
        &self,
        request: v1::WaitApprovalRequest,
    ) -> Result<v1::Approval, ApiError> {
        let approving = self.approving_ports()?;
        let name = ApprovalName::parse(&request.name).ok_or_else(|| unknown(&request.name))?;
        loop {
            self.parts.hosting.fresh().await;
            let row = approving.index.index().named(&name.to_string());
            let row = row.ok_or_else(|| unknown(&name.ask))?;
            if row.state != pending_approvals::State::Waiting {
                return Ok(row.resource());
            }
        }
    }
}

/// What an answer came to: the ask as the daemon has it after, and whether
/// this call applied it.
#[derive(Clone, Debug, PartialEq)]
pub struct Answered {
    pub approval: v1::Approval,
    pub applied: bool,
}

/// The user's approvals verbs (`session approvals`, `session approvals
/// answer`), as the CLI calls them.
#[allow(async_fn_in_trait)]
pub trait ApprovalAnswers {
    /// This project's waiting asks; under the root `root` (a harp) only,
    /// when it names one.
    async fn list_approvals(
        &self,
        root: Option<&str>,
    ) -> Result<v1::ListApprovalsResponse, CallError>;

    /// The waiting ask `word` names: an ask's name, or the asking session's
    /// harp while exactly one of its asks waits.
    async fn approval(&self, word: &str) -> Result<v1::Approval, CallError>;

    /// `approval` answered with `option` and `message`.
    async fn answer(
        &self,
        approval: &v1::Approval,
        option: &str,
        message: &str,
    ) -> Result<Answered, CallError>;
}

/// `rejection`, an approvals refusal the coordinator named by its type
/// alone, said with the fields of `approval` as the daemon has it now.
fn filled(
    rejection: ApprovalsRejection,
    approval: &v1::Approval,
    option: &str,
) -> ApprovalsRejection {
    let ask_id = ask_of(approval).to_string();
    match rejection {
        ApprovalsRejection::ApprovalUnknown { .. } => {
            ApprovalsRejection::ApprovalUnknown { ask_id }
        }
        ApprovalsRejection::ApprovalSettled { .. } => ApprovalsRejection::ApprovalSettled {
            ask_id,
            how: settled_how(approval).unwrap_or_default(),
        },
        ApprovalsRejection::OptionNotOffered { .. } => ApprovalsRejection::OptionNotOffered {
            ask_id,
            option: option.to_string(),
            offered: offered(approval),
        },
        ApprovalsRejection::MessageNotRelayed { .. } => ApprovalsRejection::MessageNotRelayed {
            ask_id,
            engine: approval.engine.clone(),
        },
        other => other,
    }
}

/// An approval's option ids, comma separated, in order.
fn offered(approval: &v1::Approval) -> String {
    let ids: Vec<&str> = approval.options.iter().map(|o| o.id.as_str()).collect();
    ids.join(", ")
}

impl<D: Daemons> Caller<D> {
    async fn got(&self, api: &D::Api, ask: &str) -> Result<v1::Approval, Called> {
        api.get_approval(v1::GetApprovalRequest {
            name: ApprovalName {
                project: self.project.harp().to_string(),
                ask: ask.to_string(),
            }
            .to_string(),
        })
        .await
    }

    /// `session approvals [<harp>]`: this project's waiting asks, under the
    /// root session `root` names when it names one.
    pub async fn list_approvals(
        &self,
        root: Option<&str>,
    ) -> Result<v1::ListApprovalsResponse, CallError> {
        let root = match root {
            Some(root) => self.target(root)?.to_string(),
            None => String::new(),
        };
        let request = v1::ListApprovalsRequest {
            parent: self.project.to_string(),
            root,
            waiting: true,
            ..v1::ListApprovalsRequest::default()
        };
        on_daemon!(self, |api| api.list_approvals(request.clone()).await)
    }

    /// The ask `word` names: an ask's own name (`ask-...`), or the harp of
    /// the session that asked while exactly one of its asks waits.
    pub async fn approval(&self, word: &str) -> Result<v1::Approval, CallError> {
        if is_ask_name(word) {
            return on_daemon!(self, |api| self.got(api, word).await);
        }
        let child = self.target(word)?.to_string();
        let listed = self.list_approvals(None).await?;
        let mut waiting: Vec<v1::Approval> = listed
            .approvals
            .into_iter()
            .filter(|approval| approval.child == child)
            .collect();
        match waiting.len() {
            0 => Err(CallError::Refused(Rejection::Api(
                ApiRejection::ApprovalNoneWaiting {
                    harp: word.to_string(),
                },
            ))),
            1 => Ok(waiting.remove(0)),
            _ => Err(CallError::Refused(Rejection::Api(
                ApiRejection::ApprovalAmbiguous {
                    harp: word.to_string(),
                    asks: waiting.iter().map(|a| ask_of(a).to_string()).collect(),
                },
            ))),
        }
    }

    async fn answered(
        &self,
        api: &D::Api,
        approval: &v1::Approval,
        command: &AnswerApproval,
        key: &str,
    ) -> Result<Answered, Called> {
        let root = uuid::Uuid::parse_str(&approval.uid).unwrap_or_default();
        let cover = Cover {
            domain: APPROVALS_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let book = sessions::keyed_book(cover, command, key);
        let synced = api
            .handle_sync(book, vec![PENDING_APPROVALS.to_string()])
            .await?;
        let approval = match synced.projection::<v1::Approval>(PENDING_APPROVALS) {
            Some(approval) => approval,
            None => self.got(api, ask_of(approval)).await?,
        };
        Ok(Answered {
            approval,
            applied: synced.applied,
        })
    }

    /// `session approvals answer`: `approval` answered with `option` (and
    /// `message`, a claude deny's), under an id minted once per call so its
    /// retry on a new daemon is one answer. A settled ask, or an option it
    /// does not offer, is refused as the domain would refuse it, said here
    /// with the ask as the daemon has it (the coordinator's refusal carries
    /// its type alone).
    pub async fn answer_approval(
        &self,
        approval: &v1::Approval,
        option: &str,
        message: &str,
    ) -> Result<Answered, CallError> {
        let ask_id = ask_of(approval).to_string();
        if let Some(settled) = settled_refusal(approval) {
            return Err(settled);
        }
        if !approval.options.iter().any(|o| o.id == option) {
            return Err(CallError::Refused(Rejection::Approvals(
                ApprovalsRejection::OptionNotOffered {
                    ask_id,
                    option: option.to_string(),
                    offered: offered(approval),
                },
            )));
        }
        let command = AnswerApproval {
            ask_id,
            option: option.to_string(),
            message: message.to_string(),
        };
        let key = format!("answer:{}", (self.ids)());
        let answered = on_daemon!(self, |api| self
            .answered(api, approval, &command, &key)
            .await);
        match answered {
            Err(CallError::Refused(Rejection::Approvals(rejection))) => {
                let now = on_daemon!(self, |api| self.got(api, ask_of(approval)).await)
                    .unwrap_or_else(|_| approval.clone());
                Err(CallError::Refused(Rejection::Approvals(filled(
                    rejection, &now, option,
                ))))
            }
            answered => answered,
        }
    }

    /// The approval tool's ask (D10): this session's engine asks to run
    /// `tool_name` with `input_json` (its JSON text), under its request id
    /// `request_id` (claude's `tool_use_id`); answered the approval the
    /// daemon recorded (the same on a retry).
    pub async fn create_approval(
        &self,
        tool_name: &str,
        input_json: &str,
        request_id: &str,
    ) -> Result<v1::Approval, CallError> {
        let me = self.session().ok_or_else(|| {
            CallError::Refused(Rejection::Api(ApiRejection::NameInvalid {
                name: String::new(),
                reason: "shape".to_string(),
            }))
        })?;
        let request = v1::CreateApprovalRequest {
            parent: self.project.to_string(),
            session: me.uid(),
            engine_request_id: request_id.to_string(),
            tool_name: tool_name.to_string(),
            input_json: input_json.to_string(),
            request_id: (self.ids)(),
        };
        on_daemon!(self, |api| api.create_approval(request.clone()).await)
    }

    /// The approval tool's wait (S27): the approval named `name`, once it
    /// is settled, waited for until `until` (U5: one instant, so a wait
    /// retried on a new daemon ends when the first would have).
    pub async fn wait_approval(
        &self,
        name: &str,
        until: daemon_ports::Until,
    ) -> Result<v1::Approval, CallError> {
        let request = v1::WaitApprovalRequest {
            name: name.to_string(),
        };
        on_daemon!(self, |api| api.wait_approval(request.clone(), until).await)
    }

    /// The approval tool's call cancelled by its engine (claude's
    /// `notifications/cancelled`, a steer): its ask withdrawn, `reason` the
    /// engine's words. Keyed by the ask, so a redelivery is one withdrawal.
    pub async fn withdraw_approval(
        &self,
        approval: &v1::Approval,
        reason: &str,
    ) -> Result<bool, CallError> {
        let ask_id = ask_of(approval).to_string();
        let root = uuid::Uuid::parse_str(&approval.uid).unwrap_or_default();
        let cover = Cover {
            domain: APPROVALS_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let fact = RecordApprovalWithdrawn {
            ask_id: ask_id.clone(),
            reason: reason.to_string(),
        };
        let book = fact_book(cover, &fact, &format!("approval-withdrawn:{ask_id}"));
        on_daemon!(self, |api| api.handle(book.clone()).await)
    }
}

impl<D: Daemons> ApprovalAnswers for Caller<D> {
    async fn list_approvals(
        &self,
        root: Option<&str>,
    ) -> Result<v1::ListApprovalsResponse, CallError> {
        Caller::list_approvals(self, root).await
    }

    async fn approval(&self, word: &str) -> Result<v1::Approval, CallError> {
        Caller::approval(self, word).await
    }

    async fn answer(
        &self,
        approval: &v1::Approval,
        option: &str,
        message: &str,
    ) -> Result<Answered, CallError> {
        Caller::answer_approval(self, approval, option, message).await
    }
}

#[cfg(test)]
#[path = "approvals.test.rs"]
mod tests;
