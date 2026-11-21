//! `engine`: list and show, each the daemon's read (step 10a U2) of its API
//! resource (OD-40), its text drawn from the message (D10).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let request = v1::GetEngineRequest {
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::GetEngineRequest::default()
        };
        let under = library_api::names::named_target("engines", &text_arg(sub, "engine"));
        let read = cx.served.get_engine(&under, request);
        return served(cx, read, out, err, false, |engine, out, _| {
            text::engine_show(out, &crate::read_back::engine_row(engine))
        });
    }
    let request = v1::ListEnginesRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListEnginesRequest::default()
    };
    let read = cx.served.list_engines("", request);
    served(cx, read, out, err, false, |listed, out, _| {
        text::engine_list(out, &crate::read_back::engine_rows(listed))
    })
}

#[cfg(test)]
#[path = "engine.test.rs"]
mod tests;
