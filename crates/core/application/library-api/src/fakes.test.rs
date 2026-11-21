//! A library tree's files as the scanner finds them, from a table, counting
//! what it is asked.

use std::collections::BTreeMap;
use std::sync::Mutex;

/// The files on disk, by content id, and every call asked so far.
#[derive(Default)]
pub(crate) struct Tree {
    pub(crate) files: BTreeMap<String, String>,
    pub(crate) asked: Mutex<Vec<String>>,
}

impl Tree {
    /// The file `path` on disk, its id `oid`.
    pub(crate) fn file(mut self, path: &str, oid: &str) -> Tree {
        self.files.insert(path.to_string(), oid.to_string());
        self
    }
}

/// The scanner over the table (step 10c U13 rework): every file on disk,
/// by its id.
impl ctxloom_ports::LibraryScanner for Tree {
    fn scan(&self, _: &str) -> Result<BTreeMap<String, String>, ctxloom_ports::Failure> {
        self.asked.lock().unwrap().push("scan".to_string());
        Ok(self.files.clone())
    }

    fn content_ids(
        &self,
        _: &str,
        paths: &[&str],
    ) -> Result<BTreeMap<String, String>, ctxloom_ports::Failure> {
        Ok(paths
            .iter()
            .filter_map(|path| Some((path.to_string(), self.files.get(*path)?.clone())))
            .collect())
    }
}
