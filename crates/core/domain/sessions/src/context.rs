//! What a library change owes a session (owner ruling 2026-11-27): the
//! session aggregate decides it on its own folded loadout and handed
//! positions (`SessionState.context`), from the change the daemon tells it
//! (`RecordLibraryChanged`). Pure: the same state and change always give the
//! same answer, and nothing here reads a file or a clock.

use std::collections::BTreeSet;

use angzarr_embedded::Timestamp;

use crate::{ContextHanded, ContextOwed, LoadoutRecorded, RecordLibraryChanged, TreePosition};

/// Where a bundle's directory is in its tree.
const BUNDLES: &str = "content/bundles/v2/";

/// The config file's path in its tree, which every loadout draws on.
const CONFIG: &str = "config.yaml";

/// What [`touched`] names a config change by.
pub const CONFIG_CHANGED: &str = "config";

/// What of a loadout drawing on `bundles` a change of `paths` (relative to
/// its tree) touches: each bundle it names that a path is in, and
/// [`CONFIG_CHANGED`] for the tree's config; sorted, each once. Empty: the
/// change is none of its.
pub fn touched(bundles: &[String], paths: &[String]) -> Vec<String> {
    let mut named = BTreeSet::new();
    for path in paths {
        if path == CONFIG {
            named.insert(CONFIG_CHANGED.to_string());
            continue;
        }
        let Some(bundle) = path
            .strip_prefix(BUNDLES)
            .and_then(|rest| rest.split('/').next())
        else {
            continue;
        };
        if bundles.iter().any(|named| named == bundle) {
            named.insert(bundle.to_string());
        }
    }
    named.into_iter().collect()
}

/// Of `paths`, those a loadout drawing on `bundles` draws on, sorted, each
/// once.
pub fn drawn_on(bundles: &[String], paths: &[String]) -> Vec<String> {
    let kept: BTreeSet<&String> = paths
        .iter()
        .filter(|path| !touched(bundles, std::slice::from_ref(*path)).is_empty())
        .collect();
    kept.into_iter().cloned().collect()
}

/// Whether a change at `sequence`, recorded at `recorded`, is one a context
/// that covers `covered` of its tree and was loaded at `loaded_at` has not
/// been given: past its covered position, or, covering none there, recorded
/// after its loadout.
pub fn owes(
    covered: Option<u64>,
    loaded_at: Timestamp,
    sequence: u64,
    recorded: Timestamp,
) -> bool {
    match covered {
        Some(covered) => sequence > covered,
        None => (recorded.seconds, recorded.nanos) > (loaded_at.seconds, loaded_at.nanos),
    }
}

impl ContextHanded {
    /// The position this context covers of `tree`, if it covers one.
    pub fn covered_of(&self, tree: &str) -> Option<u64> {
        self.covered
            .iter()
            .find(|position| position.tree == tree)
            .map(|position| position.sequence)
    }

    /// Whether a change of `tree` at `sequence`, recorded at `recorded`, is
    /// one this context has not been given ([`owes`]).
    pub fn owes(&self, tree: &str, sequence: u64, recorded: Timestamp) -> bool {
        owes(self.covered_of(tree), self.loaded_at, sequence, recorded)
    }

    /// `positions` covered: each one's tree moved up to it, never back.
    pub(crate) fn cover(&mut self, positions: &[TreePosition]) {
        for position in positions {
            match self
                .covered
                .iter_mut()
                .find(|held| held.tree == position.tree)
            {
                Some(held) => held.sequence = held.sequence.max(position.sequence),
                None => self.covered.push(position.clone()),
            }
        }
        self.covered.sort_by(|a, b| a.tree.cmp(&b.tree));
    }
}

/// The context a freshly recorded loadout hands, loaded at `recorded_at`.
pub(crate) fn handed_by(loadout: &LoadoutRecorded, recorded_at: Timestamp) -> ContextHanded {
    let mut handed = ContextHanded {
        bundles: loadout.loadout.bundles.clone(),
        loaded_at: recorded_at,
        covered: Vec::new(),
    };
    handed.cover(&loadout.covered);
    handed
}

/// What `change` owes a session whose context is `context`: the change as an
/// owed event when it is past what the context covers and touches its
/// loadout; none otherwise (no loadout recorded, covered, or not drawn on).
pub(crate) fn owed_by(
    context: Option<&ContextHanded>,
    change: &RecordLibraryChanged,
) -> Option<ContextOwed> {
    let context = context?;
    let recorded = change.recorded;
    if !context.owes(&change.tree, change.sequence, recorded) {
        return None;
    }
    let bundles = touched(&context.bundles, &change.paths);
    if bundles.is_empty() {
        return None;
    }
    Some(ContextOwed {
        tree: change.tree.clone(),
        sequence: change.sequence,
        bundles,
        paths: drawn_on(&context.bundles, &change.paths),
    })
}

#[cfg(test)]
#[path = "context.test.rs"]
mod tests;
