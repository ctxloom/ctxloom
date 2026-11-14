//! The project refresh a pull previewed and did not write for want of
//! `--yes`: said in words, or carried in the pull's json as its
//! `skipped_refresh` (OD-40, B4: `ProjectRefresh`).

use std::io::{self, Write};

use angzarr_presentation::Catalog;
use daemon_proto::v1;
use deps_application::Refresh;

use cli_support::FixOf;

/// The refresh a pull skipped, its refusals in words.
pub(crate) struct Skipped {
    target: String,
    engines: Vec<Engine>,
}

struct Engine {
    engine: String,
    wrote: Vec<String>,
    released: Vec<String>,
    refused: Vec<Refused>,
}

struct Refused {
    kind: String,
    reason: String,
    fix: String,
}

/// `refresh`, its refusals said from the catalog.
pub(crate) fn view(refresh: &Refresh, catalog: &Catalog) -> Skipped {
    Skipped {
        target: refresh.target.clone(),
        engines: refresh
            .engines
            .iter()
            .map(|e| Engine {
                engine: e.engine.clone(),
                wrote: e.wrote.clone(),
                released: e.released.clone(),
                refused: e
                    .refused
                    .iter()
                    .map(|r| Refused {
                        kind: r.kind.clone(),
                        reason: catalog.message(r.cause.as_ref()),
                        fix: catalog
                            .render(&FixOf(r.cause.as_ref(), format!("{}-fix", r.cause.code())))
                            .unwrap_or_default(),
                    })
                    .collect(),
            })
            .collect(),
    }
}

/// Whether applying it would write or release anything (with only
/// refusals there is nothing to apply).
pub(crate) fn applies(skipped: &Skipped) -> bool {
    skipped
        .engines
        .iter()
        .any(|e| !e.wrote.is_empty() || !e.released.is_empty())
}

/// The refresh as its API message, `ProjectRefresh`, each engine by name.
pub(crate) fn message(parent: &str, skipped: &Skipped) -> v1::ProjectRefresh {
    v1::ProjectRefresh {
        target: skipped.target.clone(),
        engines: skipped
            .engines
            .iter()
            .map(|e| v1::RefreshEngine {
                engine: format!("{parent}/engines/{}", e.engine),
                written_kinds: e.wrote.clone(),
                released_kinds: e.released.clone(),
                refused_kinds: e
                    .refused
                    .iter()
                    .map(|r| v1::RefusedKind {
                        kind: r.kind.clone(),
                        reason: r.reason.clone(),
                        fix: r.fix.clone(),
                    })
                    .collect(),
            })
            .collect(),
    }
}

/// The note: the directory nothing was written into, per engine the kinds it
/// would have written or released, each kind it would have been refused with
/// the reason and its fix, and the two ways to apply it: `rerun`, the
/// command's own `--yes` form, and the `ctxloom materialize` limited by
/// `--surface` to the kinds the refresh touches. With only refusals there is
/// nothing to apply, so no apply line.
pub(crate) fn text(out: &mut dyn Write, skipped: &Skipped, rerun: &str) -> io::Result<()> {
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
