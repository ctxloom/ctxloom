//! `search` (B3): the daemon's `SearchLibrary` of `projects/{project}`
//! (OD-40, design §5.9; step 10a U2), its text drawn from the message (D10).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // The kind is the API's enum: a word that names none is refused here,
    // as the use case refuses it, since the request cannot carry it.
    let word = text_arg(sub, "type");
    let (kind, _) = library_api::resources::item_kind(&word);
    if !word.is_empty() && kind == v1::ItemKind::Unspecified {
        return Err(AppError::SearchType { value: word }.into_failure());
    }
    let request = v1::SearchLibraryRequest {
        query: text_arg(sub, "query"),
        tags: many(sub, "tag"),
        kind: kind as i32,
        local: flag(sub, "local"),
        remote: flag(sub, "remote"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::SearchLibraryRequest::default()
    };
    let read = cx.served.search_library("", request);
    served(cx, read, out, err, false, |found, out, _| {
        text::search(out, &crate::read_back::search(found))
    })
}

#[cfg(test)]
#[path = "search.test.rs"]
mod tests;
