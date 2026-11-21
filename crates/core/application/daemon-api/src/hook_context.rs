//! `DeliverHookContext` (10c, the lean hook RPC; owner budget 2026-11-18:
//! under 20 ms added per hook at p95): ONE call answers everything a live
//! session's hook carries, read from the PROJECTIONS with no round awaited.
//!
//! - A primary's pending messages of the urgencies the hook delivers (a
//!   prompt's: informs and wakes), as `setup_application::messages_context`
//!   fits them to the hook's budget, each in its own element with its id.
//!   This is a READ: it marks nothing delivered (owner, 2026-11-19: "we
//!   can tolerate duplicates, not losses"; at-least-once, never marked
//!   delivered before it has been). The hook prints the context to the
//!   engine, flushes stdout, and ONLY THEN sends
//!   [`Api::acknowledge_hook_delivery`], which records the ids delivered
//!   (`DeliverMessages`, idempotent per id). The engine gives no
//!   acknowledgement, so "printed and flushed" is the best proof of
//!   delivery there is. A hook that dies first leaves the messages pending,
//!   to be offered again; two hooks within one round may both carry one (a
//!   duplicate, which its visible id lets the model ignore), neither loses
//!   it. A message the budget cut off stays pending, its id unacked.
//! - Then what the session is owed, as `RedeliverContext` collects it,
//!   within what the messages left.
//!
//! Both are joined by `setup_application::compose_context`, the one join
//! point of a hook's context.

use angzarr_embedded::v1::{event_page, Cover, DeliveryOutcome, Uuid};
use angzarr_embedded::{Failure, Payload};
use daemon_proto::v1;
use run_ports::Edge;
use session_index::SessionRecord;
use sessions::{DeliverMessages, SessionEvent, SessionName};
use setup_application::{compose_context, messages_context, remaining, HookMessage};

use crate::reactions::budget_of;
use crate::server::Api;
use crate::{ApiError, ApiRefusal};

impl<E: Edge> Api<E> {
    /// `DeliverHookContext`: the messages the hook delivers, then what the
    /// session is owed, joined within the hook's budget. A session the
    /// daemon does not know is refused `SessionNotFound`.
    pub async fn deliver_hook_context(
        &self,
        request: v1::DeliverHookContextRequest,
    ) -> Result<v1::HookContext, ApiError> {
        let name = SessionName::parse(&request.name).map_err(|_| {
            ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: request.name.clone(),
                reason: "shape".to_string(),
            }))
        })?;
        // No round awaited: the index as it stands.
        let record = self
            .parts
            .index
            .index()
            .find_by_id(&name.uid())
            .ok_or_else(|| {
                ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                    name: request.name.clone(),
                }))
            })?;
        let budget = budget_of(request.budget);
        let (messages, offered) = self.hook_messages(&record, &request, budget).await?;
        // What the messages left of the budget; none left, the owed context
        // waits (it stays owed).
        let used = setup_application::HOOK_CONTEXT_BUDGET - remaining(&[&messages]);
        let left = budget.saturating_sub(used);
        let owed = match left {
            0 => v1::RedeliveredContext::default(),
            left => {
                self.redeliver_context(v1::RedeliverContextRequest {
                    name: request.name.clone(),
                    carried_by: request.carried_by.clone(),
                    budget: u32::try_from(left).unwrap_or(u32::MAX),
                    caller: request.caller.clone(),
                })
                .await?
            }
        };
        Ok(v1::HookContext {
            context: compose_context(&[&messages, &owed.context]),
            delivered: Vec::new(),
            changed: owed.changed,
            offered,
        })
    }

    /// The messages of `request.urgencies` pending for the primary `record`,
    /// within `budget`: their context and the ids it carries, marking
    /// nothing delivered. A child's messages are its turns, so it is given
    /// none here.
    async fn hook_messages(
        &self,
        record: &SessionRecord,
        request: &v1::DeliverHookContextRequest,
        budget: usize,
    ) -> Result<(String, Vec<String>), ApiError> {
        if record.owner.is_some() {
            return Ok((String::new(), Vec::new()));
        }
        let pending: Vec<HookMessage> = {
            let index = self.parts.index.index();
            self.parts
                .pending
                .pending()
                .pending(&record.id)
                .into_iter()
                .map(|message| HookMessage {
                    sender: hook_sender(
                        message.sender.map(|sender| index.name_of_ref(&sender)),
                        &message.message_id,
                    ),
                    id: message.message_id,
                    urgency: sessions::urgency_word(message.urgency).to_string(),
                    text: message.text,
                })
                .collect()
        };
        let urgencies: Vec<&str> = request.urgencies.iter().map(String::as_str).collect();
        let offered = messages_context(&pending, &urgencies, &record.harp, budget);
        Ok((offered.context, offered.ids))
    }

    /// `AcknowledgeHookDelivery`: the hook printed and flushed what
    /// `DeliverHookContext` offered, so `message_ids` are recorded delivered
    /// and the wake `wake_nonce` redeemed. Idempotent: an id already
    /// delivered, or a repeat of this very call, records nothing and
    /// answers none.
    pub async fn acknowledge_hook_delivery(
        &self,
        request: v1::AcknowledgeHookDeliveryRequest,
    ) -> Result<v1::HookDeliveryAcknowledged, ApiError> {
        let name = SessionName::parse(&request.name).map_err(|_| {
            ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: request.name.clone(),
                reason: "shape".to_string(),
            }))
        })?;
        let record = self
            .parts
            .index
            .index()
            .find_by_id(&name.uid())
            .ok_or_else(|| {
                ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                    name: request.name.clone(),
                }))
            })?;
        let delivered = self
            .record_delivery(&record, &request.message_ids, &request.wake_nonce)
            .await?;
        Ok(v1::HookDeliveryAcknowledged { delivered })
    }

    /// `ids` and the wake `nonce` recorded delivered on `record`'s session
    /// (the hook's ack, after it printed them), keyed `deliver:<session>:<nonce>:<ids, comma-joined>` (as the
    /// caller's `deliver`): the ids the aggregate recorded (those still
    /// pending; none for a DUPLICATE, which another call recorded).
    async fn record_delivery(
        &self,
        record: &SessionRecord,
        ids: &[String],
        nonce: &str,
    ) -> Result<Vec<String>, ApiError> {
        let root = uuid::Uuid::parse_str(&record.id).map_err(|_| ApiError::Failed {
            detail: format!("the session id {} is not a UUID", record.id),
        })?;
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let command = DeliverMessages {
            message_ids: ids.to_vec(),
            wake_nonce: nonce.to_string(),
        };
        let key = format!("deliver:{}:{nonce}:{}", record.id, ids.join(","));
        let answered = match self
            .parts
            .edge
            .handle(sessions::keyed_book(cover, &command, &key))
            .await
        {
            Ok(answered) => answered,
            Err(Failure::Rejected(rejected)) => return Err(ApiRefusal::rejected(&rejected).into()),
            Err(other) => return Err(ApiError::failed(other)),
        };
        if answered.delivery == DeliveryOutcome::Duplicate {
            return Ok(Vec::new());
        }
        Ok(delivered_ids(answered.events.as_ref()))
    }
}

/// Who a message is from, as its element says (`HookMessage::sender`): the
/// session that sent it, by name; with no requester, the daemon's own
/// notices (told by their id, `session_policies::is_daemon_message`) are
/// `ctxloom`, and the rest (empty) the user's. Nothing but a message the
/// user sent is ever from the user. DRAFT (2026-11-19).
fn hook_sender(requester: Option<String>, id: &str) -> String {
    match requester {
        Some(name) => name,
        None if session_policies::is_daemon_message(id) => {
            setup_application::DAEMON_SENDER.to_string()
        }
        None => String::new(),
    }
}

/// The ids every `MessagesDelivered` among `events` names, in order.
fn delivered_ids(events: Option<&angzarr_embedded::v1::EventBook>) -> Vec<String> {
    events
        .into_iter()
        .flat_map(|book| book.pages.iter())
        .filter_map(|page| match &page.body {
            Some(event_page::Body::Event(any)) => SessionEvent::from_any(any).ok(),
            _ => None,
        })
        .filter_map(|event| match event {
            SessionEvent::MessagesDelivered(delivered) => Some(delivered.message_ids),
            _ => None,
        })
        .flatten()
        .collect()
}

#[cfg(test)]
#[path = "hook_context.test.rs"]
mod tests;
