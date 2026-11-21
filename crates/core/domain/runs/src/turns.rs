//! The run's turn gate (S-b): turns keyed by the session's messages. One turn
//! is in flight at a time; a steer may be queued while one is, and asks it to
//! be interrupted, once; a wake or an inform waits (refused `TurnInFlight`,
//! so it stays in the pending messages) until the turn ends.

#[cfg(test)]
#[path = "turns.test.rs"]
mod tests;
