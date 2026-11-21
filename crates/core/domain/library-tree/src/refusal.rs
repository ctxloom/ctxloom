//! A rejection as a typed refusal, and back.
//!
//! The rules raise a [`LibraryTreeRejection`]: a stable code, args and
//! English, which the edge prints. On the wire a command refuses with a
//! typed refusal MESSAGE of the protos (`proto/ctxloom/library/v1`), one
//! per rejection, listed in the `refuses` of the commands that raise it.
//! The `refuse!` macro turns the one into the other for a handler, and
//! `TryFrom<&TypedRefusal>` turns it back for the application, keyed by
//! the refusal's TYPE and by nothing else: the status a refusal travels
//! under is its message's `(refusal_status)` option, read here from the
//! generated constant, and never decides what a person is told.

use angzarr_embedded::markup::TypedRefusal;

use crate::{
    BuiltinReadOnly, LibraryTreeRejection, NoSuchResource, NoSuchSlot, OneChangePerCall,
    PlanRejected, PlanStale, ResourceExists, SlotNotDue,
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

/// A typed refusal that is none of this domain's.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct NotOurs;

/// The rejection a library command's typed refusal is, by its type.
impl TryFrom<&TypedRefusal> for LibraryTreeRejection {
    type Error = NotOurs;

    fn try_from(refusal: &TypedRefusal) -> Result<Self, NotOurs> {
        if let Some(PlanStale { path, seen, now }) = refusal.object() {
            return Ok(LibraryTreeRejection::PlanStale { path, seen, now });
        }
        if let Some(ResourceExists { path }) = refusal.object() {
            return Ok(LibraryTreeRejection::ResourceExists { path });
        }
        if let Some(NoSuchResource { path }) = refusal.object() {
            return Ok(LibraryTreeRejection::NoSuchResource { path });
        }
        if let Some(BuiltinReadOnly { bundle }) = refusal.object() {
            return Ok(LibraryTreeRejection::BuiltinReadOnly { bundle });
        }
        if let Some(PlanRejected { code, args }) = refusal.object() {
            return Ok(LibraryTreeRejection::PlanRejected { code, args });
        }
        if let Some(OneChangePerCall { paths }) = refusal.object() {
            return Ok(LibraryTreeRejection::OneChangePerCall { paths });
        }
        if let Some(NoSuchSlot { request_id }) = refusal.object() {
            return Ok(LibraryTreeRejection::NoSuchSlot { request_id });
        }
        if let Some(SlotNotDue {
            request_id,
            age_days,
        }) = refusal.object()
        {
            return Ok(LibraryTreeRejection::SlotNotDue {
                request_id,
                age_days,
            });
        }
        Err(NotOurs)
    }
}

#[cfg(test)]
#[path = "refusal.test.rs"]
mod tests;
