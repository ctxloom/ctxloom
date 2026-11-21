//! A rejection as a typed refusal, and back.
//!
//! The rules raise a [`TrustRejection`]: a stable code and English, which
//! the edge prints. On the wire a command refuses with a typed refusal
//! MESSAGE of the protos (`proto/ctxloom/trust/v1`), listed in the
//! `refuses` of the command that raises it, under the status its
//! `(refusal_status)` option declares, read here from the generated
//! constant; `TryFrom<&TypedRefusal>` turns it back for the application,
//! keyed by the refusal's TYPE and by nothing else.

use angzarr_embedded::markup::{Failed, Refuses, TypedRefusal};

use crate::{GrantTrust, PlanStale, Refusal, TrustAggregate, TrustRejection};

/// A grant whose preview showed `seen` and whose settings are `now`:
/// refused `PlanStale`, under the status the message declares.
pub(crate) fn plan_stale(seen: String, now: String) -> Failed<Refuses<TrustAggregate, GrantTrust>> {
    Failed::Rejected {
        rejection: Refuses::<TrustAggregate, GrantTrust>::from(PlanStale { seen, now }),
        status: <PlanStale as Refusal>::REFUSAL_STATUS,
    }
}

/// A typed refusal that is none of this domain's.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct NotOurs;

/// The rejection a trust command's typed refusal is, by its type. The
/// coordinator's status carries a refusal's type, not its fields, so the
/// rejection names none of them.
impl TryFrom<&TypedRefusal> for TrustRejection {
    type Error = NotOurs;

    fn try_from(refusal: &TypedRefusal) -> Result<Self, NotOurs> {
        if refusal.object::<PlanStale>().is_some() {
            return Ok(TrustRejection::PlanStale);
        }
        Err(NotOurs)
    }
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
