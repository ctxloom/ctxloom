//! A primary's hooks and what they carry (the terminal wake, owner ruling
//! 2026-11-18; the lean hook RPC, owner budget 2026-11-18: under 20 ms
//! added per hook at p95). A hook asks the daemon ONCE
//! ([`HookContexts::hook_context`], `DeliverHookContext`), which answers
//! from its projections with no round awaited: the session's pending
//! messages of the urgencies the hook delivers, each OFFERED (marked delivered by the
//! hook's ack after it printed them), then what the session is owed (step 10c U13), joined within the
//! one budget (`crate::delivery`).
//!
//! - `turn_start` (claude's UserPromptSubmit) delivers its informs and
//!   wakes; a prompt that IS a wake line (`run_ports::wake_nonce`) redeems
//!   its wake, and is blocked when it delivered nothing (a stale wake: no
//!   model call spent on it); a human's prompt is never blocked;
//! - `post_tool` (PostToolUse) delivers no message: it would be a steer,
//!   and a primary is offered none (a child's messages are its turns). It
//!   carries what the session is owed alone.

use crate::Setup;

/// Why a stale wake line was blocked. DRAFT (2026-11-18).
pub(crate) const STALE_WAKE: &str =
    "ctxloom: a wake with no message waiting for it; nothing was sent to the model";

/// The urgencies a prompt delivers.
pub(crate) const AT_A_PROMPT: &[&str] = &["inform", "wake"];

/// What one hook carries, as the daemon answered it.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct HookCarried {
    /// The messages offered, then the owed context, joined.
    pub context: String,
    /// The ids of the messages whose elements `context` carries: OFFERED,
    /// not delivered (the daemon marked nothing). The hook acknowledges them
    /// ([`HookContexts::acknowledge`]) after it printed the context.
    pub offered: Vec<String>,
}

/// What a hook has to acknowledge once its answer is printed and flushed
/// (message delivery, owner 2026-11-19: at-least-once, never marked
/// delivered before it has been). The engine gives no acknowledgement of
/// its own, so "printed and flushed" is the best proof of delivery there
/// is; a hook that dies before the ack leaves the messages pending, offered
/// again.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Offered {
    /// The ids the printed context carries.
    pub ids: Vec<String>,
    /// The wake the prompt was; empty: none.
    pub wake: String,
}

impl Offered {
    /// Whether there is nothing to acknowledge.
    pub fn is_empty(&self) -> bool {
        self.ids.is_empty() && self.wake.is_empty()
    }
}

/// What a live session's hooks carry, THROUGH THE DAEMON, within the
/// hook's bound, as the library reads are.
pub trait HookContexts {
    /// ONE call for the hook `hook` (the engine's native name): the
    /// pending messages of `urgencies` (none: no message), offered (nothing
    /// marked delivered), then what the session is owed, within `budget`
    /// characters. `wake` is the wake the prompt is (empty: none). None
    /// when no session is named or no daemon answered in time (nothing
    /// offered; owed stays owed).
    fn hook_context(
        &self,
        hook: &str,
        urgencies: &[&str],
        wake: &str,
        budget: usize,
    ) -> Option<HookCarried>;

    /// The ack: `offered` was printed and flushed, so record it delivered
    /// (and redeem its wake). Idempotent on the daemon. Whether the daemon
    /// recorded it; a failure leaves the messages pending, offered again
    /// (a duplicate is tolerated, a loss is not). The default has nothing
    /// to acknowledge.
    fn acknowledge(&self, offered: &Offered) -> bool {
        let _ = offered;
        true
    }
}

impl Setup {
    /// The setup, its hooks carrying what the daemon answers through
    /// `contexts`.
    #[must_use]
    pub fn with_hook_contexts(mut self, contexts: Box<dyn HookContexts>) -> Setup {
        self.hook_contexts = Some(contexts);
        self
    }

    /// What the hook `native` carries: the messages of `urgencies` (with
    /// the wake `wake`) and the owed context, within `budget`; nothing
    /// with no daemon in time.
    pub(crate) fn carried(
        &self,
        native: &str,
        urgencies: &[&str],
        wake: &str,
        budget: usize,
    ) -> HookCarried {
        self.hook_contexts
            .as_deref()
            .and_then(|contexts| contexts.hook_context(native, urgencies, wake, budget))
            .unwrap_or_default()
    }

    /// A prompt's answer: its informs and wakes and its owed context, what
    /// the hook is to acknowledge once it printed them, and whether the
    /// prompt is to be blocked (a wake line with no message offered: stale;
    /// its wake is still acknowledged, so it is redeemed).
    pub(crate) fn prompt_delivery(
        &self,
        prompt: &str,
        native: &str,
        budget: usize,
    ) -> (String, Offered, bool) {
        let wake = run_ports::wake_nonce(prompt);
        let carried = self.carried(native, AT_A_PROMPT, wake.unwrap_or_default(), budget);
        let block = wake.is_some() && carried.offered.is_empty();
        let offered = Offered {
            ids: carried.offered,
            wake: wake.unwrap_or_default().to_string(),
        };
        (carried.context, offered, block)
    }

    /// The ack of an answer's `offered`, once its stdout is printed and
    /// flushed (nothing to acknowledge, nothing asked). Whether it was
    /// recorded.
    pub fn acknowledge(&self, offered: &Offered) -> bool {
        if offered.is_empty() {
            return true;
        }
        self.hook_contexts
            .as_deref()
            .is_none_or(|contexts| contexts.acknowledge(offered))
    }
}

#[cfg(test)]
#[path = "hooks_wake.test.rs"]
mod tests;
