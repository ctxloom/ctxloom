//! A live session's hooks reach what they carry THROUGH THE DAEMON in ONE
//! call (the lean hook RPC, owner budget 2026-11-18: under 20 ms added per
//! hook at p95): `DeliverHookContext` of the hook's session
//! (`CTXLOOM_SESSION`) as a caller of the user's daemon, within the hook's
//! bound. Its daemon is the one the discovery file names, asked with no
//! health check first (a TLS connection of its own); only when it cannot
//! be reached is it probed, and started, as every hook's daemon is. With no
//! session named, or no daemon in time, the hook carries nothing.

use std::sync::Arc;
use std::time::Instant;

use daemon_api::Caller;
use process_env::EnvSnapshot;
use setup_application::{HookCarried, HookContexts, Offered};

use crate::hook_library::HookDaemons;

/// The hook's session's one call over the user's daemon.
pub(crate) struct DaemonHookContexts {
    pub(crate) env: Arc<EnvSnapshot>,
    /// The binary's stamp, which the daemon admits only when it is its own.
    pub(crate) stamp: String,
    /// When the hook's bound ends.
    pub(crate) until: Instant,
}

impl DaemonHookContexts {
    /// A hook's call, its bound starting now.
    pub(crate) fn starting_now(env: Arc<EnvSnapshot>, stamp: &str) -> DaemonHookContexts {
        DaemonHookContexts {
            env,
            stamp: stamp.to_string(),
            until: Instant::now() + crate::daemon::HOOK_BOUND,
        }
    }
}

impl DaemonHookContexts {
    /// The hook's session as a caller of the user's daemon, and the
    /// working directory it speaks from; None with no session named.
    fn caller(&self) -> Option<(Caller<HookDaemons>, String)> {
        let session = self.env.non_empty(run_ports::SESSION_VARIABLE)?;
        let session = session.to_string_lossy().into_owned();
        let starts = self
            .env
            .utf8(run_ports::ENDPOINT_VARIABLE)
            .is_none_or(str::is_empty);
        let daemons = HookDaemons::trusting(Arc::clone(&self.env), &self.stamp, self.until, starts);
        let caller = Caller::new(
            daemons,
            &session,
            crate::daemon::caller_of(&self.env, daemon_proto::v1::client::Kind::Hook, &self.stamp),
            Box::new(crate::daemon::request_id),
        )
        .ok()?;
        let cwd = self
            .env
            .cwd()
            .map(|cwd| cwd.display().to_string())
            .unwrap_or_default();
        Some((caller, cwd))
    }

    /// `call` on a runtime of its own, within what is left of the hook's
    /// bound.
    fn within<T>(&self, call: impl std::future::Future<Output = T>) -> Option<T> {
        let within = self.until.saturating_duration_since(Instant::now());
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .ok()?;
        runtime
            .block_on(async { tokio::time::timeout(within, call).await })
            .ok()
    }
}

impl HookContexts for DaemonHookContexts {
    fn hook_context(
        &self,
        hook: &str,
        urgencies: &[&str],
        wake: &str,
        budget: usize,
    ) -> Option<HookCarried> {
        let (caller, cwd) = self.caller()?;
        let answered = self
            .within(caller.hook_context(hook, (urgencies, wake), budget, &cwd))?
            .ok()?;
        Some(HookCarried {
            context: answered.context,
            offered: answered.offered,
        })
    }

    /// The ack, a call of its own AFTER the hook printed and flushed. Not
    /// answered in time, it is not recorded: the messages stay pending and
    /// are offered again.
    fn acknowledge(&self, offered: &Offered) -> bool {
        let Some((caller, cwd)) = self.caller() else {
            return false;
        };
        self.within(caller.acknowledge_hook_delivery(&offered.ids, &offered.wake, &cwd))
            .is_some_and(|answered| answered.is_ok())
    }
}

#[cfg(test)]
#[path = "hook_messages.test.rs"]
mod tests;
