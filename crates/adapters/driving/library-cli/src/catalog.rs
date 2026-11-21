//! The catalog's resource names (OD-40, B1; design §1.2 and §3.2 to §3.6):
//! a bundle's name from its catalog name, with its origin in the pattern
//! (OQ-4), an item's below its bundle's, a configured reference resolved to
//! a name where its text says which (a reference it cannot place keeps its
//! text, OQ-16), and the write path every catalog write answers through.
//!
//! An ID on disk that breaks the item grammar (OQ-6) is named as it is: the
//! grammar is enforced where a write mints a name, and a name already on
//! disk still reads.

use std::io::{self, Write};

use cli_support::{say_notices, Exit};
use daemon_proto::v1;
use library_application::{Notice, Outcome};
use serde::Serialize;

use crate::{failed, output_failure, Context, Failure, PROG};

pub(crate) use library_api::names::{profile_ref, reference_name};

/// A distilled form there or not.
pub(crate) fn distilled(there: bool) -> i32 {
    if there {
        v1::DistilledState::Fresh as i32
    } else {
        v1::DistilledState::None as i32
    }
}

#[cfg(test)]
pub(crate) use library_api::resources::shown_distilled;

/// A catalog answer: the notices, said; then its error, or its json (the
/// view as its API message, named under the parent the names give, asked
/// only here), or its text with the notes it adds. `explicit_json` keeps a
/// verb that answered in text only answering in text unless json is asked
/// for by name. The exit is `exit`'s.
#[allow(clippy::too_many_arguments)]
pub(crate) fn answer<T, M: Serialize>(
    cx: &Context<'_>,
    outcome: Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    explicit_json: bool,
    message: impl FnOnce(&str, &T) -> M,
    text: impl FnOnce(&T, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
    exit: impl FnOnce(&T) -> Exit,
) -> Result<Exit, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let value = outcome.result.map_err(failed)?;
    let json = cx.format == angzarr_presentation::Format::Json && (cx.explicit || !explicit_json);
    let mut notes = Vec::new();
    if json {
        let parent = cx.names.parent()?;
        crate::api::print(out, &message(&parent, &value)).map_err(output_failure)?;
    } else {
        text(&value, out, &mut notes).map_err(output_failure)?;
    }
    let said: Vec<Notice> = notes.into_iter().map(Notice::text).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(exit(&value))
}

/// A read or a write that answers ok.
pub(crate) fn ok<T>(_: &T) -> Exit {
    Exit::OK
}

#[cfg(test)]
#[path = "catalog.test.rs"]
mod tests;
