//! `context assemble` (B3): its json the `AssembledContext` of
//! `AssembleContext` (OD-40, design §5.9).

use super::search::count;
use crate::catalog;
use crate::prelude::*;
use library_application::{Assembled, Located};

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
    resource(cx, outcome, out, err, assembled, |out, assembled| {
        writeln!(out, "{}", assembled.context)
    })
}

/// `context assemble`: `AssembledContext`, its profiles and fragments named
/// under `parent`; one the catalog does not hold keeps the text it was asked
/// by, as a fragment asked for and not found does (OQ-16). Its notices are
/// said on stderr, as every notice is before T13.
pub(crate) fn assembled(parent: &str, assembled: &Assembled) -> v1::AssembledContext {
    let named = |text: &String, located: Option<&Option<Located>>, collection: &str| {
        located.and_then(Option::as_ref).map_or_else(
            || text.clone(),
            |at| catalog::item_name(parent, &at.bundle, collection, &at.item),
        )
    };
    v1::AssembledContext {
        context: assembled.context.clone(),
        tokens: count(assembled.tokens),
        fragments: assembled
            .fragments_loaded
            .iter()
            .enumerate()
            .map(|(at, reference)| {
                let used = assembled.used.get(at);
                v1::AssembledFragment {
                    fragment: named(reference, used.map(|u| &u.located), "fragments"),
                    form: if used.is_some_and(|u| u.distilled) {
                        v1::assembled_fragment::Form::Distilled
                    } else {
                        v1::assembled_fragment::Form::Original
                    } as i32,
                }
            })
            .collect(),
        profiles: assembled
            .profiles
            .iter()
            .enumerate()
            .map(|(at, ask)| named(ask, assembled.profiles_located.get(at), "profiles"))
            .collect(),
        missing_fragments: assembled.missing_fragments.clone(),
        missing_tags: assembled.missing_tags.clone(),
        notices: Vec::new(),
    }
}

#[cfg(test)]
#[path = "context.test.rs"]
mod tests;
