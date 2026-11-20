//! The premise index (D24): the menu of conditional guidance an agent selects
//! from. Under `config.premised: on-demand` (the default) a fragment with a
//! `premise` is not loaded at launch; the index, which ends the assembled
//! context (`compose::assemble`), names it and the premise, and the agent asks
//! for those that apply to what it is about to do. The wording is the owner's,
//! measured, and copied byte for byte from Go ctxloom's
//! `internal/adapters/operations/premise.go` at release/0.7 (860336c4e, which
//! is ca14d6046 or later), but for its last clause.
//!
//! NOTE for the owner (owner ruling of 2026-11-12, the wording pending the
//! owner's approval): Go's instruction ended "call the ctxloom
//! `assemble_context` tool with the names you chose (the `bundles`
//! argument)", an MCP tool not built here. Until T20 builds it, the clause
//! points at the CLI instead: "run `ctxloom fragment show <name>` for each
//! name you chose"; every other word is Go's. A name the index gives is one
//! `fragment show` shows (a companion's too).

use crate::catalog::Catalog;

/// One row of the index: the qualified reference a selection quotes back, and
/// the premise that decides whether it applies.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Entry {
    pub name: String,
    pub premise: String,
}

/// The guidance an agent selects by, served the same on every surface.
pub fn selection_instruction() -> String {
    INSTRUCTION.to_string()
}

/// `PremiseSelectionInstruction()` of Go ctxloom, byte for byte, but for its
/// last clause (owner ruling of 2026-11-12: the CLI, not the MCP tool).
const INSTRUCTION: &str = "# Conditional guidance\n\nSome of this project's guidance applies CONDITIONALLY. Each entry gives a\nfragment's NAME and the PREMISE under which it applies. Load any your\ncontext does not already carry.\n\nMatch against WHAT YOU ARE ABOUT TO DO -- the next action, not the whole\nconversation behind you. Take each premise ON ITS OWN and ask whether it\ndescribes that action; you are not picking the single best match, and\nseveral premises often apply at once.\n\nWhen it is a BORDERLINE call, include it. An unnecessary fragment costs a\nlittle context; one you withhold is never learned to exist and cannot be\nasked for.\n\nSelect on the premise, not on the name. Then run\n`ctxloom fragment show <name>` for each name you chose and follow what it\nreturns.\n";

/// The index an agent selects from: the instruction, then one line per entry
/// (`- <name>: <premise>`). Empty for no entries: an empty render keeps a
/// premise-free corpus byte-identical to what it assembled before.
pub fn render(entries: &[Entry]) -> String {
    if entries.is_empty() {
        return String::new();
    }
    let mut out = selection_instruction();
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
