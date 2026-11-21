//! The hosting projector's state (step 10e Stage 2a): what each agent's
//! engine is doing, as the session and run streams say it, folded by the
//! board from sequence 0 on every start of the process (an ephemeral
//! subscription's fold; no durable mark).
//!
//! The state is LOCAL to the projector and DERIVED, never stored: the
//! session aggregate decides whether a restart is owed (`RestartOwed`, in its
//! stream), and this only reads where the engine stands against it.
//!
//! - **Active / Idle** is the llm's real activity: a turn queued on the
//!   open incarnation's run and not ended is Active (the run stream's
//!   `RecordTurnStarted`/`Completed`/`Failed`, `RunState.turn_in_flight`); the
//!   engine's own end (`RunEnded`, whatever its cause) forces Idle, so a turn
//!   end that was never delivered cannot leave an agent Active for ever.
//! - **Parked** is a restart owed while a turn is in flight: a deferral, never
//!   a held handler. It ends on the Active to Idle edge, a turn end or the
//!   engine's exit.
//! - **Restarting** is a restart owed with nothing in flight: the engine is to
//!   be ended now (or, once it has ended, the next incarnation is owed).
//!
//! It does not stop when the engine does: the next event finds it alive.

use sessions::RestartReason;

use crate::Board;

/// What one agent's engine is doing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Agent {
    /// No turn is in flight, or no engine runs.
    Idle,
    /// A turn is in flight, and no restart is owed.
    Active { turn: u64 },
    /// A restart is owed, and waits for the turn in flight to end.
    Parked { turn: u64, reason: RestartReason },
    /// A restart is owed, and nothing is in flight.
    Restarting { reason: RestartReason },
}

/// A restart to carry out now: the engine of `session`'s open `incarnation`
/// is to be ended, and its next incarnation resumed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RestartDue {
    pub session: String,
    pub incarnation: u32,
    pub reason: RestartReason,
}

impl Board {
    /// What the engine of `session` is doing.
    pub fn agent(&self, session: &str) -> Agent {
        let Some(state) = self.session(session) else {
            return Agent::Idle;
        };
        if state.closed {
            return Agent::Idle;
        }
        let in_flight = state
            .open_incarnation
            .and_then(|incarnation| self.run(session, incarnation))
            .filter(|run| run.live())
            .and_then(|run| run.state.turn_in_flight);
        match (&state.restart_owed, in_flight) {
            (None, None) => Agent::Idle,
            (None, Some(turn)) => Agent::Active { turn },
            (Some(owed), Some(turn)) => Agent::Parked {
                turn,
                reason: owed.reason,
            },
            (Some(owed), None) => Agent::Restarting {
                reason: owed.reason,
            },
        }
    }

    /// The restarts to carry out now, by session: an agent restarting whose
    /// engine still runs (its incarnation open, its run live and not ended).
    /// One whose engine has ended is owed its next incarnation instead (the
    /// plan's re-host), and is not listed.
    pub fn restarts_due(&self) -> Vec<RestartDue> {
        self.sessions()
            .filter_map(|(id, state)| {
                let Agent::Restarting { reason } = self.agent(id) else {
                    return None;
                };
                let incarnation = state.open_incarnation?;
                self.run(id, incarnation)
                    .is_some_and(|run| run.live())
                    .then(|| RestartDue {
                        session: id.to_string(),
                        incarnation,
                        reason,
                    })
            })
            .collect()
    }
}

#[cfg(test)]
#[path = "hosting.test.rs"]
mod tests;

#[cfg(test)]
#[path = "bus.test.rs"]
mod bus;
