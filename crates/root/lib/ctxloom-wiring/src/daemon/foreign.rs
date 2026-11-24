//! Other tools' files, planned in the daemon where the caller stands
//! (step 10c U10): the setup use cases over a RECORDING surface store, and
//! the doctor over recording user files, so a use case runs whole and
//! writes nothing; what it would have left is the daemon's sequence to
//! write. Its answer is said as the API's message.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::rc::Rc;
use std::sync::{Arc, Mutex};

use ctxloom_ports::{Failure, Put, TreeFiles, UserFiles};
use daemon_api::{ApiError, ForeignAnswer, ForeignAsk, ForeignPlanned, ForeignPut};
use daemon_proto::v1;
use surface_cli::wire;
use surface_files::SurfaceFiles;

use crate::Wiring;

/// The setup use cases' foreign writers, each run as a process at the
/// caller's directory in the caller's environment would run it, with
/// ctxloom's `home`.
pub(crate) struct ForeignWriters {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

/// `cause` said as the API says a notice.
pub(crate) fn notice_of(cause: &dyn coded_diagnostic::Diagnostic) -> v1::Notice {
    v1::Notice {
        code: cause.code().to_string(),
        args: run_application::client::flat_args(cause),
        text: cause.to_string(),
    }
}

/// What a recording surface store recorded, as the daemon writes it.
pub(crate) fn puts_of(store: &SurfaceFiles) -> Vec<ForeignPut> {
    store
        .take_recorded()
        .into_iter()
        .map(|recorded| ForeignPut {
            path: recorded.path,
            bytes: recorded.bytes,
            executable: recorded.executable,
            keep: recorded.keep,
        })
        .collect()
}

impl daemon_api::ForeignPlans for ForeignWriters {
    fn plan(
        &self,
        dir: &str,
        env: &[(String, String)],
        flags: &library_application::Flags,
        ask: ForeignAsk<'_>,
    ) -> Result<ForeignPlanned, ApiError> {
        let at = if dir.is_empty() {
            self.home.display().to_string()
        } else {
            dir.to_string()
        };
        let pairs = env
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()));
        let wiring = super::caller_wiring(&self.home, &self.temp, (&at, None), pairs);
        let failed = |e: &dyn coded_diagnostic::Diagnostic| ApiError::diagnosed(e);
        if matches!(ask, ForeignAsk::DoctorFix) {
            return self.doctor_fix(&wiring);
        }
        let deps = Rc::new(crate::commands::build_deps(&wiring).map_err(|e| failed(&e))?);
        let store = Arc::new(SurfaceFiles::recording(
            wiring.fs(),
            wiring.home().join(".ctxloom/records"),
        ));
        let setup = crate::commands::build_setup_over(&wiring, deps, Arc::clone(&store))
            .map_err(|e| failed(&e))?;
        let refused = |error: setup_application::SetupError| {
            use cli_support::ExitClass;
            let exit = error.exit();
            daemon_api::setup_error(&error, exit)
        };
        let answer = match ask {
            ForeignAsk::Materialize(asked) => {
                let done = wire::materialize_request(asked, flags)
                    .and_then(|request| setup.materialize(&request))
                    .map_err(refused)?;
                ForeignAnswer::Materialized(v1::MaterializeResponse {
                    notices: done
                        .notices
                        .iter()
                        .map(run_application::client::notice_of)
                        .collect(),
                    ..wire::materialized(&done.value, &notice_of)
                })
            }
            ForeignAsk::Uninstall => {
                ForeignAnswer::Uninstalled(wire::uninstalled(&setup.uninstall().map_err(refused)?))
            }
            ForeignAsk::DoctorFix => unreachable!("answered above"),
        };
        Ok(ForeignPlanned {
            answer,
            files: puts_of(&store),
        })
    }
}

impl ForeignWriters {
    /// `doctor --fix` over recording user files: the stale entries it
    /// takes out, and each settings file as it would leave it.
    fn doctor_fix(&self, wiring: &Wiring) -> Result<ForeignPlanned, ApiError> {
        let uses =
            crate::setup::SetupUseCases::build(wiring, "").map_err(|e| ApiError::diagnosed(&e))?;
        let files = Arc::new(Recording::over(Arc::new(content_fs::UserFilesFs::new(
            wiring.fs(),
        ))));
        let doctor = uses.doctor.writing_through(files.clone());
        // A fix that fails is a failure (10), as in process.
        let removed = doctor.fix().map_err(|failure| {
            daemon_api::setup_error(failure.as_ref(), cli_support::Exit::FAILED)
        })?;
        let answer = v1::RunDoctorResponse {
            removed: removed
                .into_iter()
                .map(|r| v1::RemovedHook {
                    file: r.file,
                    event: r.event,
                    sub: r.sub,
                })
                .collect(),
            ..v1::RunDoctorResponse::default()
        };
        Ok(ForeignPlanned {
            answer: ForeignAnswer::Fixed(answer),
            files: files.take(),
        })
    }
}

/// User files that write nothing (step 10c U10): each write is recorded,
/// and read back by the reads after it.
pub(crate) struct Recording {
    inner: Arc<dyn UserFiles + Send + Sync>,
    written: Mutex<BTreeMap<String, Vec<u8>>>,
    order: Mutex<Vec<String>>,
}

impl Recording {
    pub(crate) fn over(inner: Arc<dyn UserFiles + Send + Sync>) -> Recording {
        Recording {
            inner,
            written: Mutex::new(BTreeMap::new()),
            order: Mutex::new(Vec::new()),
        }
    }

    /// What was written, in the order first written, each as left last.
    pub(crate) fn take(&self) -> Vec<ForeignPut> {
        let written = std::mem::take(&mut *self.written.lock().unwrap_or_else(|e| e.into_inner()));
        let order = std::mem::take(&mut *self.order.lock().unwrap_or_else(|e| e.into_inner()));
        order
            .into_iter()
            .filter_map(|path| {
                let bytes = written.get(&path)?.clone();
                let keep = Path::new(&path)
                    .parent()
                    .map(|dir| dir.display().to_string())
                    .unwrap_or_default();
                Some(ForeignPut {
                    path,
                    bytes: Some(bytes),
                    executable: false,
                    keep,
                })
            })
            .collect()
    }

    fn recorded(&self, path: &str) -> Option<Vec<u8>> {
        self.written
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .get(path)
            .cloned()
    }
}

impl UserFiles for Recording {
    fn exists(&self, path: &str) -> Result<bool, Failure> {
        if self.recorded(path).is_some() {
            return Ok(true);
        }
        self.inner.exists(path)
    }

    fn is_dir(&self, path: &str) -> Result<bool, Failure> {
        self.inner.is_dir(path)
    }

    fn read_file(&self, path: &str) -> Result<Vec<u8>, Failure> {
        match self.recorded(path) {
            Some(bytes) => Ok(bytes),
            None => self.inner.read_file(path),
        }
    }

    fn read_tree(&self, dir: &str) -> Result<TreeFiles, Failure> {
        self.inner.read_tree(dir)
    }

    fn write_file(&self, path: &str, bytes: &[u8], overwrite: bool) -> Result<bool, Failure> {
        if !overwrite && self.exists(path)? {
            return Ok(false);
        }
        let mut order = self.order.lock().unwrap_or_else(|e| e.into_inner());
        if !order.iter().any(|known| known == path) {
            order.push(path.to_string());
        }
        self.written
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .insert(path.to_string(), bytes.to_vec());
        Ok(true)
    }

    fn write_tree(&self, dir: &str, files: &[Put]) -> Result<(), Failure> {
        for put in files {
            self.write_file(&format!("{dir}/{}", put.path), &put.bytes, false)?;
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "foreign.test.rs"]
mod tests;
