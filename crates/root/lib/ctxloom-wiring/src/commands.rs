//! The program's commands as one extension of the command line: the
//! library's (S1) and the remote and dependency ones (S3). They share a noun,
//! `bundle` (the library inspects bundles; `push` and `move` publish and
//! relocate them), and the library reads the remotes through the dependency
//! use cases, so both are built together.

use cli_support::Exit;
use std::collections::BTreeMap;
use std::io::Write;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use angzarr_presentation::Catalog;
use clap::Command;
use content_fs::ContentFs;
use ctxloom_cli::{Call, Extension, Failure};
use deps::forge;
use deps::registry::Registry;
use deps_application::{Deps, ProjectRefresh, Refresh, RefreshEngine, RefusedKind};
use deps_files::ProjectFiles;
use deps_ports::DepsFiles;
use forge_github::GitHub;
use process_env::EnvSnapshot;
use remote_git::GitRemotes;
use setup_application::{MaterializeRequest, Puller, Setup};
use surface_files::SurfaceFiles;
use surface_ports::Registry as Engines;

use crate::sessions::{self, NOUNS as SESSION_NOUNS};
use crate::setup::{SetupUseCases, NOUNS as SETUP_NOUNS};
use crate::{LibError, LibraryCommands, Sessions, Wiring};

/// The nouns the dependency commands own outright.
const OWN_NOUNS: [&str; 2] = ["remote", "deps"];

/// The dependency use cases over the process's home, working directory and
/// project.
pub(crate) fn build_deps(wiring: &Wiring) -> Result<Deps, LibError> {
    let (_, app) = wiring.locate();
    let files = Arc::new(ProjectFiles::new(wiring.fs(), app.path.clone()));
    build_deps_over(wiring, files.clone(), files)
}

/// The dependency use cases over `store`, the deps files they read and
/// write (a recording one in the daemon, step 10c U4); `files` the
/// project's own, which a clone's token is looked up in.
pub(crate) fn build_deps_over(
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
pub(crate) fn git_remotes(wiring: &Wiring, files: Arc<ProjectFiles>) -> GitRemotes {
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

/// `run --one-shot`, or `run` (the human's session): the user's daemon plans
/// and records it, this command its client (U4) or its frontend (U5). A
/// one-shot's prompt piped on standard input is read only where nothing else
/// gave one; a session is given the terminal.
fn one_shot(
    (env, version): (&Arc<EnvSnapshot>, &str),
    run: &clap::ArgMatches,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let wiring = Wiring::new(Arc::clone(env)).map_err(|e| Failure::new(e, Exit::FAILED))?;
    let mut uses = crate::oneshot::OneShots::new(&wiring).stamped(version);
    if !run_cli::is_one_shot(run) {
        return run_cli::session(&mut uses, run, call, catalog, out, err);
    }
    run_cli::run(&mut uses, run, call, catalog, &mut piped_prompt, out, err)
}

/// The text piped on standard input, none where it is a terminal.
// Reads the process's own stdin, which a unit test cannot feed or make a terminal.
#[cfg_attr(test, mutants::skip)]
fn piped_prompt() -> Option<String> {
    use std::io::IsTerminal;
    let stdin = std::io::stdin();
    (!stdin.is_terminal()).then(|| std::io::read_to_string(stdin).unwrap_or_default())
}

/// The nouns the surface commands own outright.
const SURFACE_NOUNS: [&str; 3] = ["manage", "materialize", "util"];

/// The dependency pull `manage install` ends with.
pub(crate) struct DepsPuller(pub(crate) Rc<Deps>);

impl Puller for DepsPuller {
    fn pull(&self) -> Result<u64, String> {
        let pulled = self.0.pull(false, true);
        match pulled.result {
            Ok(report) if report.errors == 0 => Ok(report.installed),
            Ok(report) => Err(format!("{} could not be installed", report.errors)),
            Err(e) => Err(angzarr_diagnostic::Diagnostic::code(&e).to_string()),
        }
    }
}

/// The project refresh a consented pull ends with, through `materialize`:
/// into the project's directory, with the context of an engine whose context
/// rides the launch left alone. Without consent it is a dry run, and answers
/// only what would change.
pub(crate) struct SetupRefresh<'a> {
    pub(crate) setup: &'a Setup,
    pub(crate) flags: library_application::Flags,
}

impl ProjectRefresh for SetupRefresh<'_> {
    fn refresh(&self, write: bool) -> Result<Refresh, Box<dyn angzarr_diagnostic::Diagnostic>> {
        let done = self
            .setup
            .materialize(&MaterializeRequest {
                yes: true,
                dry_run: !write,
                leave_launch_context: true,
                flags: self.flags.clone(),
                ..MaterializeRequest::default()
            })
            .map_err(|e| Box::new(e) as Box<dyn angzarr_diagnostic::Diagnostic>)?;
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

/// The engines the daemon hosts a child on (T18), each its `Engine` beside
/// its `ChildEngine`, by name: claude, then opencode (its ACP child). The
/// launcher resolves a child's engine here, never by naming an adapter.
/// They are composed as EVERY launch's are (`oneshot::launch_engines`, the
/// one session-setup path): calling `program`, over `wiring`'s user's own
/// settings.
pub fn hosted_engines(wiring: &Wiring, program: &str) -> Vec<daemon_application::HostedEngine> {
    let crate::oneshot::LaunchEngines { claude, opencode } =
        crate::oneshot::launch_engines(wiring, program);
    // Each launch composed anew (T21): her settings read at that launch,
    // where her launcher finds them, never once at the daemon's start.
    let home = wiring.home().to_path_buf();
    let user_home = wiring.user_home();
    let temp = wiring.env().temp_dir().to_path_buf();
    let program = program.to_string();
    let for_launch = move |claude_side: bool| -> daemon_application::ForLaunch {
        let (home, user_home, temp, program) = (
            home.clone(),
            user_home.clone(),
            temp.clone(),
            program.clone(),
        );
        Arc::new(
            move |launcher: &BTreeMap<String, String>,
                  launched: Option<&daemon_application::Program>| {
                // The launcher's program, else the daemon's own (owner,
                // 2026-11-18).
                let program = launched.map_or(program.as_str(), |launched| launched.name.as_str());
                let engines = crate::daemon::launch_engines_for(
                    &home,
                    user_home.as_deref(),
                    &temp,
                    launcher,
                    program,
                );
                match claude_side {
                    true => (
                        engines.claude.clone() as Arc<dyn run_ports::Engine>,
                        engines.claude as Arc<dyn run_ports::ChildEngine>,
                    ),
                    false => (
                        engines.opencode.clone() as Arc<dyn run_ports::Engine>,
                        engines.opencode as Arc<dyn run_ports::ChildEngine>,
                    ),
                }
            },
        )
    };
    vec![
        daemon_application::HostedEngine {
            engine: claude.clone(),
            child: claude,
            transcripts: Arc::new(engine_claude::transcripts_of),
            for_launch: Some(for_launch(true)),
        },
        daemon_application::HostedEngine {
            engine: opencode.clone(),
            child: opencode,
            transcripts: Arc::new(|_| None),
            for_launch: Some(for_launch(false)),
        },
    ]
}

/// The setup use cases over the process's home, working directory and
/// project.
pub(crate) fn build_setup(wiring: &Wiring, deps: Rc<Deps>) -> Result<Setup, LibError> {
    let files = Arc::new(SurfaceFiles::new(
        wiring.fs(),
        wiring.home().join(".ctxloom/records"),
    ));
    build_setup_over(wiring, deps, files)
}

/// `setup`, its hooks reading the library and their contexts through the
/// daemon, as the client stamped `stamp` (a hook process's own setup).
pub(crate) fn with_hook_clients(setup: Setup, wiring: &Wiring, stamp: &str) -> Setup {
    setup
        // The hooks read the library through the daemon (O3), within their
        // bound from when the use cases were wired, which a hook does first.
        .with_hook_library(Box::new(
            crate::hook_library::DaemonHookLibrary::starting_now(Arc::clone(wiring.env()), stamp),
        ))
        // What a live session's hooks carry (a primary's pending messages,
        // the terminal wake, and what it is owed, U13), through the daemon
        // too, in ONE call (the lean hook RPC).
        .with_hook_contexts(Box::new(
            crate::hook_messages::DaemonHookContexts::starting_now(Arc::clone(wiring.env()), stamp),
        ))
}

/// The setup use cases over `files`, the surface store they deliver
/// through (a recording one in the daemon, step 10c U10).
pub(crate) fn build_setup_over(
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
    let library = crate::library::build_over(wiring, deps.clone())?;
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

/// The engines' callbacks that answer through the setup use cases.
pub struct Hooks<'a>(&'a Setup);

impl hook_cli::HookService for Hooks<'_> {
    fn execute(
        &self,
        engine: &str,
        native: &str,
        payload: &str,
        harp: &str,
    ) -> Result<setup_application::HookAnswer, Box<dyn angzarr_diagnostic::Diagnostic>> {
        self.0
            .execute_hook(engine, native, payload, harp)
            .map_err(|e| Box::new(e) as _)
    }

    fn acknowledge(&self, offered: &setup_application::Offered) -> bool {
        self.0.acknowledge(offered)
    }
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

/// Every command of the program, wired when one first needs the home.
pub struct Commands {
    /// The environment the process was launched in, read once.
    pub(crate) env: Arc<EnvSnapshot>,
    /// The binary's version stamp, as `ctxloom version` says it.
    pub(crate) version: String,
    wired: Option<Wired>,
    /// The sessions use cases, built when a command first needs them.
    pub(crate) sessions: Option<Sessions>,
    /// The diagnostics beyond the wiring's and the command line's that
    /// `ctxloom test diagnostics` lists: the composition root's to name.
    #[cfg(feature = "test-support")]
    samples: crate::Samples,
}

struct Wired {
    deps: Rc<Deps>,
    library: LibraryCommands,
    surface: Setup,
    setup: SetupUseCases,
}

impl Commands {
    /// The commands over `env`, of the binary stamped `version`.
    pub fn new(env: Arc<EnvSnapshot>, version: &str) -> Commands {
        Commands {
            env,
            version: version.to_string(),
            wired: None,
            sessions: None,
            #[cfg(feature = "test-support")]
            samples: Vec::new,
        }
    }

    /// The diagnostics `ctxloom test diagnostics` lists besides the wiring's
    /// and the command line's own.
    #[cfg(feature = "test-support")]
    #[must_use]
    pub fn with_diagnostic_samples(mut self, samples: crate::Samples) -> Commands {
        self.samples = samples;
        self
    }

    /// The engines' callbacks, over the setup use cases.
    pub fn hooks(&mut self) -> Result<Hooks<'_>, LibError> {
        Ok(Hooks(&self.wired()?.surface))
    }

    fn wired(&mut self) -> Result<&mut Wired, LibError> {
        if self.wired.is_none() {
            let wiring = Wiring::new(Arc::clone(&self.env))?;
            let deps = Rc::new(build_deps(&wiring)?);
            let library = LibraryCommands::new(Arc::clone(&self.env))
                .with_remotes(deps.clone())
                .with_version(&self.version);
            let surface =
                with_hook_clients(build_setup(&wiring, deps.clone())?, &wiring, &self.version);
            let setup = SetupUseCases::build(&wiring, &self.version)?;
            self.wired = Some(Wired {
                deps,
                library,
                setup,
                surface,
            });
        }
        Ok(self
            .wired
            .as_mut()
            .unwrap_or_else(|| unreachable!("just built")))
    }
}

impl Commands {
    /// A setup command: `auth`, `clean`, `doctor` or `init`.
    fn setup(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let env = Arc::clone(&self.env);
        let stamp = self.version.clone();
        let wired = self.wired().map_err(|e| Failure::new(e, Exit::FAILED))?;
        let terminal = crate::interview::StdTerminal::new(&env);
        let sessions = crate::interview::SetupSessions {
            env: &env,
            flags: &call.globals,
            stamp: &stamp,
        };
        let cleaning = crate::library::DaemonServed::new(Arc::clone(&env), stamp.clone());
        let uses = setup_cli::UseCases {
            auth: &wired.setup.auth,
            cleaning: &cleaning,
            fixing: &cleaning,
            reading: &cleaning,
            globals: library_api::reads::globals_of(&call.globals),
            doctor: &wired.setup.doctor,
            initializing: &cleaning,
            terminal: &terminal,
            sessions: &sessions,
            prompts: &sessions,
        };
        // `init` names the project it set up (owner rulings 2026-11-13 and
        // 14), as the daemon named it (step 10c U6: `Initializing`).
        setup_cli::run(&uses, noun, call, catalog, out, err)
    }
}

impl Extension for Commands {
    fn nouns(&self) -> Vec<Command> {
        let mut nouns: Vec<Command> = library_cli::nouns()
            .into_iter()
            .map(|noun| {
                if noun.get_name() == "run" {
                    return run_cli::with_one_shot(noun);
                }
                // ctxloom's own MCP server, hidden, beside the library's
                // `mcp server` listing (slice 3, T20).
                if noun.get_name() == crate::mcp::NOUN {
                    return noun.subcommand(mcp_server::verb());
                }
                if noun.get_name() != "bundle" {
                    return noun;
                }
                deps_cli::bundle_verbs()
                    .into_iter()
                    .fold(noun, |noun, verb| noun.subcommand(verb))
            })
            .collect();
        nouns.extend(deps_cli::nouns());
        nouns.extend(surface_cli::nouns());
        nouns.extend(setup_cli::nouns());
        nouns.extend(container_cli::nouns());
        nouns.extend(sessions::nouns());
        nouns.push(crate::daemon::noun());
        nouns.push(crate::daemon::shutdown::noun());
        #[cfg(feature = "test-support")]
        nouns.push(crate::test_commands::noun());
        nouns
    }

    fn run(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        // The project root the flag names, as `CTXLOOM_ROOT` for everything
        // wired after this (and the daemon, which reads the caller's whole
        // environment).
        self.env = crate::root_flag::applied(
            &self.env,
            call.matches
                .try_get_one::<String>("root")
                .ok()
                .flatten()
                .map(String::as_str),
        )
        .map_err(|e| Failure::new(e, cli_support::Exit::REFUSED))?;
        self.env = crate::root_flag::daemon_root_applied(
            &self.env,
            call.matches
                .try_get_one::<String>("daemon_root")
                .ok()
                .flatten()
                .map(String::as_str),
        );
        let ran = self.run_noun(noun, call, catalog, out, err);
        // What the flush on the way in set aside (s4-Q14, H3), said once
        // a session as the command ends.
        for set_aside in crate::daemon::flush::take_said() {
            cli_support::say_warning(&set_aside, ctxloom_cli::PROG, call.format(), catalog, err);
        }
        // What the daemon's answers warned of (OD-52: its start-up catch-up
        // failed; OD-54: it hands over), heard from the answers themselves
        // (OD-55), each said once as the command ends.
        for warning in crate::daemon::heard::take_heard() {
            cli_support::say_warning(&warning, ctxloom_cli::PROG, call.format(), catalog, err);
        }
        ran
    }
}

impl Commands {
    /// [`Extension::run`]'s command, before what its flush set aside is said.
    fn run_noun(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        #[cfg(feature = "test-support")]
        if noun == crate::test_commands::NOUN {
            let env = Arc::clone(&self.env);
            return crate::test_commands::run(
                || Wiring::new(env),
                &self.version,
                self.samples,
                call,
                catalog,
                out,
            );
        }
        if noun == crate::daemon::NOUN {
            return crate::daemon::run(&self.env, &self.version, call, catalog, out);
        }
        if noun == crate::daemon::shutdown::NOUN {
            let wiring = Wiring::new(Arc::clone(&self.env))
                .map_err(|e| Failure::new(e, cli_support::Exit::FAILED))?;
            return crate::daemon::shutdown::run(&wiring, &self.version, call, out);
        }
        let verb_of = |noun: &str| {
            call.matches
                .subcommand_matches(noun)
                .and_then(|m| m.subcommand_name())
                .unwrap_or_default()
                .to_string()
        };
        if crate::mcp::is_serve(noun, &verb_of(noun)) {
            let wakes = call
                .matches
                .subcommand_matches(noun)
                .and_then(|m| m.subcommand_matches(mcp_server::VERB))
                .is_none_or(mcp_server::posts_wakes);
            return crate::mcp::serve(&self.env, &self.version, wakes, out);
        }
        // A hook answers its engine whatever the store (it never fails the
        // engine); every other command in a project that still has a store
        // of its own is refused (T17, S48).
        let hook = SESSION_NOUNS.contains(&noun) && noun != "session";
        if !hook {
            if let Ok(wiring) = Wiring::new(Arc::clone(&self.env)) {
                wiring
                    .refuse_per_project_store()
                    .map_err(|refused| Failure::new(refused, Exit::REFUSED))?;
            }
        }
        if noun == "container" {
            return crate::isolation::container(&self.env, call, catalog, out);
        }
        if noun == sessions::TRUST_NOUN {
            return self.trust(call, catalog, out, err);
        }
        if noun == sessions::TOP_NOUN {
            return self.top(call, catalog, out, err);
        }
        if SESSION_NOUNS.contains(&noun) {
            if noun == "session" {
                return self.session(call, catalog, out, err);
            }
            // Some engines report a failed hook if stdin is closed before the
            // payload is read, so it is read to the end first.
            let input = std::io::read_to_string(std::io::stdin()).unwrap_or_default();
            return self.hook(&input, call, catalog, out, err);
        }
        if SETUP_NOUNS.contains(&noun) {
            return self.setup(noun, call, catalog, out, err);
        }
        let verb = call
            .matches
            .subcommand_matches(noun)
            .and_then(|m| m.subcommand_name())
            .unwrap_or_default()
            .to_string();
        let env = Arc::clone(&self.env);
        let version = self.version.clone();
        let names = crate::library::DaemonNames {
            env: Arc::clone(&self.env),
            version: self.version.clone(),
            minting: crate::library::minting(noun),
        };
        let wired = self.wired().map_err(|e| Failure::new(e, Exit::FAILED))?;
        if noun == "run" {
            let run = call.matches.subcommand_matches("run");
            if let Some(run) = run.filter(|m| !cli_support::flag(m, "dry-run")) {
                return one_shot((&env, &version), run, call, catalog, out, err);
            }
        }
        if SURFACE_NOUNS.contains(&noun) {
            let foreign = crate::library::DaemonServed::new(Arc::clone(&env), version.clone());
            return surface_cli::run(&wired.surface, &foreign, noun, call, catalog, out, err);
        }
        let ours =
            OWN_NOUNS.contains(&noun) || (noun == "bundle" && deps_cli::is_bundle_verb(&verb));
        if !ours {
            return wired.library.run(noun, call, catalog, out, err);
        }
        let served = crate::library::DaemonServed::new(env, version);
        let globals = library_api::reads::globals_of(&call.globals);
        deps_cli::run(
            &wired.deps,
            &names,
            (&served, Some(globals)),
            noun,
            call,
            catalog,
            out,
            err,
        )
    }
}

#[cfg(test)]
#[path = "commands.test.rs"]
pub(crate) mod tests;
