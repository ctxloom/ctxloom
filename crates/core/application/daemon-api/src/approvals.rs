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
//! (S29): two or more are `daemon-api-contract-approval-ambiguous`, none
//! `daemon-api-contract-approval-none-waiting`, both said here (no service method
//! refuses them).

use std::sync::Arc;

use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Failure, Payload};
use approvals::{
    ask_key, ask_name, ask_root, bounded, claude_options, fact_book, name_root, ApprovalName,
    ClaimApprovalName, RecordApprovalAsked, SessionRef, APPROVALS_DOMAIN, APPROVAL_NAMES_DOMAIN,
};
use daemon_proto::v1;
use pending_approvals::PendingApprovals;
use run_ports::Edge;

use crate::server::Api;
use daemon_api_contract::{ApiError, ApiRefusal};

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

/// What an ask is recorded with: the session's run, its engine and agent,
/// and how long the ask waits.
struct Asking {
    run: String,
    engine: String,
    agent: String,
    approval_timeout: std::time::Duration,
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
        let asker = self.recorded_ask(&record).await?.ok_or_else(|| {
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

    /// What an ask of `record`'s current run is recorded with, from what the streams record: the
    /// run's spec for how long an ask waits, the session's mint for its
    /// engine and its loadout for its agent. None when the session has no
    /// run, or its run was enqueued with no recorded spec (a run from before
    /// the record, or a one-shot the daemon still hosts in memory).
    async fn recorded_ask(
        &self,
        record: &session_index::SessionRecord,
    ) -> Result<Option<Asking>, ApiError> {
        if record.incarnation == 0 {
            return Ok(None);
        }
        let run = runs::run_root(&record.id, record.incarnation);
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(run.clone()),
            edition: None,
        };
        let pages = self
            .parts
            .edge
            .events(&cover, 0)
            .await
            .map_err(ApiError::failed)?;
        let mut spec = None;
        for page in pages {
            let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = page.body else {
                continue;
            };
            if let runs::RunEvent::RunEnqueued(enqueued) =
                runs::RunEvent::from_any(&any).map_err(ApiError::failed)?
            {
                spec = enqueued.spec;
            }
        }
        let Some(seconds) = spec.and_then(|spec| spec.approval_timeout_seconds) else {
            return Ok(None);
        };
        Ok(Some(Asking {
            run: uuid::Uuid::from_slice(&run.value)
                .map(|id| id.hyphenated().to_string())
                .unwrap_or_default(),
            engine: record.engine.clone(),
            agent: self.recorded_agent(record).await?,
            approval_timeout: std::time::Duration::from_secs(u64::from(seconds)),
        }))
    }

    /// The agent the loadout of `record`'s current incarnation recorded, read
    /// from the session's own stream (the warm index may not have folded it
    /// yet); empty when none was.
    async fn recorded_agent(
        &self,
        record: &session_index::SessionRecord,
    ) -> Result<String, ApiError> {
        let root = uuid::Uuid::parse_str(&record.id).map_err(ApiError::failed)?;
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let pages = self
            .parts
            .edge
            .events(&cover, 0)
            .await
            .map_err(ApiError::failed)?;
        let mut agent = String::new();
        for page in pages {
            let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = page.body else {
                continue;
            };
            if let sessions::SessionEvent::LoadoutRecorded(recorded) =
                sessions::SessionEvent::from_any(&any).map_err(ApiError::failed)?
            {
                if recorded.incarnation == record.incarnation {
                    agent = recorded.loadout.agent;
                }
            }
        }
        Ok(agent)
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

#[cfg(test)]
#[path = "approvals.test.rs"]
mod tests;
