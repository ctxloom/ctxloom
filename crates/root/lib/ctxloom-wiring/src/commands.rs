//! The program's commands as one extension of the command line: the
//! library's (S1) and the remote and dependency ones (S3). They share a noun,
//! `bundle` (the library inspects bundles; `push` and `move` publish and
//! relocate them), and the library reads the remotes through the dependency
//! use cases, so both are built together.

use crate::compose::{build_deps, build_setup};
use crate::sessions::{self, NOUNS as SESSION_NOUNS};
use crate::setup::{SetupUseCases, NOUNS as SETUP_NOUNS};
use crate::{LibError, LibraryCommands, Sessions, Wiring};
use clap::Command;
use cli_support::Exit;
use ctxloom_cli::{Call, Extension, Failure};
use deps_application::Deps;
use presentation::Catalog;
use process_env::EnvSnapshot;
use setup_application::Setup;
use std::io::Write;
use std::rc::Rc;
use std::sync::Arc;
/// The nouns the dependency commands own outright.
const OWN_NOUNS: [&str; 2] = ["remote", "deps"];
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
/// `setup`, its hooks reading the library and their contexts through the
/// daemon, as the client stamped `stamp` (a hook process's own setup).
pub fn with_hook_clients(setup: Setup, wiring: &Wiring, stamp: &str) -> Setup {
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
/// The engines' callbacks that answer through the setup use cases.
pub struct Hooks<'a>(&'a Setup);
impl hook_cli::HookService for Hooks<'_> {
    fn execute(
        &self,
        engine: &str,
        native: &str,
        payload: &str,
        harp: &str,
    ) -> Result<setup_application::HookAnswer, Box<dyn coded_diagnostic::Diagnostic>> {
        self.0
            .execute_hook(engine, native, payload, harp)
            .map_err(|e| Box::new(e) as _)
    }

    fn acknowledge(&self, offered: &setup_application::Offered) -> bool {
        self.0.acknowledge(offered)
    }
}
/// Every command of the program, wired when one first needs the home.
pub struct Commands {
    /// The environment the process was launched in, read once.
    pub env: Arc<EnvSnapshot>,
    /// The binary's version stamp, as `ctxloom version` says it.
    pub version: String,
    wired: Option<Wired>,
    /// The sessions use cases, built when a command first needs them.
    pub sessions: Option<Sessions>,
    /// The daemon's composition, handed in by the binary.
    entry: crate::daemon::DaemonEntry,
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
            entry: crate::daemon::DaemonEntry::NONE,
            #[cfg(feature = "test-support")]
            samples: Vec::new,
        }
    }

    /// The daemon's composition (`ctxloom-daemon`'s [`DaemonEntry`]): what runs
    /// `daemon serve` and hosts the store's test commands. The binary hands it in, so the CLI's parse stays here.
    ///
    /// [`DaemonEntry`]: crate::daemon::DaemonEntry
    #[must_use]
    pub fn entered_by(mut self, entry: crate::daemon::DaemonEntry) -> Commands {
        self.entry = entry;
        self
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
        nouns.push(crate::daemon::cli::noun());
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
    /// The nouns about ctxloom's own process (the test commands, the daemon,
    /// shutdown, the MCP server), answered before any project is looked at;
    /// `None` for every other noun.
    fn run_host_noun(
        &self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
    ) -> Option<Result<Exit, Failure>> {
        #[cfg(feature = "test-support")]
        if noun == crate::test_commands::NOUN {
            let env = Arc::clone(&self.env);
            return Some(crate::test_commands::run(
                || Wiring::new(env),
                &self.version,
                (self.samples, self.entry.store_tests),
                call,
                catalog,
                out,
            ));
        }
        if noun == crate::daemon::NOUN {
            return Some(crate::daemon::cli::run(
                self.entry.serve,
                &self.env,
                &self.version,
                call,
                catalog,
                out,
            ));
        }
        if noun == crate::daemon::shutdown::NOUN {
            let ran = Wiring::new(Arc::clone(&self.env))
                .map_err(|e| Failure::new(e, cli_support::Exit::FAILED))
                .and_then(|wiring| crate::daemon::shutdown::run(&wiring, &self.version, call, out));
            return Some(ran);
        }
        let serve = call
            .matches
            .subcommand_matches(noun)
            .and_then(|m| m.subcommand_name());
        if crate::mcp::is_serve(noun, serve.unwrap_or_default()) {
            let wakes = call
                .matches
                .subcommand_matches(noun)
                .and_then(|m| m.subcommand_matches(mcp_server::VERB))
                .is_none_or(mcp_server::posts_wakes);
            return Some(crate::mcp::serve(&self.env, &self.version, wakes, out));
        }
        None
    }

    /// [`Extension::run`]'s command, before what its flush set aside is said.
    fn run_noun(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        if let Some(ran) = self.run_host_noun(noun, call, catalog, out) {
            return ran;
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
pub mod tests;
