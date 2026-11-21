//! The premise index (D24): the menu of conditional guidance an agent selects
//! from. Under `config.premised: on-demand` (the default) a fragment with a
//! `premise` is not loaded at launch; the index, which ends the assembled
//! context (`compose::assemble`), names it and the premise, and the agent asks
//! for those that apply to what it is about to do. The wording is the owner's,
//! measured, and copied byte for byte from Go ctxloom's
//! `internal/adapters/operations/premise.go` at release/0.7 (860336c4e, which
//! is ca14d6046 or later), but for its last clause.
//!
//! Go's instruction ended "call the ctxloom `assemble_context` tool with the
//! names you chose (the `bundles` argument)". Here the last clause follows
//! the surface (s3-Q5, owner-approved 2026-11-14): context that rides a
//! launch, where ctxloom's own MCP server is offered, names its
//! `context_assemble` tool and its `bundles` argument; context at rest (no
//! server) says "run `ctxloom fragment show <name>` for each name you
//! chose" (owner ruling of 2026-11-12). Every other word is Go's. A name the
//! index gives is one `fragment show` shows (a companion's too), and one
//! `context_assemble` loads.

use crate::catalog::Catalog;

/// One row of the index: the qualified reference a selection quotes back, and
/// the premise that decides whether it applies.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Entry {
    pub name: String,
    pub premise: String,
}

/// Where an index's context goes (s3-Q5): its instruction's last clause
/// says how to load what was chosen, and that depends on what is offered
/// there.
#[derive(Clone, Copy, Default, PartialEq, Eq, Debug)]
pub enum Surface {
    /// Context at rest (a materialized file, `context assemble`, `fragment
    /// premises`): no server is offered, so the agent runs the CLI.
    #[default]
    AtRest,
    /// Context that rides a launch, where ctxloom's own MCP server is
    /// offered: the agent calls its `context_assemble` tool.
    Launch,
}

/// The guidance an agent selects by, at rest.
pub fn selection_instruction() -> String {
    selection_instruction_on(Surface::AtRest)
}

/// The guidance an agent selects by, worded for `surface`.
pub fn selection_instruction_on(surface: Surface) -> String {
    let clause = match surface {
        Surface::AtRest => AT_REST,
        Surface::Launch => ON_LAUNCH,
    };
    format!("{PREAMBLE}{clause}")
}

/// `PremiseSelectionInstruction()` of Go ctxloom, byte for byte, but for its
/// last clause, which follows the surface (s3-Q5).
const PREAMBLE: &str = "# Conditional guidance\n\nSome of this project's guidance applies CONDITIONALLY. Each entry gives a\nfragment's NAME and the PREMISE under which it applies. Load any your\ncontext does not already carry.\n\nMatch against WHAT YOU ARE ABOUT TO DO -- the next action, not the whole\nconversation behind you. Take each premise ON ITS OWN and ask whether it\ndescribes that action; you are not picking the single best match, and\nseveral premises often apply at once.\n\nWhen it is a BORDERLINE call, include it. An unnecessary fragment costs a\nlittle context; one you withhold is never learned to exist and cannot be\nasked for.\n\nSelect on the premise, not on the name. ";

/// The last clause at rest (owner ruling of 2026-11-12: the CLI).
const AT_REST: &str = "Then run\n`ctxloom fragment show <name>` for each name you chose and follow what it\nreturns.\n";

/// The last clause on a launch (s3-Q5, owner-approved 2026-11-14).
const ON_LAUNCH: &str = "Then for each name you chose, call\nthe ctxloom `context_assemble` tool with those names as its `bundles`\nargument.\n";

/// The index an agent selects from: the instruction, then one line per entry
/// (`- <name>: <premise>`). Empty for no entries: an empty render keeps a
/// premise-free corpus byte-identical to what it assembled before.
pub fn render(entries: &[Entry]) -> String {
    render_on(entries, Surface::AtRest)
}

/// [`render`], its instruction worded for `surface`.
pub fn render_on(entries: &[Entry], surface: Surface) -> String {
    if entries.is_empty() {
        return String::new();
    }
    let mut out = selection_instruction_on(surface);
    for entry in entries {
        out.push_str(&format!("\n- {}: {}", entry.name, entry.premise));
    }
    out
}

/// Every fragment of the catalog that carries a premise, by qualified name
/// (`bundle#fragments/name`), sorted by name so the same corpus always renders
/// the same index.
pub fn index(catalog: &Catalog) -> Vec<Entry> {
    let mut entries: Vec<Entry> = catalog
        .list_fragments()
        .into_iter()
        .filter(|info| !info.premise.is_empty())
        .map(|info| Entry {
            name: format!("{}#fragments/{}", info.bundle, info.name),
            premise: info.premise,
        })
        .collect();
    entries.sort_by(|a, b| a.name.cmp(&b.name));
    entries
}

/// What to say when no fragment carries a premise.
pub const NONE: &str =
    "No fragments carry a premise: every fragment in this project's corpus is loaded unconditionally.";

#[cfg(test)]
#[path = "premise.test.rs"]
mod tests;
