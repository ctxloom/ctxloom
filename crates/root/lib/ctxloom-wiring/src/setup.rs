//! The setup commands (S12) wired: the cache, the doctor's files and the
//! scaffold over the process's filesystem, and the use cases over them.

use std::rc::Rc;
use std::sync::Arc;

use auth_application::Auth;
use cache_files::CacheFiles;
use config_files::{ConfigFilesFs, ScaffoldFs};
use content_fs::UserFilesFs;
use deps_application::Deps;
use doctor_checks::{Doctor, GitIdentity};
use init_application::{Create, Failure, Init};
use library_application::Library;
use reclaim_application::Cleaner;

use crate::{LibError, ProcessEnv, Wiring};

/// The nouns these commands own.
pub(crate) const NOUNS: [&str; 4] = ["auth", "clean", "doctor", "init"];

/// `config create`, as init's scaffold of a new project, and the output
/// directory init then writes into it. The environment builds a library over
/// the project as it is when the directory is written: the one built for the
/// scaffold saw no project yet.
struct ConfigCreate(Library, Arc<process_env::EnvSnapshot>);

impl Create for ConfigCreate {
    fn create(&self) -> Result<(), Failure> {
        let created = self.0.config_create("");
        created
            .result
            .map(|_| ())
            .map_err(|e| Box::new(e) as Failure)
    }

    fn deps(&self) -> Result<Rc<Deps>, Failure> {
        let wiring = Wiring::new(Arc::clone(&self.1)).map_err(|e| Box::new(e) as Failure)?;
        crate::commands::build_deps(&wiring)
            .map(Rc::new)
            .map_err(|e| Box::new(e) as Failure)
    }

    fn set_output_dir(&self, dir: &str) -> Result<(), Failure> {
        let wiring = Wiring::new(Arc::clone(&self.1)).map_err(|e| Box::new(e) as Failure)?;
        let library = crate::library::build(&wiring, None).map_err(|e| Box::new(e) as Failure)?;
        library
            .set_output_dir(&library_application::Flags::default(), dir)
            .result
            .map(|_| ())
            .map_err(|e| Box::new(e) as Failure)
    }

    fn set_premised(&self, word: &str) -> Result<(), Failure> {
        let wiring = Wiring::new(Arc::clone(&self.1)).map_err(|e| Box::new(e) as Failure)?;
        let library = crate::library::build(&wiring, None).map_err(|e| Box::new(e) as Failure)?;
        library
            .set_premised(&library_application::Flags::default(), word)
            .result
            .map(|_| ())
            .map_err(|e| Box::new(e) as Failure)
    }
}

/// The identity git commits with in the directory ctxloom was run in, asked
/// of git as a session home's is (`oneshot::identity_git`: her environment,
/// her `HOME`), never read from a `.gitconfig` in ctxloom's home.
struct GitIdentityIn {
    git: worktree_git::Git,
    dir: std::path::PathBuf,
}

impl GitIdentity for GitIdentityIn {
    fn is_known(&self) -> bool {
        let user = self.git.user(&self.dir);
        !user.name.is_empty() && !user.email.is_empty()
    }
}

/// The setup use cases, built together.
pub(crate) struct SetupUseCases {
    pub(crate) auth: Auth,
    pub(crate) cleaner: Cleaner,
    pub(crate) doctor: Doctor,
    pub(crate) init: Init,
}

impl SetupUseCases {
    /// The use cases of the binary stamped `version` (doctor reports it).
    pub(crate) fn build(
        wiring: &Wiring,
        deps: Rc<Deps>,
        version: &str,
    ) -> Result<SetupUseCases, LibError> {
        let fs = wiring.fs();
        let home = wiring.home().to_path_buf();
        let (around, app) = wiring.locate();
        let user = Arc::new(UserFilesFs::new(Arc::clone(&fs)));
        let binaries = wiring.path_search();
        let cleaner = Cleaner::new(Arc::new(CacheFiles::new(Arc::clone(&fs), &app.path)));
        let config = ConfigFilesFs::new(Arc::clone(&fs), app, &home);
        let doctor = Doctor::new(
            Arc::new(config),
            user.clone(),
            Arc::new(binaries),
            Arc::new(GitIdentityIn {
                git: crate::oneshot::identity_git(wiring),
                dir: around.cwd.clone(),
            }),
            Auth::new(Arc::new(ProcessEnv::from_snapshot(wiring.env()))),
            // Her own HOME for her files, never ctxloom's home.
            wiring.env().home().and_then(std::ffi::OsStr::to_str),
            version,
        )
        // An entry running this binary, by its file name, is ctxloom's too
        // (owner ruling 163).
        .calling(&crate::program::running().name)
        // The project's worktrees ctxloom did not create (j001300), every
        // project's scratch checkouts being ctxloom's.
        .reporting_worktrees(Arc::new(worktree_git::GitForeignWorktrees::new(
            Arc::clone(&fs),
            isolation_ports::scratch_root(&home),
            Arc::clone(wiring.env()),
        )))
        // What each agent's engine cannot carry, as the library reports it.
        .reporting_losses({
            let library = crate::library::build(wiring, None)?;
            Box::new(move || {
                library
                    .agents_losses(&library_application::Flags::default())
                    .result
                    .unwrap_or_default()
            })
        })
        // The distilled forms that no longer match their sources (S51).
        .reporting_stale_distilled({
            let library = crate::library::build(wiring, None)?;
            Box::new(move || {
                library
                    .stale_distilled(&library_application::Flags::default())
                    .result
                    .unwrap_or_default()
            })
        })
        // The enabled registered companions, each asked through the
        // library's one probe (OD-8), as a session's assembly asks them.
        .reporting_companions({
            let library = crate::library::build(wiring, None)?;
            Box::new(move || library.companion_inits(&library_application::Flags::default()))
        });
        let init = Init::new(
            Arc::new(ScaffoldFs::new(Arc::clone(&fs), &around.cwd)),
            user,
            deps,
            Box::new(ConfigCreate(
                crate::library::build(wiring, None)?,
                Arc::clone(wiring.env()),
            )),
        );
        Ok(SetupUseCases {
            auth: Auth::new(Arc::new(ProcessEnv::from_snapshot(wiring.env()))),
            cleaner,
            doctor,
            init,
        })
    }
}

#[cfg(test)]
#[path = "setup.test.rs"]
mod tests;
