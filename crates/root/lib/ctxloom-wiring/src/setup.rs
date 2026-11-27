//! The setup commands (S12) wired: the cache, the doctor's files and the
//! scaffold over the process's filesystem, and the use cases over them.

use std::sync::Arc;

use auth_application::Auth;
use config_files::ConfigFilesFs;
use content_fs::UserFilesFs;
use doctor_checks::{Doctor, GitIdentity};

use crate::{LibError, ProcessEnv, Wiring};

/// The nouns these commands own.
pub const NOUNS: [&str; 4] = ["auth", "clean", "doctor", "init"];

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
pub struct SetupUseCases {
    pub auth: Auth,
    pub doctor: Doctor,
}

impl SetupUseCases {
    /// The use cases of the binary stamped `version` (doctor reports it).
    pub fn build(wiring: &Wiring, version: &str) -> Result<SetupUseCases, LibError> {
        let fs = wiring.fs();
        let home = wiring.home().to_path_buf();
        let (around, app) = wiring.locate();
        let user = Arc::new(UserFilesFs::new(Arc::clone(&fs)));
        let binaries = wiring.path_search();
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
            let library = crate::library::build(wiring)?;
            Box::new(move || {
                library
                    .agents_losses(&library_application::Flags::default())
                    .result
                    .unwrap_or_default()
            })
        })
        // The agents whose run would be refused as planned (smoke
        // 2026-11-19: a profile including a bundle that is gone).
        .reporting_unresolved({
            let library = crate::library::build(wiring)?;
            Box::new(move || {
                library
                    .agents_unresolved(&library_application::Flags::default())
                    .result
                    .unwrap_or_default()
            })
        })
        // The distilled forms that no longer match their sources (S51).
        .reporting_stale_distilled({
            let library = crate::library::build(wiring)?;
            Box::new(move || {
                library
                    .stale_distilled(&library_application::Flags::default())
                    .result
                    .unwrap_or_default()
            })
        })
        // The bundles whose metadata is in the old layout (step 10c U5,
        // D15), each said in the library's words with the move.
        .reporting_old_layouts({
            let library = crate::library::build(wiring)?;
            Box::new(move || {
                let found = library
                    .old_layouts(&library_application::Flags::default())
                    .result
                    .unwrap_or_default();
                said_old_layouts(&found)
            })
        })
        // The enabled registered companions, each asked through the
        // library's one probe (OD-8), as a session's assembly asks them.
        .reporting_companions({
            let library = crate::library::build(wiring)?;
            Box::new(move || library.companion_inits(&library_application::Flags::default()))
        })
        // This home's holds (s4-Q14, H4), judged as this binary's commands
        // flush them: by its own stamp (a test's under test-support).
        .reporting_holds(
            Arc::new(fact_spool::Spool::at_home(Arc::clone(&fs), &home)),
            crate::daemon::own_stamp(wiring.env(), version),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX)),
        );
        Ok(SetupUseCases {
            auth: Auth::new(Arc::new(ProcessEnv::from_snapshot(wiring.env()))),
            doctor,
        })
    }
}

/// Each bundle in the old layout (step 10c U5, D15), its refusal and the
/// move that migrates it, in the library's en-US words.
pub fn said_old_layouts(found: &[library::BundleError]) -> Vec<(String, String)> {
    use coded_diagnostic::Diagnostic;
    let Ok(catalog) = presentation::Catalog::en_us(&[library::EN_US]) else {
        return Vec::new();
    };
    found
        .iter()
        .map(|cause| {
            let fix = cause.args().into_iter().fold(
                coded_diagnostic::Report::new(format!("{}-fix", cause.code())),
                |fix, (name, value)| fix.arg(name.to_string(), value),
            );
            (
                catalog.message(cause),
                catalog.render(&fix).unwrap_or_default(),
            )
        })
        .collect()
}

#[cfg(test)]
#[path = "setup.test.rs"]
mod tests;
