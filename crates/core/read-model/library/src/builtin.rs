//! The bundles ctxloom ships built in (owner rulings 110, 111): the default
//! content a new project's seeded profile inherits, compiled into the binary
//! so a fresh project has it with nothing pulled, nothing written under
//! `.ctxloom/cache`, and no network.
//!
//! They are the catalog's BUILT-IN origin, [`ORIGIN`], beside the project's
//! own bundles and those installed from remotes. Each keeps the name it has
//! in the repository it was taken from (`ai-developer`, `testing`, ...), so
//! the shipped profiles read as written; a project bundle of the same name
//! overrides it whole (its profiles with it), and the built-in one is then
//! not in the catalog at all. They are read-only: no write targets them.
//!
//! The files are `resources/builtin/<bundle>/`, byte for byte the
//! repository's at [`SOURCE_COMMIT`] (`resources/builtin/README.md` says how
//! to refresh them, and the licence they ship under is beside them).

use std::collections::BTreeMap;

use crate::catalog::Entry;
use crate::tree::{self, Files};

/// The name the built-in origin is shown under.
pub const ORIGIN: &str = "ctxloom/default";

/// The repository the built-in bundles were taken from.
pub const SOURCE_REPOSITORY: &str = "https://github.com/ctxloom/ctxloom-default";

/// The commit of [`SOURCE_REPOSITORY`] they are a copy of.
pub const SOURCE_COMMIT: &str = "6129ccb9b173f957d05ceb956aa078462f86e697";

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
pub const FILES: [Shipped; 4] = [
    bundle!("ai-coding-general":
        "bundle.yaml",
        "fragments/concurrency-warnings.distilled.md",
        "fragments/concurrency-warnings.md",
        "fragments/context-engineering.distilled.md",
        "fragments/context-engineering.md",
        "fragments/human-review-checklist.distilled.md",
        "fragments/human-review-checklist.md",
        "fragments/is-this-a-standard.distilled.md",
        "fragments/is-this-a-standard.md",
        "fragments/mutation-as-test-validator.distilled.md",
        "fragments/mutation-as-test-validator.md",
        "fragments/no-provenance-comments.distilled.md",
        "fragments/no-provenance-comments.md",
        "fragments/tdd-for-llm.distilled.md",
        "fragments/tdd-for-llm.md",
        "fragments/version-source-of-truth.distilled.md",
        "fragments/version-source-of-truth.md",
        "fragments/warning-suppression.distilled.md",
        "fragments/warning-suppression.md",
    ),
    bundle!("ai-developer":
        "bundle.yaml",
        "profiles/developer.yaml",
    ),
    bundle!("git":
        "bundle.yaml",
        "fragments/git.distilled.md",
        "fragments/git.md",
    ),
    bundle!("testing":
        "bundle.yaml",
        "fragments/mutation-testing.distilled.md",
        "fragments/mutation-testing.md",
        "fragments/tdd.distilled.md",
        "fragments/tdd.md",
        "fragments/test-coverage.distilled.md",
        "fragments/test-coverage.md",
    ),
];

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
/// name is not in `taken`, read, by name. A built-in bundle reads (its test
/// holds it), so none is left out for a fault.
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

/// The profiles the built-in bundles not in `taken` ship, by the key a
/// profile of a bundle of that name has (`ctxloom+local:<bundle>#profiles/<name>`),
/// each named by its key.
pub fn profiles(taken: &dyn Fn(&str) -> bool) -> BTreeMap<String, crate::profile::Profile> {
    entries(taken)
        .into_iter()
        .flat_map(|entry| {
            entry.bundle.profiles.into_iter().map(move |(name, body)| {
                let key = format!("ctxloom+local:{}#profiles/{name}", entry.name);
                let mut body = body;
                body.name.clone_from(&key);
                (key, body)
            })
        })
        .collect()
}

#[cfg(test)]
#[path = "builtin.test.rs"]
mod tests;
