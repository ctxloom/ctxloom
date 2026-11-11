//! A rejection as a typed refusal, and back.
//!
//! The rules raise a [`SessionRejection`]: a stable code, args and English,
//! which the edge prints. On the wire a command refuses with a typed refusal
//! MESSAGE of the protos (`proto/ctxloom/sessions/v1`), one per rejection,
//! listed in the `refuses` of the commands that raise it. The `refuse!`
//! macro turns the one into the other for a handler, and
//! `TryFrom<&TypedRefusal>` turns it back for the application, keyed by the
//! refusal's TYPE and by nothing else: the business status a refusal travels
//! under is its message's `(refusal_status)` option, read here from the
//! generated constant, and never decides what a person is told.

use angzarr_embedded::markup::TypedRefusal;

use crate::{
    ChildCannotStop, EngineMismatch, HarpInvalid, HarpTooLong, NamesTaken, NoClaim,
    NoNativeSession, NoSession, NotHolder, SessionDestroyed, SessionExists, SessionLive,
    SessionRejection, SessionRunning, SessionUnknown,
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

/// The rejection a session or name-claim command's typed refusal is, by its type.
impl TryFrom<&TypedRefusal> for SessionRejection {
    type Error = NotOurs;

    fn try_from(refusal: &TypedRefusal) -> Result<Self, NotOurs> {
        if let Some(SessionExists { harp }) = refusal.object() {
            return Ok(SessionRejection::SessionExists { harp });
        }
        if refusal.object::<NoSession>().is_some() {
            return Ok(SessionRejection::NoSession);
        }
        if let Some(HarpInvalid { harp, reason }) = refusal.object() {
            return Ok(SessionRejection::HarpInvalid { harp, reason });
        }
        if let Some(HarpTooLong {
            harp,
            limit,
            length,
        }) = refusal.object()
        {
            return Ok(SessionRejection::HarpTooLong {
                harp,
                limit,
                length,
            });
        }
        if let Some(NamesTaken { harp }) = refusal.object() {
            return Ok(SessionRejection::NamesTaken { harp });
        }
        if refusal.object::<NoClaim>().is_some() {
            return Ok(SessionRejection::NoClaim);
        }
        if let Some(NotHolder { harp }) = refusal.object() {
            return Ok(SessionRejection::NotHolder { harp });
        }
        if let Some(ChildCannotStop { depth }) = refusal.object() {
            return Ok(SessionRejection::ChildCannotStop { depth });
        }
        if let Some(SessionLive { harp }) = refusal.object() {
            return Ok(SessionRejection::SessionLive { harp });
        }
        if let Some(SessionUnknown { harp }) = refusal.object() {
            return Ok(SessionRejection::SessionUnknown { harp });
        }
        if let Some(SessionDestroyed { harp }) = refusal.object() {
            return Ok(SessionRejection::SessionDestroyed { harp });
        }
        if let Some(SessionRunning { harp }) = refusal.object() {
            return Ok(SessionRejection::SessionRunning { harp });
        }
        if let Some(NoNativeSession { harp }) = refusal.object() {
            return Ok(SessionRejection::NoNativeSession { harp });
        }
        if let Some(EngineMismatch {
            harp,
            engine,
            asked,
        }) = refusal.object()
        {
            return Ok(SessionRejection::EngineMismatch {
                harp,
                engine,
                asked,
            });
        }
        Err(NotOurs)
    }
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
