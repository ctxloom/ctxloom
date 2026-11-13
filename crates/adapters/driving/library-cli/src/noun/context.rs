//! `context assemble` (B3).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = AssembleRequest {
        profiles: many(sub, "profile"),
        fragments: many(sub, "fragment"),
        tags: many(sub, "tag"),
    };
    let outcome = cx.library.assemble(&cx.flags, &request);
    answer(cx, outcome, out, err, |assembled, out, _| {
        emit(cx.format, out, &assembled, |out| {
            writeln!(out, "{}", assembled.context)
        })
    })
}

#[cfg(test)]
#[path = "context.test.rs"]
mod tests;
