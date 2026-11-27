//! The setup commands as a driving adapter (S9): arguments in, answers out, no
//! logic. `manage install|uninstall|check|hooks|statusline|gitignore`,
//! `materialize` and the hidden `util config-write`.
//!
//! The whole command line is parsed by the program's own command line (one
//! behaviour for help, usage errors and the exit ladder); this crate gives it
//! the setup [`nouns`] and, given the matches of one, [`run`]s it.

mod commands;
mod in_process;
mod text;
pub use in_process::InProcess;

use cli_support::{Exit, ExitClass};
use std::io::{self, Read, Write};

use clap::ArgMatches;
use cli_support::{flag, many, one, say_notices};
use daemon_proto::v1;
use library_application::Flags;
use presentation::{Catalog, Format};
use serde::Serialize;
use setup_application::{
    DiffRequest, EngineOutcome, MaterializeResult, NotCarried, Setup, SetupError, SurfaceSpec,
    UninstallReport,
};

pub use commands::nouns;

/// The program's name, for the warnings.
const PROG: &str = "ctxloom";

pub use cli_support::Failure;

/// One command, with the global flags the library's use cases take.
pub type Call<'a> = cli_support::Call<'a, Flags>;

/// A setup error as the failure it ends the command in.
trait IntoFailure {
    fn into_failure(self) -> Failure;
}

impl IntoFailure for SetupError {
    fn into_failure(self) -> Failure {
        let exit = self.exit();
        Failure::new(self, exit)
    }
}

fn failed(error: SetupError) -> Failure {
    error.into_failure()
}

/// The setup writes the daemon makes (step 10c U10): other tools' files,
/// written as one sequence of its own.
pub trait Foreign {
    fn materialize(
        &self,
        request: v1::MaterializeRequest,
    ) -> Result<v1::MaterializeResponse, Failure>;
    fn uninstall(
        &self,
        request: v1::UninstallManageRequest,
    ) -> Result<v1::UninstallManageResponse, Failure>;
    /// `manage check`, the daemon's read (step 10c U5): each engine's
    /// wiring read off its files where the caller stands.
    fn check(&self, request: v1::CheckManageRequest) -> Result<v1::CheckManageResponse, Failure>;
    /// `manage gitignore install`, the daemon's write (step 10c U5):
    /// `.ctxloom/.gitignore`, one file of the project's library.
    fn install_gitignore(
        &self,
        request: v1::InstallGitignoreRequest,
    ) -> Result<v1::InstallGitignoreResponse, Failure>;
    /// `manage install`, the daemon's write (step 10c U6): a new project
    /// created whole as init creates it, or one there given its
    /// `.gitignore`.
    fn install_manage(
        &self,
        request: v1::InstallManageRequest,
    ) -> Result<v1::InstallManageResponse, Failure>;
    /// The project the daemon named `project_id` bound at its root; empty,
    /// the one there, named as found.
    fn bind_project(&self, project_id: &str) -> Result<(), Failure>;
}

/// The report a materialize's answer says, as it has always been printed.
pub fn materialize_report(answered: &v1::MaterializeResponse) -> MaterializeResult {
    MaterializeResult {
        target: answered.target.clone(),
        created: answered.created,
        profiles: answered.profiles.clone(),
        engines: answered
            .engines
            .iter()
            .map(|engine| EngineOutcome {
                engine: engine.engine.clone(),
                wrote: engine.wrote.clone(),
                released: engine.released.clone(),
                context_file: engine.context_file.clone(),
                not_carried: engine
                    .not_carried
                    .iter()
                    .map(|lost| NotCarried {
                        surface: lost.surface.clone(),
                        reason: lost.reason.clone(),
                    })
                    .collect(),
                skipped: engine.skipped.clone(),
                warnings: engine.warnings.clone(),
                refused: Vec::new(),
            })
            .collect(),
        status: answered.status.clone(),
        errors: answered.errors.clone(),
        warnings: answered.warnings.clone(),
        failures: Vec::new(),
    }
}

struct Context<'a> {
    setup: &'a Setup,
    foreign: &'a dyn Foreign,
    flags: Flags,
    format: Format,
    catalog: &'a Catalog,
}

/// Run `noun` (given the matches of the whole line).
pub fn run(
    setup: &Setup,
    foreign: &dyn Foreign,
    noun: &str,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let format = call.explicit_format.unwrap_or(if call.stdout_is_terminal {
        Format::Text
    } else {
        call.default_format
    });
    let cx = Context {
        setup,
        foreign,
        flags: call.globals.clone(),
        format,
        catalog,
    };
    let (_, sub) = call
        .matches
        .subcommand()
        .filter(|(name, _)| *name == noun)
        .ok_or_else(|| {
            failed(SetupError::Request {
                detail: format!("no {noun} command"),
            })
        })?;
    match noun {
        "manage" => manage(&cx, sub, out, err),
        "materialize" => materialize(&cx, sub, out, err),
        _ => util(&cx, sub, &mut io::stdin(), out),
    }
}

fn emit<T: Serialize>(
    cx: &Context<'_>,
    out: &mut dyn Write,
    value: &T,
    text: impl FnOnce(&mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    cli_support::emit_or(cx.format, out, value, text, |e| {
        failed(SetupError::Request {
            detail: format!("the answer could not be written: {e}"),
        })
    })
}

/// `manage install`, the daemon's (step 10c U6): the engine refused by
/// name first, and asked of a project there refused from the preview, so
/// nothing is written either way.
fn install(
    cx: &Context<'_>,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<(), Failure> {
    let engine = one(args, "engine");
    cx.setup.check_engine(engine.as_deref()).map_err(failed)?;
    let request = v1::InstallManageRequest {
        engine: engine.clone().unwrap_or_default(),
        caller: v1::Caller::of(Some(library_api::reads::globals_of(&cx.flags)), None),
        ..v1::InstallManageRequest::default()
    };
    let preview = cx.foreign.install_manage(v1::InstallManageRequest {
        validate_only: true,
        ..request.clone()
    })?;
    setup_application::engine_applies(engine.as_deref(), !preview.initialized).map_err(failed)?;
    let answered = cx.foreign.install_manage(request)?;
    cx.foreign.bind_project(&answered.project_id)?;
    say_v1(cx, &answered.notices, err);
    let report = setup_application::InstallReport {
        app_dir: answered.app_dir,
        initialized: answered.initialized,
        gitignore: answered.gitignore,
        gitignore_status: answered.gitignore_status,
        pull: cx.setup.pull_report(flag(args, "no-pull")),
    };
    emit(cx, out, &report, |w| text::install(w, &report))?;
    Ok(())
}

fn manage(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let Some((verb, args)) = sub.subcommand() else {
        return Ok(Exit::OK);
    };
    match verb {
        "install" if flag(args, "print") => {
            let plan = cx
                .setup
                .install_plan(one(args, "engine").as_deref())
                .map_err(failed)?;
            emit(cx, out, &plan, |w| text::plan(w, &plan))?;
        }
        "install" => install(cx, args, out, err)?,
        "uninstall" => {
            let answered = cx.foreign.uninstall(v1::UninstallManageRequest {
                caller: v1::Caller::of(Some(library_api::reads::globals_of(&cx.flags)), None),
                ..v1::UninstallManageRequest::default()
            })?;
            say_v1(cx, &answered.notices, err);
            let report = UninstallReport {
                status: answered.status,
                backends: answered.backends,
                errors: answered.errors,
                note: answered.note,
            };
            emit(cx, out, &report, |w| text::uninstall(w, &report))?;
            if let Some(first) = report.errors.first() {
                return Err(failed(SetupError::Request {
                    detail: first.clone(),
                }));
            }
        }
        "check" => check(cx, out, err)?,
        "hooks" => match args.subcommand() {
            Some(("list", list)) => {
                let done = cx
                    .setup
                    .hooks_list(
                        one(list, "event").as_deref(),
                        &many(list, "profile"),
                        &cx.flags,
                    )
                    .map_err(failed)?;
                say_notices(&done.notices, PROG, cx.format, cx.catalog, err);
                emit(cx, out, &done.value, |w| text::hooks(w, &done.value))?;
            }
            _ => check(cx, out, err)?,
        },
        "statusline" => {
            let enabled = matches!(args.subcommand_name(), Some("install"));
            if args.subcommand_name().is_none() {
                return Ok(Exit::OK);
            }
            let done = cx
                .setup
                .set_statusline(enabled, &cx.flags)
                .map_err(failed)?;
            say_notices(&done.notices, PROG, cx.format, cx.catalog, err);
            let state = if done.value { "enabled" } else { "disabled" };
            let doc = StatuslineAnswer {
                status: state,
                statusline: done.value,
            };
            emit(cx, out, &doc, |w| {
                writeln!(w, "ctxloom HUD statusline: {state}")?;
                writeln!(
                    w,
                    "Run 'ctxloom run' or 'ctxloom materialize' to apply the change."
                )
            })?;
        }
        _ => {
            if args.subcommand_name() == Some("install") {
                let answered = cx.foreign.install_gitignore(v1::InstallGitignoreRequest {
                    caller: v1::Caller::of(Some(library_api::reads::globals_of(&cx.flags)), None),
                    ..v1::InstallGitignoreRequest::default()
                })?;
                say_v1(cx, &answered.notices, err);
                let report = surface_api::wire::gitignore_report(&answered);
                emit(cx, out, &report, |w| text::gitignore(w, &report))?;
            }
        }
    }
    Ok(Exit::OK)
}

/// The daemon's notices, said as the use case's own were.
fn say_v1(cx: &Context<'_>, notices: &[v1::Notice], err: &mut dyn Write) {
    let said: Vec<_> = notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
}

#[derive(Serialize)]
struct StatuslineAnswer {
    status: &'static str,
    statusline: bool,
}

fn check(cx: &Context<'_>, out: &mut dyn Write, err: &mut dyn Write) -> Result<(), Failure> {
    let answered = cx.foreign.check(v1::CheckManageRequest {
        caller: v1::Caller::of(Some(library_api::reads::globals_of(&cx.flags)), None),
        ..v1::CheckManageRequest::default()
    })?;
    say_v1(cx, &answered.notices, err);
    let report = surface_api::wire::check_report(&answered);
    emit(cx, out, &report, |w| text::check(w, &report))
}

fn materialize(
    cx: &Context<'_>,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let surfaces = many(args, "surface")
        .iter()
        .map(|s| SurfaceSpec::parse(s))
        .collect::<Result<Vec<_>, _>>()
        .map_err(failed)?;
    if let Some(file) = one(args, "diff") {
        let request = DiffRequest {
            profiles: many(args, "profiles"),
            engines: many(args, "backend"),
            surfaces,
            release: flag(args, "release"),
            file,
            flags: cx.flags.clone(),
        };
        let done = cx.setup.materialize_diff(&request).map_err(failed)?;
        say_notices(&done.notices, PROG, cx.format, cx.catalog, err);
        emit(cx, out, &done.value, |w| {
            text::materialize_diff(w, &done.value)
        })?;
        return Ok(Exit::OK);
    }
    // Parsed above so a malformed one is refused before the daemon is
    // asked; the daemon parses what it is sent.
    let _ = surfaces;
    let answered = cx.foreign.materialize(v1::MaterializeRequest {
        profiles: many(args, "profiles"),
        target: one(args, "target").unwrap_or_default(),
        yes: flag(args, "yes"),
        backends: many(args, "backend"),
        surfaces: many(args, "surface"),
        release: flag(args, "release"),
        dry_run: flag(args, "dry-run"),
        caller: v1::Caller::of(Some(library_api::reads::globals_of(&cx.flags)), None),
        ..v1::MaterializeRequest::default()
    })?;
    say_v1(cx, &answered.notices, err);
    let report = materialize_report(&answered);
    emit(cx, out, &report, |w| text::materialize(w, &report))?;
    // A delivery that was refused leaves the file as it was, and the command
    // is refused (ruling 152): a script gating on the exit code must not
    // believe it landed.
    match answered.failures.first() {
        Some(first) => Err(Failure::new(
            library_api::reads::reported(first),
            Exit::REFUSED,
        )),
        None => Ok(Exit::OK),
    }
}

/// `util config-write`: the patch is read from `input` (stdin), and only when
/// the verb is there.
fn util(
    cx: &Context<'_>,
    sub: &ArgMatches,
    input: &mut dyn Read,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let Some(("config-write", args)) = sub.subcommand() else {
        return Ok(Exit::OK);
    };
    let mut patch = String::new();
    input.read_to_string(&mut patch).map_err(|e| {
        failed(SetupError::ConfigWrite {
            detail: format!("read patch from stdin: {e}"),
        })
    })?;
    let file = one(args, "file").unwrap_or_default();
    let done = cx
        .setup
        .config_write(&file, one(args, "filetype").as_deref(), &patch)
        .map_err(failed)?;
    emit(cx, out, &done, |w| {
        writeln!(
            w,
            "Merged {} into {} ({})",
            done.merged_keys.join(", "),
            done.file,
            if done.created { "created" } else { "updated" }
        )
    })?;
    Ok(Exit::OK)
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;

#[cfg(test)]
#[path = "world.test.rs"]
mod world;
