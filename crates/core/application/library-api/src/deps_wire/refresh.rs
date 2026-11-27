//! The project refresh a pull previewed and did not write for want of
//! `--yes`: said in words, or carried in the pull's json as its
//! `skipped_refresh` (OD-40, B4: `ProjectRefresh`).

use daemon_proto::v1;
use deps_application::Refresh;
use presentation::Catalog;

use cli_support::FixOf;

/// The refresh a pull skipped, its refusals in words.
pub struct Skipped {
    pub target: String,
    pub engines: Vec<Engine>,
}

pub struct Engine {
    pub engine: String,
    pub wrote: Vec<String>,
    pub released: Vec<String>,
    pub refused: Vec<Refused>,
}

pub struct Refused {
    pub kind: String,
    pub reason: String,
    pub fix: String,
}

/// `refresh`, its refusals said from the catalog.
pub fn view(refresh: &Refresh, catalog: &Catalog) -> Skipped {
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

/// The refresh a pull's answer carries (step 10c U4), read back: each
/// engine by its ID, each refusal in the words it was said in.
pub fn from_message(message: &v1::ProjectRefresh) -> Skipped {
    Skipped {
        target: message.target.clone(),
        engines: message
            .engines
            .iter()
            .map(|e| Engine {
                engine: e
                    .engine
                    .rsplit_once("/engines/")
                    .map_or(e.engine.as_str(), |(_, id)| id)
                    .to_string(),
                wrote: e.written_kinds.clone(),
                released: e.released_kinds.clone(),
                refused: e
                    .refused_kinds
                    .iter()
                    .map(|r| Refused {
                        kind: r.kind.clone(),
                        reason: r.reason.clone(),
                        fix: r.fix.clone(),
                    })
                    .collect(),
            })
            .collect(),
    }
}

/// Whether applying it would write or release anything (with only
/// refusals there is nothing to apply).
pub fn applies(skipped: &Skipped) -> bool {
    skipped
        .engines
        .iter()
        .any(|e| !e.wrote.is_empty() || !e.released.is_empty())
}

/// The refresh as its API message, `ProjectRefresh`, each engine by name.
pub fn message(parent: &str, skipped: &Skipped) -> v1::ProjectRefresh {
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

#[cfg(test)]
#[path = "refresh.test.rs"]
mod tests;
