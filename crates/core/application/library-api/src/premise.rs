//! The premise index as a driving edge presents it (E2, owner 2026-11-17:
//! "The text of how to view is a UI layer change. It should happen at
//! presentation, not deeper."). `AssembleContext` answers what a premise
//! held back as data (`AssembledContext.premised`); the edge that shows the
//! context renders the index after it, in the words of where it is shown:
//! the CLI's `context assemble` at rest, the MCP server's
//! `context_assemble` tool on a launch. The words are the library's own
//! (`library::premise`), unchanged. One renderer, library-api's (step 10a
//! U2): the CLI's and the MCP server's alike.

use daemon_proto::v1;
use library_application::premise::{render_on, Entry, Surface, SECTION_SEPARATOR};

/// Where an assembled context is shown, which words its index's last
/// clause (s3-Q5).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Shown {
    /// At rest: the agent runs `ctxloom fragment show`.
    AtRest,
    /// On a launch, where ctxloom's MCP server is offered.
    Launch,
}

/// `assembled`'s context, ended with the premise index of what it held
/// back, worded for `shown`, as the assembly once rendered it in the core:
/// after a section separator, nothing for nothing held back.
pub fn render_premise_index(assembled: &mut v1::AssembledContext, shown: Shown) {
    let entries: Vec<Entry> = assembled
        .premised
        .iter()
        .map(|entry| Entry {
            name: entry.fragment.clone(),
            premise: entry.premise.clone(),
        })
        .collect();
    let surface = match shown {
        Shown::AtRest => Surface::AtRest,
        Shown::Launch => Surface::Launch,
    };
    let index = render_on(&entries, surface);
    if index.is_empty() {
        return;
    }
    if !assembled.context.is_empty() {
        assembled.context.push_str(SECTION_SEPARATOR);
    }
    assembled.context.push_str(&index);
}

#[cfg(test)]
#[path = "premise.test.rs"]
mod tests;
