//! A rejection as a typed refusal, and back.
//!
//! The rules raise an [`ApprovalsRejection`]: a stable code, args and
//! English, which the edge prints. On the wire a command refuses with a
//! typed refusal MESSAGE of the protos (`proto/ctxloom/approvals/v1`),
//! listed in the `refuses` of the command that raises it, under the status
//! its `(refusal_status)` option declares, read here from the generated
//! constant; `TryFrom<&TypedRefusal>` turns it back, keyed by the refusal's
//! TYPE and by nothing else. The coordinator's status carries a refusal's
//! type and not its fields, so a field the refusal reaches a client without
//! reads back empty: the client says it from the ask as the daemon has it.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use crate::{
    AnswerApproval, ApprovalAggregate, ApprovalIsSettled, ApprovalNameAggregate, ApprovalNameTaken,
    ApprovalUnknown, ApprovalsRejection, ClaimApprovalName, MessageNotRelayed, OptionNotOffered,
    Refusal,
};

/// What `AnswerApproval` fails with for `rejection`, under the status its
/// message declares. A rejection it lists no message for is a bug.
pub(crate) fn answer_refused(
    rejection: ApprovalsRejection,
) -> Failed<Refuses<ApprovalAggregate, AnswerApproval>> {
    type Choice = Refuses<ApprovalAggregate, AnswerApproval>;
    match rejection {
        ApprovalsRejection::ApprovalUnknown { ask_id } => Failed::Rejected {
            rejection: Choice::from(ApprovalUnknown { ask_id }),
            status: <ApprovalUnknown as Refusal>::REFUSAL_STATUS,
        },
        ApprovalsRejection::ApprovalSettled { ask_id, how } => Failed::Rejected {
            rejection: Choice::from(ApprovalIsSettled {
                ask_id,
                how,
                option: String::new(),
            }),
            status: <ApprovalIsSettled as Refusal>::REFUSAL_STATUS,
        },
        ApprovalsRejection::OptionNotOffered {
            ask_id,
            option,
            offered,
        } => Failed::Rejected {
            rejection: Choice::from(OptionNotOffered {
                ask_id,
                option,
                offered,
            }),
            status: <OptionNotOffered as Refusal>::REFUSAL_STATUS,
        },
        ApprovalsRejection::MessageNotRelayed { ask_id, engine } => Failed::Rejected {
            rejection: Choice::from(MessageNotRelayed { ask_id, engine }),
            status: <MessageNotRelayed as Refusal>::REFUSAL_STATUS,
        },
        other @ ApprovalsRejection::NameTaken { .. } => Failed::bug(&other),
    }
}

/// What `ClaimApprovalName` fails with for a name another ask holds.
pub(crate) fn name_taken(
    ask_id: String,
) -> Failed<Refuses<ApprovalNameAggregate, ClaimApprovalName>> {
    Failed::Rejected {
        rejection: Refuses::<ApprovalNameAggregate, ClaimApprovalName>::from(ApprovalNameTaken {
            ask_id,
        }),
        status: <ApprovalNameTaken as Refusal>::REFUSAL_STATUS,
    }
}

/// A typed refusal that is none of this domain's.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct NotOurs;

/// The rejection an approvals command's typed refusal is, by its type: its
/// fields as the refusal carries them (empty where only its type came).
impl TryFrom<&TypedRefusal> for ApprovalsRejection {
    type Error = NotOurs;

    fn try_from(refusal: &TypedRefusal) -> Result<Self, NotOurs> {
        if let Some(ApprovalUnknown { ask_id }) = refusal.object() {
            return Ok(ApprovalsRejection::ApprovalUnknown { ask_id });
        }
        if let Some(ApprovalIsSettled { ask_id, how, .. }) = refusal.object() {
            return Ok(ApprovalsRejection::ApprovalSettled { ask_id, how });
        }
        if let Some(OptionNotOffered {
            ask_id,
            option,
            offered,
        }) = refusal.object()
        {
            return Ok(ApprovalsRejection::OptionNotOffered {
                ask_id,
                option,
                offered,
            });
        }
        if let Some(MessageNotRelayed { ask_id, engine }) = refusal.object() {
            return Ok(ApprovalsRejection::MessageNotRelayed { ask_id, engine });
        }
        if let Some(ApprovalNameTaken { ask_id }) = refusal.object() {
            return Ok(ApprovalsRejection::NameTaken { ask_id });
        }
        Err(NotOurs)
    }
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
