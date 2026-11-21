//! The ask names' claim aggregate (Q7): one stream per name, holding which
//! ask has it, so an ask's name is unique among its project's asks because
//! a stream is.

use angzarr_embedded::markup::{Context, Failed, PageContext, Refuses, Snapshots};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd};

use crate::refusal::name_taken;
use crate::{
    ApprovalNameClaimed, ApprovalNameCommand, ApprovalNameEvent, ApprovalNameTaken,
    ClaimApprovalName,
};

/// A name's claim as its stream holds it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NameClaim {
    pub project_id: String,
    pub ask_id: String,
    /// The root of the ask's stream that holds the name (its text); empty
    /// while free.
    pub approval: String,
}

#[derive(Clone, Debug, Default)]
pub struct ApprovalNameAggregate {
    claim: NameClaim,
}

impl ApprovalNameAggregate {
    /// The folded claim.
    pub fn state(&self) -> &NameClaim {
        &self.claim
    }
}

/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for ApprovalNameAggregate {
    type Anchor = crate::generated::ApprovalNameState;
}

#[aggregate(domain = "approval-names", command = ApprovalNameCommand, event = ApprovalNameEvent)]
impl ApprovalNameAggregate {
    /// Take the name for an ask; `None` when its holder asks again (a
    /// retry), refused when another ask holds it.
    #[handles_cmd(ClaimApprovalName, refuses(ApprovalNameTaken))]
    fn claim(
        &self,
        cmd: ClaimApprovalName,
        ctx: &Context,
    ) -> Result<Option<ApprovalNameClaimed>, Failed<Refuses<Self, ClaimApprovalName>>> {
        let _ = ctx;
        if self.claim.approval == cmd.approval {
            return Ok(None);
        }
        if !self.claim.approval.is_empty() {
            return Err(name_taken(cmd.ask_id));
        }
        Ok(Some(ApprovalNameClaimed {
            project_id: cmd.project_id,
            ask_id: cmd.ask_id,
            approval: cmd.approval,
        }))
    }

    #[applies(ApprovalNameClaimed)]
    fn claimed(&mut self, event: ApprovalNameClaimed, page: &PageContext) {
        let _ = page;
        self.claim = NameClaim {
            project_id: event.project_id,
            ask_id: event.ask_id,
            approval: event.approval,
        };
    }
}

#[cfg(test)]
#[path = "names.test.rs"]
mod tests;
