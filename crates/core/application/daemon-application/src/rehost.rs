//! The re-host (T19, S18, S47; owner s4-Q9): a child whose host was lost (a
//! killed daemon's, ended `orphaned` by the next start's sweep) is owed its
//! next incarnation, cause `rehost` (or, handed over by a draining daemon,
//! cause `drain`, s4-Q5: never counted, never refused for the budget), and
//! started again by the plan's start step, resuming its own conversation in its own session home, its
//! settings written again from the user's config (T21).
//!
//! The budget's evidence is counted here, never in decide (S47: decide
//! reads no clock): the session's own `IncarnationOwed{reason: rehost}`
//! recorded in the hour before the loss the re-host answers, by their
//! recorded instants (the loss is the last `IncarnationEnded`'s). Past
//! [`sessions::REHOST_BUDGET`] the session refuses it
//! (`sessions-rehost-budget-spent`): the child stays ended, runner-lost, and
//! its owner is told, `notice:<child>:<n>:rehost-budget`.

use angzarr_embedded::markup::TypedRefusal;
use angzarr_embedded::v1::event_page;
use angzarr_embedded::{Failure, Payload};
use run_ports::Edge;
use session_policies::{session_cover, supported, Keyed, RunAt, Sent};
use sessions::{RehostSession, SendMessage, SessionEvent, SessionRef, SessionRejection, Urgency};

use crate::daemon::{Daemon, Taken};

/// The hour the budget counts re-hosts over, in seconds.
const BUDGET_WINDOW_SECONDS: i64 = 3_600;

/// A session stream's events, each with the instant it was recorded (its
/// seconds since the epoch; 0 where none was stamped).
pub(crate) type Recorded = Vec<(i64, SessionEvent)>;

/// How many re-hosts of `events` fall in the hour before the last loss
/// (the last `IncarnationEnded`'s instant), that loss's own instant
/// included; a drain's hand-over is not one.
pub(crate) fn recent_rehosts(events: &Recorded) -> u32 {
    let Some(lost) = events
        .iter()
        .rev()
        .find(|(_, event)| matches!(event, SessionEvent::IncarnationEnded(_)))
        .map(|(at, _)| *at)
    else {
        return 0;
    };
    let since = lost - BUDGET_WINDOW_SECONDS;
    let counted = events
        .iter()
        .filter(|(at, event)| {
            matches!(event, SessionEvent::IncarnationOwed(owed) if owed.reason == sessions::REHOSTED)
                && (since..=lost).contains(at)
        })
        .count();
    u32::try_from(counted).unwrap_or(u32::MAX)
}

/// What the owner of a child its budget left ended is told.
pub(crate) fn budget_notice(harp: &str, recent: u32) -> String {
    format!(
        "child {harp} ended: its host was lost, and it was not re-hosted again \
         (re-hosted {recent} times in the last hour, its limit {})",
        sessions::REHOST_BUDGET
    )
}

impl<E: Edge> Daemon<E> {
    /// The re-host of the lost child `at` names (its next incarnation):
    /// `RehostSession` with its recent re-hosts, keyed by the incarnation it
    /// owes. Refused for the budget, its owner is told and the step is done
    /// (the child stays ended); refused otherwise (someone else resumed it,
    /// it closed), the step is refused.
    pub(crate) async fn rehost(
        &mut self,
        project_id: &str,
        at: &RunAt,
        harp: &str,
        owner: &SessionRef,
        cause: &str,
    ) -> Taken {
        let Some(cover) = session_cover(&at.session) else {
            return Taken::Done;
        };
        let events = match self.session_recorded(&cover).await {
            Ok(events) => events,
            Err(why) => {
                (self.say)(&format!(
                    "{harp}: its re-host cannot read its session: {why}"
                ));
                return Taken::Again;
            }
        };
        let recent = recent_rehosts(&events);
        let key = format!("rehost:{}:{}", at.session, at.incarnation);
        let rehost = Keyed {
            project_id: project_id.to_string(),
            cover,
            key: key.clone(),
            command: RehostSession {
                recent_rehosts: recent,
                cause: cause.to_string(),
            },
        };
        match self.edge.handle(rehost.book()).await {
            Ok(_) => {
                (self.say)(&format!(
                    "{harp}: re-hosted as its incarnation {} ({recent} in the last hour)",
                    at.incarnation
                ));
                Taken::Done
            }
            Err(Failure::Rejected(rejected)) => {
                let rejection = rejected
                    .rejection::<TypedRefusal>()
                    .and_then(|typed| SessionRejection::try_from(typed).ok());
                if !matches!(rejection, Some(SessionRejection::RehostBudgetSpent { .. })) {
                    (self.say)(&format!("{key} refused: {}", rejected.report()));
                    return Taken::Refused;
                }
                (self.say)(&format!(
                    "{harp}: not re-hosted again: re-hosted {recent} times in the last hour"
                ));
                self.tell_budget_spent(at, harp, owner, recent).await
            }
            Err(other) => {
                if self.complained.insert(key.clone()) {
                    (self.say)(&format!("{key} failed: {other:?}"));
                }
                Taken::Again
            }
        }
    }

    /// Tell `owner` its child `harp` was left ended for its budget.
    async fn tell_budget_spent(
        &mut self,
        at: &RunAt,
        harp: &str,
        owner: &SessionRef,
        recent: u32,
    ) -> Taken {
        let board = self.board.projector().board();
        let Some(owner_state) = board.session(&owner.session).filter(|o| !o.closed) else {
            return Taken::Done;
        };
        let supported = supported(owner_state, self.wakeable().contains(&owner.session));
        let urgency = match supported.contains(&Urgency::Wake) {
            true => Urgency::Wake,
            false => Urgency::Inform,
        };
        drop(board);
        let Some(cover) = session_cover(&owner.session) else {
            return Taken::Done;
        };
        let key = format!(
            "notice:{}:{}:rehost-budget",
            at.session,
            at.incarnation.saturating_sub(1)
        );
        let told = Sent::from(Keyed {
            project_id: owner.project_id.clone(),
            cover,
            key: key.clone(),
            command: SendMessage {
                message_id: key,
                urgency,
                text: budget_notice(harp, recent),
                requester: None,
                supported,
            },
        });
        match self.send(&told).await {
            Taken::Again => Taken::Again,
            _ => Taken::Done,
        }
    }

    /// The session stream `cover` names, each event with its recorded
    /// instant.
    async fn session_recorded(
        &self,
        cover: &angzarr_embedded::v1::Cover,
    ) -> Result<Recorded, String> {
        let pages = self
            .edge
            .events(cover, 0)
            .await
            .map_err(|e| e.to_string())?;
        Ok(pages
            .iter()
            .filter_map(|page| {
                let Some(event_page::Body::Event(any)) = &page.body else {
                    return None;
                };
                let event = SessionEvent::from_any(any).ok()?;
                let at = page
                    .meta
                    .as_ref()
                    .and_then(|meta| meta.recorded_at.as_ref())
                    .map_or(0, |at| at.seconds);
                Some((at, event))
            })
            .collect())
    }
}

#[cfg(test)]
#[path = "rehost.test.rs"]
mod tests;
