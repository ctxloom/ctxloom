//! The one root of the INFRASTRUCTURE tree (owner, 2026-11-14): every
//! non-business answer a listener of ctxloom's gives, named once with the
//! gRPC status it travels with.
//!
//! There are two trees of answers that are not a success:
//!
//! - a BUSINESS refusal, a command's alone: a typed refusal message of the
//!   protos whose status is its `(refusal_status)` option and nowhere else
//!   (no Rust source spells it; the generated `REFUSAL_STATUS` is how Rust
//!   names it);
//! - an INFRASTRUCTURE error: what the plumbing answers whatever was asked
//!   (a fact for a session there is no record of, a write that lost the
//!   race to its stream's head, a failure, a method not served, a
//!   credential refused, nobody there). Its status is named in Rust, HERE,
//!   and nowhere else: both listeners answer through [`Infrastructure`]
//!   (the hook listener's command half, `runs-application`'s
//!   `EdgeCommands`; the daemon's `Ctxloom` service, `daemon-api-grpc`) and
//!   both clients read an answer back through [`Infrastructure::read`]
//!   (`hook-sink-grpc`, `daemon-client`). The workspace test that greps the
//!   sources for a status spelled in Rust allows this file alone.
//!
//! A fact is never refused for a business reason: what a fact can be
//! answered, but applied or duplicate, is of this tree.
//!
//! The status is angzarr's transport-free [`Code`] (the gRPC wire numbers),
//! so the core names it without a transport; an adapter turns it into its
//! transport's code by its number.

pub use angzarr_coordinator::angzarr_coordinator_base::Code;

use crate::NO_SUCH_SESSION;

/// The reason key angzarr answers a fact its kernel dead-lettered with,
/// under INTERNAL: final, a resend would be parked again.
pub const DEAD_LETTERED: &str = "ANGZARR_REASON_FACT_DEAD_LETTERED";

/// The `google.rpc.ErrorInfo` domain of a failure the daemon says in a
/// diagnostic of its own (its reason the diagnostic's code): the key of
/// [`Infrastructure::Unmet`] under failed-precondition.
pub const DIAGNOSED: &str = "ctxloom.dev/diagnostic";

/// Each infrastructure error, with its status ([`Infrastructure::code`]) and,
/// where one status carries more than one, the reason key that tells them
/// apart ([`Infrastructure::key`]).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Infrastructure {
    /// A capture fact for a session the daemon has no record of
    /// (capture.proto's `FactSessionUnknown`): not found, keyed
    /// [`NO_SUCH_SESSION`]. Final.
    SessionUnknown,
    /// A fact the kernel dead-lettered: INTERNAL, keyed [`DEAD_LETTERED`].
    /// Final.
    Parked,
    /// A write that lost the race to its stream's head (the kernel's etag,
    /// or sequence, conflict): ABORTED. A resend may succeed.
    Conflict,
    /// Something broke behind the listener (the store, the kernel, a task):
    /// INTERNAL.
    Failed,
    /// A request the kernel cannot read (a malformed book): invalid argument.
    Malformed,
    /// A method this listener does not serve (yet): UNIMPLEMENTED.
    Unimplemented,
    /// No credential, or one the gate does not know: UNAUTHENTICATED.
    Unauthenticated,
    /// Nobody there to answer: UNAVAILABLE.
    Unavailable,
    /// The deadline passed: DEADLINE_EXCEEDED.
    TimedOut,
    /// A limit was reached: RESOURCE_EXHAUSTED.
    Exhausted,
    /// An error the transport could not name: UNKNOWN.
    Unknown,
    /// Data was lost or damaged: DATA_LOSS.
    DataLoss,
    /// A precondition of the host unmet, said in a diagnostic of its own
    /// (git missing or too old, step 10a U2, owner ruling 2026-11-17):
    /// failed-precondition, keyed [`DIAGNOSED`] (not UNAVAILABLE, which a
    /// client reads as an unreachable daemon; not INTERNAL).
    Unmet,
}

impl Infrastructure {
    /// Every infrastructure error, in the order the enum lists them.
    pub const ALL: [Infrastructure; 13] = [
        Infrastructure::SessionUnknown,
        Infrastructure::Parked,
        Infrastructure::Conflict,
        Infrastructure::Failed,
        Infrastructure::Malformed,
        Infrastructure::Unimplemented,
        Infrastructure::Unauthenticated,
        Infrastructure::Unavailable,
        Infrastructure::TimedOut,
        Infrastructure::Exhausted,
        Infrastructure::Unknown,
        Infrastructure::DataLoss,
        Infrastructure::Unmet,
    ];

    /// The status this error travels with: the one place it is named.
    pub fn code(self) -> Code {
        match self {
            Infrastructure::SessionUnknown => Code::NotFound,
            Infrastructure::Parked => Code::Internal,
            Infrastructure::Conflict => Code::Aborted,
            Infrastructure::Failed => Code::Internal,
            Infrastructure::Malformed => Code::InvalidArgument,
            Infrastructure::Unimplemented => Code::Unimplemented,
            Infrastructure::Unauthenticated => Code::Unauthenticated,
            Infrastructure::Unavailable => Code::Unavailable,
            Infrastructure::TimedOut => Code::DeadlineExceeded,
            Infrastructure::Exhausted => Code::ResourceExhausted,
            Infrastructure::Unknown => Code::Unknown,
            Infrastructure::DataLoss => Code::DataLoss,
            Infrastructure::Unmet => Code::FailedPrecondition,
        }
    }

    /// The status's number on the wire.
    pub fn status(self) -> i32 {
        self.code().number()
    }

    /// The reason key that tells this error apart from another of its
    /// status (in the status's message, or its `google.rpc.ErrorInfo`
    /// reason), if it has one.
    pub fn key(self) -> Option<&'static str> {
        match self {
            Infrastructure::SessionUnknown => Some(NO_SUCH_SESSION),
            Infrastructure::Parked => Some(DEAD_LETTERED),
            Infrastructure::Unmet => Some(DIAGNOSED),
            _ => None,
        }
    }

    /// The infrastructure error an answer of status `status` names, given
    /// the texts that may carry its key (`keys`: the status's message and
    /// its reason), or `None` when it names none (a business refusal's
    /// status, OK, CANCELLED, a number that is no status). A passed deadline
    /// is that, whatever the texts; a dead letter is keyed under INTERNAL
    /// alone; the no-such-session key names its error under any other
    /// status; otherwise the status names the error it alone travels with.
    pub fn read(status: i32, keys: &[&str]) -> Option<Infrastructure> {
        let code = Code::from_number(status)?;
        let keyed = |key: &str| keys.contains(&key);
        if code == Infrastructure::TimedOut.code() {
            return Some(Infrastructure::TimedOut);
        }
        if code == Infrastructure::Parked.code() && keyed(DEAD_LETTERED) {
            return Some(Infrastructure::Parked);
        }
        if code == Infrastructure::Unmet.code() && keyed(DIAGNOSED) {
            return Some(Infrastructure::Unmet);
        }
        if keyed(NO_SUCH_SESSION) {
            return Some(Infrastructure::SessionUnknown);
        }
        Infrastructure::ALL
            .into_iter()
            .find(|error| error.key().is_none() && error.code() == code)
    }
}

#[cfg(test)]
#[path = "infrastructure.test.rs"]
mod tests;
