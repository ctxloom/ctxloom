//! What the daemon's scanner finds of a library tree (step 10c U13 rework,
//! owner 2026-11-18: "walk the directory structure, push facts including
//! the hashes of found files"). The library is git-free: its content ids
//! are the SHA-256 of each file's bytes (`library_tree::content_id`), and
//! no git is asked to see a tree.

use std::collections::BTreeMap;

use crate::Failure;

/// The library's paths in a tree: ctxloom's own content and its three
/// files. Nothing under `sessions/`, `state/` or `cache/` is the library.
pub const LIBRARY_PATHS: [&str; 4] = [
    "content/bundles/v2",
    "config.yaml",
    "remotes.yaml",
    "lock.yaml",
];

/// A library tree's files as they are now.
pub trait LibraryScanner: Send + Sync {
    /// Every file of the library ([`LIBRARY_PATHS`]) under the tree `dir`,
    /// by its path relative to it (`/` separators), with its content id. A
    /// file whose stat is unchanged since it was last hashed may keep that
    /// hash (the edge's memo).
    fn scan(&self, dir: &str) -> Result<BTreeMap<String, String>, Failure>;

    /// The content id of each of `paths` (relative to `dir`) as it is now;
    /// a path with no file is left out: absent.
    fn content_ids(&self, dir: &str, paths: &[&str]) -> Result<BTreeMap<String, String>, Failure>;

    /// The bytes of each of `paths` (relative to `dir`) as they are now,
    /// each only when its content id is still the one `paths` names (a file
    /// that changed between the scan and this read is left out: its next
    /// scan lists it again). The default reads nothing.
    fn read_files(
        &self,
        dir: &str,
        paths: &[(&str, &str)],
    ) -> Result<BTreeMap<String, Vec<u8>>, Failure> {
        let _ = (dir, paths);
        Ok(BTreeMap::new())
    }

    /// Every memo dropped (`ctxloom context refresh`): the next scan hashes
    /// every file again.
    fn forget(&self) {}
}
