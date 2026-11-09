//! A rejection as a typed refusal, and back.
//!
//! The rules raise a [`RunRejection`]: a stable code, args and English,
//! which the edge prints. On the wire a command refuses with a typed refusal
//! MESSAGE of the protos (`proto/ctxloom/runs/v1`), one per rejection,
//! listed in the `refuses` of the commands that raise it. The `refuse!`
//! macro turns the one into the other for a handler, and
//! `TryFrom<&TypedRefusal>` turns it back for the application, keyed by the
//! refusal's TYPE and by nothing else: the business status a refusal travels
//! under is its message's `(refusal_status)` option, read here from the
//! generated constant, and never decides what a person is told.

use angzarr_embedded::markup::TypedRefusal;

use crate::{
    AlreadyEnqueued, CredentialHashInvalid, CredentialsNotDistinct, Ended, NoCredential, NoRun,
    NotItsStream, RunRejection, TurnInFlight,
};

/// What a command handler fails with for `rejection`: the typed refusal
/// message of the variant, one of the command's `refuses(..)` choice
/// `$choice` (its `From` each listed message), under the status the message
/// declares. A rejection the command lists no message for is a bug of the
/// handler, not a refusal.
macro_rules! refuse {
    ($rejection:ident, $value:expr, $choice:ty, [$($variant:ident $({$($field:ident),*})? => $message:ident),* $(,)?]) => {
        match $value {
            $(
                $rejection::$variant $({$($field),*})? => ::angzarr_embedded::markup::Failed::Rejected {
                    rejection: <$choice>::from($crate::$message { $($($field),*)? }),
                    status: <$crate::$message as $crate::Refusal>::REFUSAL_STATUS,
                },
            )*
            #[allow(unreachable_patterns)]
            other => ::angzarr_embedded::markup::Failed::bug(&other),
        }
    };
}

pub(crate) use refuse;

/// A typed refusal that is none of this domain's (no message of the person
/// reaches: the caller reports the refusal as the runtime made it).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct NotOurs;

/// The rejection a run command's typed refusal is, by its type.
impl TryFrom<&TypedRefusal> for RunRejection {
    type Error = NotOurs;

    fn try_from(refusal: &TypedRefusal) -> Result<Self, NotOurs> {
        if refusal.object::<AlreadyEnqueued>().is_some() {
            return Ok(RunRejection::AlreadyEnqueued);
        }
        if refusal.object::<NoRun>().is_some() {
            return Ok(RunRejection::NoRun);
        }
        if let Some(TurnInFlight { turn }) = refusal.object() {
            return Ok(RunRejection::TurnInFlight { turn });
        }
        if refusal.object::<Ended>().is_some() {
            return Ok(RunRejection::Ended);
        }
        if let Some(CredentialHashInvalid { length }) = refusal.object() {
            return Ok(RunRejection::CredentialHashInvalid { length });
        }
        if refusal.object::<NotItsStream>().is_some() {
            return Ok(RunRejection::NotItsStream);
        }
        if refusal.object::<CredentialsNotDistinct>().is_some() {
            return Ok(RunRejection::CredentialsNotDistinct);
        }
        if let Some(NoCredential { engine, variables }) = refusal.object() {
            return Ok(RunRejection::NoCredential { engine, variables });
        }
        Err(NotOurs)
    }
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
