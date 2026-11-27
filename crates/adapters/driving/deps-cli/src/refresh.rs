//! A pull's skipped project refresh, said in words (the text of `deps pull`).

use std::io::{self, Write};

use library_api::deps_wire::refresh::Skipped;

/// The note: the directory nothing was written into, per engine the kinds it
/// would have written or released, each kind it would have been refused with
/// the reason and its fix, and the two ways to apply it: `rerun`, the
/// command's own `--yes` form, and the `ctxloom materialize` limited by
/// `--surface` to the kinds the refresh touches. With only refusals there is
/// nothing to apply, so no apply line.
pub fn text(out: &mut dyn Write, skipped: &Skipped, rerun: &str) -> io::Result<()> {
    writeln!(
        out,
        "  Project refresh skipped: without --yes nothing was written into {}.",
        skipped.target
    )?;
    let mut kinds: Vec<&str> = Vec::new();
    for engine in &skipped.engines {
        let mut parts = Vec::new();
        if !engine.wrote.is_empty() {
            parts.push(format!("would write {}", engine.wrote.join(", ")));
        }
        if !engine.released.is_empty() {
            parts.push(format!("would release {}", engine.released.join(", ")));
        }
        if !parts.is_empty() {
            writeln!(out, "    {}: {}", engine.engine, parts.join("; "))?;
        }
        for refused in &engine.refused {
            writeln!(
                out,
                "    {}: {} refused: {}",
                engine.engine, refused.kind, refused.reason
            )?;
            writeln!(out, "      fix: {}", refused.fix)?;
        }
        for kind in engine.wrote.iter().chain(&engine.released) {
            if !kinds.contains(&kind.as_str()) {
                kinds.push(kind);
            }
        }
    }
    if kinds.is_empty() {
        return Ok(());
    }
    let surfaces: String = kinds.iter().map(|k| format!(" --surface {k}")).collect();
    writeln!(
        out,
        "    Apply it with '{rerun}', or 'ctxloom materialize --yes{surfaces}'."
    )
}

#[cfg(test)]
#[path = "refresh.test.rs"]
mod tests;
