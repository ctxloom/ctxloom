//! The client's half of the permission asks (step 8 U4): the user's listing
//! and answer (`AnswerApproval` through the coordinator's `HandleSync` on
//! projector `pending-approvals`), and the MCP server's ask, wait and
//! withdrawal, as a [`Caller`] sends them; the daemon's half is
//! `daemon-api`'s `approvals`.

use angzarr_embedded::v1::Cover;
use approvals::{
    fact_book, is_ask_name, AnswerApproval, ApprovalName, ApprovalsRejection,
    RecordApprovalWithdrawn, APPROVALS_DOMAIN,
};
use daemon_api_contract::{ApiRejection, Rejection};
use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use pending_approvals::PENDING_APPROVALS;

use crate::caller::{on_daemon, Caller};
use crate::client::CallError;

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

/// The ask an approval's name ends with.
pub fn ask_of(approval: &v1::Approval) -> &str {
    approval.name.rsplit('/').next().unwrap_or_default()
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
