//! `context assemble` (B3): the daemon's `AssembleContext` (step 10a U2),
//! its json the `AssembledContext` (OD-40, design §5.9), its premise index
//! rendered here, at rest (E2: presentation's), into its context.

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = v1::AssembleContextRequest {
        profiles: many(sub, "profile"),
        fragments: many(sub, "fragment"),
        tags: many(sub, "tag"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::AssembleContextRequest::default()
    };
    let read = cx.served.assemble_context(request).map(at_rest);
    served(cx, read, out, err, false, |assembled, out, _| {
        writeln!(out, "{}", assembled.context)
    })
}

/// What `context refresh` says once the daemon observed afresh (a DRAFT,
/// step 10c U13 rework).
pub(crate) const REFRESHED: &str = "the library was observed afresh: what changed is recorded";

/// `context refresh` (step 10c U13 rework, owner 2026-11-18): the
/// daemon's `AssembleContext` asked to refresh, so it forgets what it has
/// seen of the trees and observes them now; what differs is recorded as any
/// read's observation, and reaches a live session through what is owed. The
/// assembly itself is not shown.
pub(crate) fn refresh(
    cx: &Context<'_>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = v1::AssembleContextRequest {
        refresh: true,
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::AssembleContextRequest::default()
    };
    let read = cx
        .served
        .assemble_context(request)
        .map(|assembled| ((), assembled.notices));
    served(cx, read, out, err, false, |_, out, _| {
        writeln!(out, "{REFRESHED}")
    })
}

/// An assembly as `context assemble` shows it: its premise index rendered
/// after its context, at rest (`ctxloom fragment show`); its notices said
/// as every read's are, and kept in the message too, so its json carries
/// them (owner ruling 2026-11-18, night: a stale distilled form's notice).
pub(crate) fn at_rest(
    mut assembled: v1::AssembledContext,
) -> (v1::AssembledContext, Vec<v1::Notice>) {
    library_api::premise::render_premise_index(&mut assembled, library_api::premise::Shown::AtRest);
    assembled.premised.clear();
    let notices = assembled.notices.clone();
    (assembled, notices)
}

#[cfg(test)]
#[path = "context.test.rs"]
mod tests;
