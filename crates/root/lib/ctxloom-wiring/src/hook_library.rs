//! The engines' hooks read the library THROUGH THE DAEMON (step 10a U2, O3,
//! owner rulings 2026-11-16 and 2026-11-17): the session-start nudge asks
//! `ListAgents` and `ListProfiles`, the skill mates `ListSkills`, each of
//! the user's daemon (found, else started, as a hook's delivery is), all
//! within the hook's bound (S16, `HOOK_BOUND`), degraded. With no daemon
//! in time, or a read that failed, the hook says nothing of the library: it
//! never reads it in process. A hook whose environment names an endpoint
//! (a container's arrival, OD-13) never starts a daemon, so it asks only a
//! daemon that serves already.

use std::sync::Arc;
use std::time::{Duration, Instant};

use daemon_api::LibraryReads;
use daemon_ports::{Daemons, Unreached};
use daemon_proto::v1;
use process_env::EnvSnapshot;
use setup_application::{hook_skills, HookLibrary, HookSkill};

use crate::Wiring;

/// The user's daemon as a hook's library read reaches it, until `until`.
pub(crate) struct HookDaemons {
    pub(crate) env: Arc<EnvSnapshot>,
    /// The binary's stamp, which its every request names (the daemon
    /// admits only its own).
    pub(crate) stamp: String,
    pub(crate) until: Instant,
    /// Whether it may start one (none where the environment names an
    /// endpoint).
    pub(crate) starts: bool,
    /// Whether its first reach is the discovery file's daemon, asked with
    /// no health check (the lean hook RPC: a probe is a TLS connection of
    /// its own); a later reach (the first answered unreachable) probes.
    pub(crate) trusts: std::sync::atomic::AtomicBool,
}

impl HookDaemons {
    /// The daemon of a hook's one call (the lean hook RPC): its first
    /// reach trusts the discovery file.
    pub(crate) fn trusting(
        env: Arc<EnvSnapshot>,
        stamp: &str,
        until: Instant,
        starts: bool,
    ) -> HookDaemons {
        HookDaemons {
            env,
            stamp: stamp.to_string(),
            until,
            starts,
            trusts: std::sync::atomic::AtomicBool::new(true),
        }
    }

    fn remaining(&self) -> Duration {
        self.until.saturating_duration_since(Instant::now())
    }

    fn client(&self, found: &run_ports::Discovery) -> daemon_client::DaemonClient {
        daemon_client::DaemonClient::new(found, self.remaining()).filling(crate::daemon::caller_of(
            &self.env,
            v1::client::Kind::Hook,
            &self.stamp,
        ))
    }

    fn found(&self, wait: Duration) -> Option<run_ports::Discovery> {
        if self.starts {
            crate::daemon::hook_daemon(&self.env, wait)
        } else {
            crate::daemon::hook_daemon_serving(&self.env)
        }
    }
}

impl Daemons for HookDaemons {
    type Api = daemon_client::DaemonClient;

    async fn serving(&self) -> Result<Option<daemon_client::DaemonClient>, Unreached> {
        Ok(crate::daemon::hook_daemon_serving(&self.env).map(|found| self.client(&found)))
    }

    async fn reach(&self) -> Result<daemon_client::DaemonClient, Unreached> {
        if self.trusts.swap(false, std::sync::atomic::Ordering::SeqCst) {
            if let Some(found) = crate::daemon::hook_daemon_unprobed(&self.env) {
                return Ok(self.client(&found));
            }
        }
        let wait = self
            .remaining()
            .saturating_sub(crate::daemon::DELIVERY_MARGIN)
            .min(crate::daemon::SPAWN_WAIT);
        match self.found(wait) {
            Some(found) => Ok(self.client(&found)),
            None => Err(Unreached::NotStarted { log: String::new() }),
        }
    }
}

/// The hooks' library reads over the user's daemon (O3).
pub(crate) struct DaemonHookLibrary {
    pub(crate) env: Arc<EnvSnapshot>,
    /// The binary's stamp, which the daemon admits only when it is its own.
    pub(crate) stamp: String,
    /// When the hook's bound ends.
    pub(crate) until: Instant,
}

impl DaemonHookLibrary {
    /// A hook's reads, its bound starting now.
    pub(crate) fn starting_now(env: Arc<EnvSnapshot>, stamp: &str) -> DaemonHookLibrary {
        DaemonHookLibrary {
            env,
            stamp: stamp.to_string(),
            until: Instant::now() + crate::daemon::HOOK_BOUND,
        }
    }

    /// `read` over this hook's client of the daemon, within the bound;
    /// none when anything on the way does not answer.
    fn ask<T>(
        &self,
        read: impl AsyncFnOnce(&daemon_api::Client<HookDaemons>) -> Option<T>,
    ) -> Option<T> {
        let wiring = Wiring::new(Arc::clone(&self.env)).ok()?;
        let registration = wiring.library_registration(crate::Minting::AtRoot).ok()?;
        let starts = self
            .env
            .utf8(run_ports::ENDPOINT_VARIABLE)
            .is_none_or(str::is_empty);
        let daemons = HookDaemons {
            env: Arc::clone(&self.env),
            stamp: self.stamp.clone(),
            until: self.until,
            starts,
            trusts: std::sync::atomic::AtomicBool::new(false),
        };
        let client = daemon_api::Client::new(
            daemons,
            registration,
            crate::daemon::caller_of(wiring.env(), v1::client::Kind::Hook, &self.stamp),
        );
        let within = self.until.saturating_duration_since(Instant::now());
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .ok()?;
        runtime
            .block_on(async { tokio::time::timeout(within, read(&client)).await })
            .ok()
            .flatten()
    }
}

/// A hook's reads are degraded: a config a strict load refuses must not
/// silence it.
fn degraded() -> v1::Globals {
    v1::Globals {
        degraded: true,
        ..v1::Globals::default()
    }
}

impl HookLibrary for DaemonHookLibrary {
    fn agents_and_profiles(&self) -> Option<(bool, bool)> {
        self.ask(async |client| {
            let agents = client
                .list_agents(
                    "",
                    v1::ListAgentsRequest {
                        caller: v1::Caller::of(Some(degraded()), None),
                        ..v1::ListAgentsRequest::default()
                    },
                )
                .await
                .ok()?;
            let profiles = client
                .list_profiles(
                    "",
                    v1::ListProfilesRequest {
                        caller: v1::Caller::of(Some(degraded()), None),
                        ..v1::ListProfilesRequest::default()
                    },
                )
                .await
                .ok()?;
            Some((
                !agents.answer.agents.is_empty(),
                !profiles.answer.profiles.is_empty(),
            ))
        })
    }

    fn skills(&self) -> Option<Vec<HookSkill>> {
        self.ask(async |client| {
            let listed = client
                .list_skills(
                    "",
                    v1::ListSkillsRequest {
                        caller: v1::Caller::of(Some(degraded()), None),
                        ..v1::ListSkillsRequest::default()
                    },
                )
                .await
                .ok()?;
            Some(hook_skills(&listed.answer))
        })
    }
}
