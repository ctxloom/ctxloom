//! The backend's use cases built over a process's wiring: the dependency and
//! setup use cases, the remotes over git and the engines composed in. The CLI
//! builds them in process (`commands.rs`) and the daemon builds them where its
//! caller stands; this module names no frontend crate.

use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use content_fs::ContentFs;
use deps::forge;
use deps::registry::Registry;
use deps_application::{Deps, ProjectRefresh, Refresh, RefreshEngine, RefusedKind};
use deps_files::ProjectFiles;
use deps_ports::DepsFiles;
use forge_github::GitHub;
use remote_git::GitRemotes;
use setup_application::{MaterializeRequest, Puller, Setup};
use surface_files::SurfaceFiles;
use surface_ports::Registry as Engines;

use crate::{LibError, Wiring};

/// The dependency use cases over the process's home, working directory and
/// project.
pub fn build_deps(wiring: &Wiring) -> Result<Deps, LibError> {
    let (_, app) = wiring.locate();
    let files = Arc::new(ProjectFiles::new(wiring.fs(), app.path.clone()));
    build_deps_over(wiring, files.clone(), files)
}

/// The dependency use cases over `store`, the deps files they read and
/// write (a recording one in the daemon, step 10c U4); `files` the
/// project's own, which a clone's token is looked up in.
pub fn build_deps_over(
    wiring: &Wiring,
    store: Arc<dyn DepsFiles>,
    files: Arc<ProjectFiles>,
) -> Result<Deps, LibError> {
    let env = Arc::clone(wiring.env());
    let (_, app) = wiring.locate();
    Ok(Deps::new(
        Arc::new(git_remotes(wiring, files)),
        store,
        Arc::new(ContentFs::new(wiring.fs(), &app.path)),
    )
    .with_forge(Arc::new(GitHub::with_env(move |name| {
        env.utf8(name).map(String::from)
    }))))
}

/// The remotes over git, in `wiring`'s environment (the caller's, in the
/// daemon: step 10c U4), cloned under its project's cache; a clone's token
/// looked up in `files`' registry.
pub fn git_remotes(wiring: &Wiring, files: Arc<ProjectFiles>) -> GitRemotes {
    let env = Arc::clone(wiring.env());
    let (_, app) = wiring.locate();
    GitRemotes::new(wiring.fs(), app.path.join("cache/repos"), Arc::clone(&env)).with_credentials(
        Arc::new({
            let env = Arc::clone(&env);
            move |name| env.utf8(name).map(String::from)
        }),
        token_vars(files),
    )
}

/// The dependency pull `manage install` ends with.
pub struct DepsPuller(pub Rc<Deps>);

impl Puller for DepsPuller {
    fn pull(&self) -> Result<u64, String> {
        let pulled = self.0.pull(false, true);
        match pulled.result {
            Ok(report) if report.errors == 0 => Ok(report.installed),
            Ok(report) => Err(format!("{} could not be installed", report.errors)),
            Err(e) => Err(coded_diagnostic::Diagnostic::code(&e).to_string()),
        }
    }
}

/// The project refresh a consented pull ends with, through `materialize`:
/// into the project's directory, with the context of an engine whose context
/// rides the launch left alone. Without consent it is a dry run, and answers
/// only what would change.
pub struct SetupRefresh<'a> {
    pub setup: &'a Setup,
    pub flags: library_application::Flags,
}

impl ProjectRefresh for SetupRefresh<'_> {
    fn refresh(&self, write: bool) -> Result<Refresh, Box<dyn coded_diagnostic::Diagnostic>> {
        let done = self
            .setup
            .materialize(&MaterializeRequest {
                yes: true,
                dry_run: !write,
                leave_launch_context: true,
                flags: self.flags.clone(),
                ..MaterializeRequest::default()
            })
            .map_err(|e| Box::new(e) as Box<dyn coded_diagnostic::Diagnostic>)?;
        let result = done.value;
        if write {
            if let Some(first) = result.failures.into_iter().next() {
                return Err(Box::new(first));
            }
            return Ok(Refresh {
                target: result.target,
                engines: Vec::new(),
            });
        }
        Ok(Refresh {
            target: result.target,
            engines: result
                .engines
                .into_iter()
                .map(|e| RefreshEngine {
                    engine: e.engine,
                    wrote: e.wrote,
                    released: e.released,
                    refused: e
                        .refused
                        .into_iter()
                        .map(|r| RefusedKind {
                            kind: r.kind,
                            cause: Box::new(r.cause),
                        })
                        .collect(),
                })
                .collect(),
        })
    }
}

/// The engines composed in: claude (the default), then opencode. Under
/// `test-support` the mock and its home-keyed, lossy and skill-less variants
/// follow them; a build without it registers no mock (ruling 52). Each one's
/// callbacks run the running binary, by its file name (owner ruling 163).
pub fn engines() -> Engines {
    let program = &crate::program::running().name;
    #[cfg_attr(not(feature = "test-support"), expect(unused_mut))]
    let mut composed: Vec<Box<dyn surface_ports::EngineSurfaces>> = vec![
        Box::new(engine_claude::Claude::calling(program)),
        Box::new(engine_opencode::Opencode::calling(program)),
    ];
    #[cfg(feature = "test-support")]
    composed.extend([
        Box::new(engine_mock::Mock::standard().calling(program))
            as Box<dyn surface_ports::EngineSurfaces>,
        Box::new(engine_mock::Mock::home_keyed().calling(program)),
        Box::new(engine_mock::Mock::lossy().calling(program)),
        Box::new(engine_mock::Mock::no_skills().calling(program)),
    ]);
    Engines::new(composed)
}

/// The setup use cases over the process's home, working directory and
/// project.
pub fn build_setup(wiring: &Wiring, deps: Rc<Deps>) -> Result<Setup, LibError> {
    let files = Arc::new(SurfaceFiles::new(
        wiring.fs(),
        wiring.home().join(".ctxloom/records"),
    ));
    build_setup_over(wiring, deps, files)
}

/// The setup use cases over `files`, the surface store they deliver
/// through (a recording one in the daemon, step 10c U10).
pub fn build_setup_over(
    wiring: &Wiring,
    deps: Rc<Deps>,
    files: Arc<SurfaceFiles>,
) -> Result<Setup, LibError> {
    let fs = wiring.fs();
    let cwd = wiring.env().cwd().unwrap_or_else(|_| PathBuf::from("."));
    let (_, app) = wiring.locate();
    let project_dir = match app.source {
        ctxloom_ports::Source::Project => {
            app.path.parent().map_or_else(|| cwd.clone(), PathBuf::from)
        }
        ctxloom_ports::Source::Home => cwd.clone(),
    };
    let library = crate::library_build::build_over(wiring, deps.clone())?;
    Ok(Setup::new(
        library,
        engines(),
        files.clone(),
        Arc::new(config_files::ScaffoldFs::new(fs, &project_dir)),
        project_dir.display().to_string(),
        cwd.display().to_string(),
    )
    .with_os(wiring.os_name())
    .with_foreign(files)
    .with_puller(Rc::new(DepsPuller(deps))))
}

/// The variables a clone address's token is read from: the forge the
/// registry (or the built-in rules) binds the address to says so, the same
/// answer the API calls get. A registry that cannot be read is an empty one
/// here: the clone then goes with the user's own git credentials, and the
/// commands that need the registry report it.
fn token_vars(files: Arc<ProjectFiles>) -> remote_git::TokenVars {
    Arc::new(move |url| {
        let registry = files
            .read_registry()
            .ok()
            .flatten()
            .and_then(|text| Registry::decode(&text, &files.registry_path()).ok())
            .unwrap_or_else(Registry::empty);
        forge::resolve(url, "", &registry).token_vars()
    })
}
