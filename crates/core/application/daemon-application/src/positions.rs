//! Where the library trees stand, for the daemon's loop: the positions a
//! loadout is recorded against.

/// Where each tree `checkouts` holds that `wanted` names is: its latest
/// sequence that changed a path (its baseline's when none did since),
/// sorted by tree.
pub fn positions(
    checkouts: &library_checkout::LibraryCheckout,
    wanted: impl Fn(&str) -> bool,
) -> Vec<sessions::TreePosition> {
    let mut positions: Vec<sessions::TreePosition> = checkouts
        .trees()
        .filter(|tree| wanted(&tree.name))
        .map(|tree| sessions::TreePosition {
            tree: tree.name.clone(),
            sequence: tree
                .resources
                .values()
                .map(|resource| resource.sequence)
                .chain([tree.baseline_sequence])
                .max()
                .unwrap_or_default(),
        })
        .collect();
    positions.sort_by(|a, b| a.tree.cmp(&b.tree));
    positions
}

#[cfg(test)]
#[path = "positions.test.rs"]
mod tests;
