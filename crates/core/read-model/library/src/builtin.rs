//! The bundle ctxloom ships built in (owner rulings 110, 111, narrowed by
//! 117 and 123): ctxloom's OPERATIONAL prompts, compiled into the binary.
//!
//! It is [`BUNDLE`], under the catalog's BUILT-IN origin, [`ORIGIN`], beside
//! the project's own bundles and those installed from remotes. It holds two
//! prompts: [`SETUP_INTERVIEW`], the interview `ctxloom init` opens with,
//! and [`DISTILL_PROMPT`], for the distill activity. It is not generally
//! injected: it carries no fragment, profile or tag, so no profile inherits
//! from it, no premise index offers it and no tag selection reaches it. A
//! project bundle of the same name overrides it whole in the catalog, and the
//! built-in one is then not in the catalog at all. It is read-only: no write
//! targets it.
//!
//! The files are `resources/builtin/ctxloom/`; `resources/builtin/README.md`
//! says what each is.

use crate::catalog::Entry;
use crate::tree::{self, Files};

/// The name the built-in origin is shown under.
pub const ORIGIN: &str = "ctxloom/default";

/// The one built-in bundle's name.
pub const BUNDLE: &str = "ctxloom";

/// The setup interview, `ctxloom#prompts/setup-interview`: the owner's
/// words, used as they are.
pub const SETUP_INTERVIEW: &str =
    include_str!("../resources/builtin/ctxloom/prompts/setup-interview.md");

/// The distill prompt, `ctxloom#prompts/distill`: the owner's own text
/// (ruling 123).
pub const DISTILL_PROMPT: &str = include_str!("../resources/builtin/ctxloom/prompts/distill.md");

/// Where a built-in bundle's manifest is said to be: no file of the project's
/// or the home's, so nothing reads it as one.
pub fn manifest_path(bundle: &str) -> String {
    format!("<built-in {ORIGIN}>/{bundle}/bundle.yaml")
}

macro_rules! bundle {
    ($name:literal: $($path:literal),* $(,)?) => {
        (
            $name,
            &[$((
                $path,
                include_bytes!(concat!("../resources/builtin/", $name, "/", $path)).as_slice(),
            )),*],
        )
    };
}

/// A built-in bundle: its name, and its files, each by its path in it.
pub type Shipped = (&'static str, &'static [(&'static str, &'static [u8])]);

/// Every built-in bundle, by name.
pub const FILES: [Shipped; 1] = [bundle!("ctxloom":
    "bundle.yaml",
    "prompts/distill.md",
    "prompts/setup-interview.md",
)];

/// The names of the built-in bundles, by name.
pub fn names() -> Vec<&'static str> {
    FILES.iter().map(|(name, _)| *name).collect()
}

/// The files of the built-in bundle `name`; none for a name that is not one.
pub fn files(name: &str) -> Option<Files> {
    let (_, files) = FILES.iter().find(|(bundle, _)| *bundle == name)?;
    Some(
        files
            .iter()
            .map(|(path, bytes)| (path.to_string(), bytes.to_vec()))
            .collect(),
    )
}

/// The built-in bundles a project's own do not override: every one whose
/// name is not in `taken`, read, by name. The built-in bundle reads (its
/// test holds it), so it is not left out for a fault.
pub fn entries(taken: &dyn Fn(&str) -> bool) -> Vec<Entry> {
    FILES
        .iter()
        .filter(|(name, _)| !taken(name))
        .filter_map(|(name, _)| {
            let bundle = tree::read_tree(name, &files(name)?).ok()?;
            Some(Entry {
                name: name.to_string(),
                manifest_path: manifest_path(name),
                bundle,
            })
        })
        .collect()
}

#[cfg(test)]
#[path = "builtin.test.rs"]
mod tests;
