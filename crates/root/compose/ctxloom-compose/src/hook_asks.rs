//! The count of permission asks waiting in a session's project, for the
//! statusline (owner ruling 2026-11-24, reversing the 2026-11-22 "no
//! statusline count"): the frontend rings the bell, the statusline says how
//! many wait. It asks a daemon already serving and never starts one, and
//! answers 0, so the statusline shows nothing of asks, when no daemon
//! serves, the session names no project or the answer is not there in time.

use std::sync::Arc;
use std::time::Duration;

use daemon_ports::DaemonApi;
use process_env::EnvSnapshot;

/// How long a statusline waits for the daemon's answer. The engine redraws
/// the line often and abandons a slow one, so this is far under the hook's
/// bound ([`crate::daemon::HOOK_BOUND`]) and a statusline that cannot be
/// answered by then shows no count.
const ASK_WITHIN: Duration = Duration::from_secs(2);

/// The waiting asks of the project of the session `env` names.
pub fn waiting(env: &Arc<EnvSnapshot>) -> u32 {
    asked(env).unwrap_or(0)
}

fn asked(env: &Arc<EnvSnapshot>) -> Option<u32> {
    let session = env.non_empty(run_ports::SESSION_VARIABLE)?;
    let listing = crate::frontend_asks::project_listing(&session.to_string_lossy())?;
    let found = crate::daemon::hook_daemon_serving(env)?;
    let api = daemon_client::DaemonClient::new(&found, ASK_WITHIN);
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .ok()?;
    let listed = runtime
        .block_on(async { tokio::time::timeout(ASK_WITHIN, api.list_approvals(listing)).await })
        .ok()?
        .ok()?;
    Some(crate::frontend_asks::count_waiting(&listed.approvals))
}

#[cfg(test)]
#[path = "hook_asks.test.rs"]
mod tests;
