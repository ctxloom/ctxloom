//! The trust aggregate on angzarr-embedded's markup.

use angzarr_embedded::markup::{Context, Failed, PageContext, Refuses, Snapshots, Unrefusable};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd};

use crate::refusal::plan_stale;
use crate::{
    GrantTrust, PlanStale, RevokeTrust, TrustCommand, TrustEvent, TrustGranted, TrustRevoked,
    Trusted,
};

#[derive(Clone, Debug, Default)]
pub struct TrustAggregate {
    trust: Trusted,
}

impl TrustAggregate {
    /// The folded trust.
    pub fn state(&self) -> &Trusted {
        &self.trust
    }
}

/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for TrustAggregate {
    type Anchor = crate::generated::TrustState;
}

#[aggregate(domain = "trust", command = TrustCommand, event = TrustEvent)]
impl TrustAggregate {
    /// Trust the project until revoked: refused stale when what the preview
    /// showed (`etag`) is not what is there now (`current_etag`), whether
    /// or not it is trusted already; trusting what is trusted records
    /// nothing more.
    #[handles_cmd(GrantTrust, refuses(PlanStale))]
    fn grant(
        &self,
        cmd: GrantTrust,
        ctx: &Context,
    ) -> Result<Option<TrustGranted>, Failed<Refuses<Self, GrantTrust>>> {
        let _ = ctx;
        if cmd.etag != cmd.current_etag {
            return Err(plan_stale(cmd.etag, cmd.current_etag));
        }
        if self.trust.trusted {
            return Ok(None);
        }
        Ok(Some(TrustGranted {
            name: cmd.name,
            project_id: cmd.project_id,
            actor: cmd.actor,
        }))
    }

    /// Take trust back; revoking what is not trusted records nothing.
    #[handles_cmd(RevokeTrust)]
    fn revoke(
        &self,
        cmd: RevokeTrust,
        ctx: &Context,
    ) -> Result<Option<TrustRevoked>, Failed<Unrefusable>> {
        let _ = ctx;
        Ok(self.trust.trusted.then_some(TrustRevoked {
            name: cmd.name,
            project_id: cmd.project_id,
            actor: cmd.actor,
        }))
    }

    #[applies(TrustGranted)]
    fn granted(&mut self, event: TrustGranted, page: &PageContext) {
        let _ = page;
        self.trust = Trusted {
            user: event.actor.user,
            name: event.name,
            project_id: event.project_id,
            trusted: true,
        };
    }

    #[applies(TrustRevoked)]
    fn revoked(&mut self, event: TrustRevoked, page: &PageContext) {
        let _ = page;
        self.trust = Trusted {
            user: event.actor.user,
            name: event.name,
            project_id: event.project_id,
            trusted: false,
        };
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
mod tests;
