//! The library commands (S1) wired: the content and config files over the
//! process's filesystem, the application over them, and the driving adapter
//! joined to the command line as an extension.

use cli_support::Exit;
use std::io::Write;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use angzarr_presentation::Catalog;
use companion_probe::ProcessProbe;
use config_files::{ConfigFilesFs, ScaffoldFs};
use content_fs::{ContentFs, UserFilesFs};
use ctxloom_cli::{Call, Extension, Failure};
use ctxloom_ports::Executables;
use deps_ports::RemoteLibrary;
use editor_process::ProcessEditor;
use file_system::{Fs, Kind};
use library_application::{Library, WritePorts};
use process_env::EnvSnapshot;

use crate::{LibError, Wiring};

/// Where a program is on the `PATH`, by the filesystem seam, by the file
/// names the host looks a command up by (`git`; on Windows `git.exe` and the
/// rest of `PATHEXT`'s).
pub(crate) struct PathSearch {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) dirs: Vec<PathBuf>,
    pub(crate) flavor: os::Flavor,
    pub(crate) pathext: Option<String>,
}

impl PathSearch {
    fn runnable(&self, candidate: &std::path::Path) -> Option<String> {
        let meta = self.fs.metadata(candidate).ok()?;
        (meta.kind == Kind::File && meta.executable).then(|| candidate.display().to_string())
    }
}

impl Executables for PathSearch {
    fn find(&self, name: &str) -> Option<String> {
        // A name with a separator is the file it names, not a name to search
        // the directories for.
        if name.contains(['/', '\\']) {
            return self.runnable(std::path::Path::new(name));
        }
        let names = self.flavor.candidates(name, self.pathext.as_deref());
        self.dirs
            .iter()
            .find_map(|dir| names.iter().find_map(|file| self.runnable(&dir.join(file))))
    }
}

/// The library's commands as an extension of the command line. The library is
/// wired when a command first needs it, so a command that needs nothing of the
/// home (`version`) does not.
pub struct LibraryCommands {
    /// The environment the process was launched in.
    env: Arc<EnvSnapshot>,
    library: Option<Library>,
    /// What the library reads of the remotes, where the program has them.
    remotes: Option<Rc<dyn RemoteLibrary>>,
    /// This binary's stamp, which the daemon that names the project must
    /// share (M3).
    version: String,
}

impl LibraryCommands {
    pub fn new(env: Arc<EnvSnapshot>) -> LibraryCommands {
        LibraryCommands {
            env,
            library: None,
            remotes: None,
            version: String::new(),
        }
    }

    /// The library naming its resources under the project the daemon of
    /// this stamp names (OD-40, OQ-2).
    pub fn with_version(mut self, version: &str) -> LibraryCommands {
        self.version = version.to_string();
        self
    }

    /// The library reading the remotes too.
    pub fn with_remotes(mut self, remotes: Rc<dyn RemoteLibrary>) -> LibraryCommands {
        self.remotes = Some(remotes);
        self
    }

    fn wired(&mut self) -> Result<&Library, LibError> {
        if self.library.is_none() {
            let wiring = Wiring::new(Arc::clone(&self.env))?;
            self.library = Some(build(&wiring, self.remotes.clone())?);
        }
        Ok(self
            .library
            .as_ref()
            .unwrap_or_else(|| unreachable!("just built")))
    }
}

pub(crate) fn build(
    wiring: &Wiring,
    remotes: Option<Rc<dyn RemoteLibrary>>,
) -> Result<Library, LibError> {
    let fs = wiring.fs();
    let home = wiring.home().to_path_buf();
    let (around, app) = wiring.locate();
    let content = ContentFs::new(Arc::clone(&fs), &app.path);
    let files = ConfigFilesFs::new(Arc::clone(&fs), app, &home);
    // Overrides are text: a variable that is not UTF-8 is none (it is never
    // an error, as `std::env::vars` made it).
    let environ = wiring.env().vars_utf8().into_iter().collect();
    let ports = WritePorts {
        user: Arc::new(UserFilesFs::new(Arc::clone(&fs))),
        scaffold: Arc::new(ScaffoldFs::new(Arc::clone(&fs), &around.cwd)),
        editor: Arc::new(ProcessEditor::new(Arc::clone(&fs), &around.temp_root)),
    };
    let library = Library::new(Arc::new(content), Arc::new(files), environ)
        .with_write_ports(ports)
        // The one companion probe (OD-8): it holds no environment or
        // directory; each call names its site (D1), the caller's environment
        // and the project's root, else the working directory here.
        .with_companions(Arc::new(ProcessProbe::standard(
            Arc::clone(&fs),
            wiring.env().flavor(),
        )))
        .with_probe_cwd(around.cwd.display().to_string())
        // The engines a plan is shown to, for what they say of it (owner ruling
        // 168): the real ones, as the run drives them.
        .with_engines(advising_engines())
        // What each engine cannot carry, in its own word (its surface half): the
        // capability-loss report and the swap preview read it.
        .with_hook_losses(hook_losses())
        // Distill's model: a one-shot on no context (plan step 5, OD-38).
        .with_distiller(Rc::new(crate::distiller::OneShotDistiller {
            env: Arc::clone(wiring.env()),
        }));
    // ctxloom's default content, built in (owner rulings 110, 111).
    let library = if builtins_withheld(wiring.env()) {
        library
    } else {
        library.with_builtins()
    };
    Ok(match remotes {
        Some(remotes) => library.with_remotes(remotes),
        None => library,
    })
}

/// The engines whose word on a run's plan is said (`Engine::advise`): those
/// the run drives. A test engine advises nothing, and is not asked.
fn advising_engines() -> Vec<Arc<dyn run_ports::Engine>> {
    let program = &crate::program::running().name;
    vec![
        Arc::new(engine_claude::Claude::calling(program)),
        Arc::new(engine_opencode::Opencode::calling(program)),
    ]
}

/// Each composed engine's hook events it has no native form for, with why
/// (`EngineSurfaces::hook_losses`), by its name.
fn hook_losses() -> library_application::HookLosses {
    let engines = crate::commands::engines();
    engines
        .names()
        .into_iter()
        .filter_map(|name| {
            let losses = engines.get(&name)?.hook_losses();
            Some((name, losses))
        })
        .collect()
}

/// Under `test-support`, `CTXLOOM_TEST_NO_BUILTINS` withholds the built-in
/// bundles, so the Go-parity tests hold the catalog Go had; it is compiled
/// out of the shipped binary.
#[cfg(feature = "test-support")]
fn builtins_withheld(env: &EnvSnapshot) -> bool {
    env.non_empty("CTXLOOM_TEST_NO_BUILTINS").is_some()
}

/// The shipped binary always has its built-in bundles.
#[cfg(not(feature = "test-support"))]
fn builtins_withheld(_: &EnvSnapshot) -> bool {
    false
}

/// The library's names (and the remotes' and dependencies', B4), rooted
/// where the daemon's `ResolveProject` says (OD-40, OQ-2): asked only when a
/// json answer prints a name.
pub(crate) struct DaemonNames {
    pub(crate) env: Arc<EnvSnapshot>,
    pub(crate) version: String,
    /// Whether naming the project may mint its id ([`minting`]).
    pub(crate) minting: crate::Minting,
}

/// Whether `noun`'s names may mint the project's id (owner ruling
/// 2026-11-14): every command that names its project may, at its root, but
/// the library's `run`, which is the dry run alone (a launch is the run use
/// case's), never does: "dry run should *not* mint".
pub(crate) fn minting(noun: &str) -> crate::Minting {
    if noun == "run" {
        crate::Minting::Never
    } else {
        crate::Minting::AtRoot
    }
}

impl DaemonNames {
    fn parent(&self) -> Result<String, Failure> {
        let wiring =
            Wiring::new(Arc::clone(&self.env)).map_err(|e| Failure::new(e, Exit::FAILED))?;
        crate::daemon::library_parent(&wiring, &self.version, self.minting)
    }
}

impl library_cli::Names for DaemonNames {
    fn parent(&self) -> Result<String, Failure> {
        DaemonNames::parent(self)
    }
}

impl deps_cli::Names for DaemonNames {
    fn parent(&self) -> Result<String, Failure> {
        DaemonNames::parent(self)
    }
}

impl Extension for LibraryCommands {
    fn nouns(&self) -> Vec<clap::Command> {
        library_cli::nouns()
    }

    fn run(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let names = DaemonNames {
            env: Arc::clone(&self.env),
            version: self.version.clone(),
            minting: minting(noun),
        };
        let library = self.wired().map_err(|e| Failure::new(e, Exit::FAILED))?;
        library_cli::run(library, &names, noun, call, catalog, out, err)
    }
}

#[cfg(test)]
#[path = "library.test.rs"]
mod tests;
