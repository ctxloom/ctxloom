//! The library over a process's wiring: its ports over their default
//! adapters, the remotes it reads through, the engines that advise on a plan.
//! The CLI builds one in process and the daemon one for each caller; this
//! module names no frontend crate.

use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use companion_probe::ProcessProbe;
use config_files::{ConfigFilesFs, ScaffoldFs};
use content_fs::{ContentFs, UserFilesFs};
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
pub struct PathSearch {
    pub fs: Arc<dyn Fs>,
    pub dirs: Vec<PathBuf>,
    pub flavor: os::Flavor,
    pub pathext: Option<String>,
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

/// The program's words for a library read's diagnostics: the wiring's
/// catalogs and the library's own, read once, at the first diagnostic said;
/// a catalog that does not read says each by its code and args.
fn words() -> library_application::Words {
    static CATALOG: std::sync::OnceLock<Option<presentation::Catalog>> = std::sync::OnceLock::new();
    Arc::new(|d: &dyn coded_diagnostic::Diagnostic| {
        let catalog = CATALOG.get_or_init(|| {
            let mut sources = crate::catalog_sources();
            sources.extend([
                angzarr_yaml::EN_US,
                library::EN_US,
                library_application::EN_US,
                deps::EN_US,
                deps_application::EN_US,
            ]);
            presentation::Catalog::en_us(&sources).ok()
        });
        match catalog {
            Some(catalog) => catalog.message(d),
            None => d.to_string(),
        }
    })
}

/// The library as a run reads it: over the dependency use cases of `wiring`'s
/// project, so a bundle installed from a remote resolves (owner ruling
/// 2026-11-21: a library without the remotes is unrepresentable).
pub fn build(wiring: &Wiring) -> Result<Library, LibError> {
    let deps: Rc<dyn RemoteLibrary> = Rc::new(crate::compose::build_deps(wiring)?);
    build_over(wiring, deps)
}

/// The same library over `remotes`, for a caller that shares its dependency
/// use cases with the library (a command, a hosted run).
pub fn build_over(wiring: &Wiring, remotes: Rc<dyn RemoteLibrary>) -> Result<Library, LibError> {
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
        // A read the daemon answers says a diagnostic (an agent's resolution
        // error) in the program's words (step 10a U2).
        .with_words(words());
    // ctxloom's default content, built in (owner rulings 110, 111).
    let library = if builtins_withheld(wiring.env()) {
        library
    } else {
        library.with_builtins()
    };
    Ok(library.with_remotes(remotes))
}

/// The engines whose word on a run's plan is said (`Engine::advise`): those
/// the run drives. A test engine advises nothing, and is not asked.
pub fn advising_engines() -> Vec<Arc<dyn run_ports::Engine>> {
    let program = &crate::program::running().name;
    vec![
        Arc::new(engine_claude::Claude::calling(program)),
        Arc::new(engine_opencode::Opencode::calling(program)),
    ]
}

/// Each composed engine's hook events it has no native form for, with why
/// (`EngineSurfaces::hook_losses`), by its name.
fn hook_losses() -> library_application::HookLosses {
    let engines = crate::compose::engines();
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
