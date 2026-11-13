//! `search` (B3).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = SearchRequest {
        query: text_arg(sub, "query"),
        tags: many(sub, "tag"),
        kind: text_arg(sub, "type"),
        local: flag(sub, "local"),
        remote: flag(sub, "remote"),
    };
    let outcome = cx.library.search(&cx.flags, &request);
    answer(cx, outcome, out, err, |results, out, _| {
        emit(cx.format, out, &results, |out| text::search(out, &results))
    })
}

#[cfg(test)]
#[path = "search.test.rs"]
mod tests;
