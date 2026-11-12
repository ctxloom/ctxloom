//! The name-claim aggregate: one stream per session name, holding which
//! session has it (D5 Q1). Its root is the name's ([`crate::SessionName::root`],
//! owner 2026-11-12, slice 2 B3/re-root): the session's own stream root, in
//! the `session-names` domain, so a name is unique because a stream is.

use angzarr_embedded::markup::{Context, Failed, PageContext, Refuses};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd};

use crate::refusal::refuse;
use crate::{
    ClaimName, Claimed, NameClaimCommand, NameClaimEvent, NameClaimState, NamesTaken, NoClaim,
    NotHolder, ReleaseName, Released, SessionRejection,
};

/// The claim aggregate: one name, folded from its stream.
#[derive(Clone, Debug, Default)]
pub struct NameClaimAggregate {
    claim: NameClaimState,
}

impl NameClaimAggregate {
    /// The folded claim.
    pub fn state(&self) -> &NameClaimState {
        &self.claim
    }
}

// The domain is NAMES_DOMAIN, spelled as the markup takes it, a literal.
#[aggregate(domain = "session-names", command = NameClaimCommand, event = NameClaimEvent)]
impl NameClaimAggregate {
    /// Take the name for a session; `None` when its holder asks again.
    #[handles_cmd(ClaimName, refuses(NamesTaken))]
    fn claim(
        &self,
        cmd: ClaimName,
        ctx: &Context,
    ) -> Result<Option<Claimed>, Failed<Refuses<Self, ClaimName>>> {
        let _ = ctx;
        if self.claim.session == cmd.session {
            return Ok(None);
        }
        if !self.claim.session.is_empty() {
            return Err(refuse!(
                SessionRejection,
                SessionRejection::NamesTaken { harp: cmd.harp },
                Refuses<Self, ClaimName>,
                [NamesTaken{harp} => NamesTaken]
            ));
        }
        Ok(Some(Claimed {
            project_id: cmd.project_id,
            harp: cmd.harp,
            session: cmd.session,
        }))
    }

    /// Give the name back.
    #[handles_cmd(ReleaseName, refuses(NoClaim, NotHolder))]
    fn release(
        &self,
        cmd: ReleaseName,
        ctx: &Context,
    ) -> Result<Released, Failed<Refuses<Self, ReleaseName>>> {
        let _ = ctx;
        if self.claim.session.is_empty() {
            return Err(refuse!(
                SessionRejection,
                SessionRejection::NoClaim,
                Refuses<Self, ReleaseName>,
                [NoClaim => NoClaim]
            ));
        }
        if self.claim.session != cmd.session {
            return Err(refuse!(
                SessionRejection,
                SessionRejection::NotHolder {
                    harp: self.claim.harp.clone(),
                },
                Refuses<Self, ReleaseName>,
                [NotHolder{harp} => NotHolder]
            ));
        }
        Ok(Released {})
    }

    #[applies(Claimed)]
    fn claimed(&mut self, event: Claimed, page: &PageContext) {
        let _ = page;
        self.claim = NameClaimState {
            project_id: event.project_id,
            harp: event.harp,
            session: event.session,
            claimed: true,
        };
    }

    #[applies(Released)]
    fn released(&mut self, event: Released, page: &PageContext) {
        let _ = (event, page);
        self.claim.session.clear();
    }
}

#[cfg(test)]
#[path = "names.test.rs"]
mod tests;
