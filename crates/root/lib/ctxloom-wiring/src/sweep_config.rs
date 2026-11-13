//! `session_purge_age` and `session_transcript_retention`, read for the
//! daemon's destroy from the layered configuration:
//! the files, then the environment's overrides, as every other command reads
//! it.

use std::sync::Arc;

use ctxloom_ports::{ConfigFiles, Failure};
use library_application::{load_config as load, Flags};
use reclaim_application::SweepConfig;

/// The configuration a sweep consults when `--purge-older-than` is not given.
pub struct SweepSettings {
    files: Arc<dyn ConfigFiles + Send + Sync>,
    environ: Vec<(String, String)>,
}

impl SweepSettings {
    pub fn new(
        files: Arc<dyn ConfigFiles + Send + Sync>,
        environ: Vec<(String, String)>,
    ) -> SweepSettings {
        SweepSettings { files, environ }
    }
}

impl SweepConfig for SweepSettings {
    fn purge_age(&mut self) -> Result<Option<String>, Failure> {
        self.key("session_purge_age")
    }

    fn retention(&mut self) -> Result<Option<String>, Failure> {
        self.key("session_transcript_retention")
    }
}

impl SweepSettings {
    /// The configuration's `key`, none where it is unset.
    fn key(&self, key: &str) -> Result<Option<String>, Failure> {
        // The notices the load raises belong to the commands that read the
        // whole configuration; a removal needs one key.
        let config = load(
            self.files.as_ref(),
            &self.environ,
            &Flags::default(),
            &mut Vec::new(),
        )
        .map_err(|e| Box::new(e) as Failure)?;
        Ok(Some(config.text(key)).filter(|value| !value.is_empty()))
    }
}

#[cfg(test)]
#[path = "sweep_config.test.rs"]
mod tests;
