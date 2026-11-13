//! The library commands as a driving adapter (S1): arguments in, answers out,
//! no logic. `config show`, `bundle list|show|view`, `fragment|command|skill
//! list|show`, `fragment premises`, `profile list|show`, `agent list|show`,
//! `mcp server list|show`, `companion list`, `search`, `context
//! assemble` and `run --dry-run`.
//!
//! The whole command line is parsed by the program's own command line (one
//! behaviour for help, usage errors and the exit ladder); this crate gives it
//! the library's [`nouns`] and, given the matches of one, [`run`]s it: the
//! arguments become a request of the application, and its answer is written
//! as json or as text, with what it noticed said on the error stream.

mod api;
mod catalog;
mod commands;
pub mod inert;
mod noun;
mod text;
mod write;

/// What every noun's module uses: the shared helpers here, in `write` and
/// in `api`, and the types they speak.
mod prelude {
    pub(crate) use std::io::{self, Write};

    pub(crate) use angzarr_diagnostic::Diagnostic;
    pub(crate) use angzarr_presentation::Format;
    pub(crate) use clap::ArgMatches;
    pub(crate) use cli_support::{flag, many, say_notices, Exit};
    pub(crate) use library_application::{
        AgentRequest, AgentRow, AgentWritten, AppError, AssembleRequest, AuthSetting,
        BundleEditRequest, ConfigShown, DefaultAgent, EngineAgent, EngineRow, ItemKind, McpEdited,
        McpPatch, ProfileCreateRequest, ProfileEdit, ProfileEdited, RunRequest, SearchRequest,
        SwapPreview, WriteError, LOADOUT,
    };

    pub(crate) use crate::write::{
        finish_text, given, given_list, given_map, given_pairs, nothing, text_only,
    };
    pub(crate) use crate::{
        api, failed, output_failure, resource, text, text_arg, verb, Context, Failure, IntoFailure,
        PROG,
    };
    pub(crate) use daemon_proto::v1;
}

use cli_support::{Exit, ExitClass};
use std::io::{self, Write};

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{Catalog, Format};
use clap::ArgMatches;
use cli_support::{one, say_notices};
use library_application::{AppError, Flags, ItemKind, Library, Notice};
use serde::Serialize;

pub use commands::nouns;

/// The program's name, for the warnings.
pub(crate) const PROG: &str = "ctxloom";

pub use cli_support::Failure;

/// One command, with the global flags the library's use cases take.
pub type Call<'a> = cli_support::Call<'a, Flags>;

/// An application error as the failure it ends the command in.
pub(crate) trait IntoFailure {
    fn into_failure(self) -> Failure;
}

impl IntoFailure for AppError {
    fn into_failure(self) -> Failure {
        let exit = self.exit();
        Failure::new(self, exit)
    }
}

pub(crate) fn failed(error: AppError) -> Failure {
    error.into_failure()
}

/// Where a library resource's name is rooted (OD-40; OQ-2, OQ-3): the
/// project as the daemon's `ResolveProject` names it, `projects/{harp}`, or
/// `users/me` outside a project. Asked only when a name is printed (a json
/// answer), so a text answer reaches no daemon.
pub trait Names {
    /// The parent of this command's resources.
    fn parent(&self) -> Result<String, Failure>;
}

/// Everything a command needs to answer.
pub(crate) struct Context<'a> {
    library: &'a Library,
    names: &'a dyn Names,
    flags: Flags,
    format: Format,
    explicit: bool,
    catalog: &'a Catalog,
}

/// Run `noun` (given the matches of the whole line).
pub fn run(
    library: &Library,
    names: &dyn Names,
    noun: &str,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let flags = call.globals.clone();
    let format = call.explicit_format.unwrap_or_else(|| {
        if call.stdout_is_terminal {
            Format::Text
        } else {
            match library.preferred_format(&flags).as_deref() {
                Some("text") => Format::Text,
                _ => call.default_format,
            }
        }
    });
    let cx = Context {
        library,
        names,
        flags,
        format,
        explicit: call.explicit_format.is_some(),
        catalog,
    };
    let (_, sub) = call
        .matches
        .subcommand()
        .filter(|(name, _)| *name == noun)
        .ok_or_else(|| failed(AppError::SearchNothing))?;
    dispatch(&cx, noun, sub, out, err)
}

/// The verb of a noun's matches, or its default.
pub(crate) fn verb<'a>(sub: &'a ArgMatches, default: &'a str) -> (&'a str, &'a ArgMatches) {
    match sub.subcommand() {
        Some((name, matches)) => (name, matches),
        None => (default, sub),
    }
}

fn dispatch(
    cx: &Context<'_>,
    noun: &str,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    match noun {
        "config" => written(noun::config::write(cx, verb(sub, "show"), out, err), || {
            noun::config::read(cx, verb(sub, "show").1, out, err)
        }),
        "bundle" => written(noun::bundle::write(cx, verb(sub, "list"), out, err), || {
            noun::bundle::read(cx, verb(sub, "list"), out, err)
        }),
        "fragment" | "command" => {
            let kind = if noun == "fragment" {
                ItemKind::Fragment
            } else {
                ItemKind::Command
            };
            written(
                noun::item::write(cx, kind, verb(sub, "list"), out, err),
                || noun::item::read(cx, noun, verb(sub, "list"), out, err),
            )
        }
        "skill" => written(noun::skill::write(cx, verb(sub, "list"), out, err), || {
            noun::skill::read(cx, verb(sub, "list"), out, err)
        }),
        "profile" => written(
            noun::profile::write(cx, verb(sub, "list"), out, err),
            || noun::profile::read(cx, verb(sub, "list"), out, err),
        ),
        "agent" => written(noun::agent::write(cx, verb(sub, "list"), out, err), || {
            noun::agent::read(cx, verb(sub, "list"), out, err)
        }),
        "engine" => noun::engine::read(cx, verb(sub, "list"), out, err),
        "mcp" => noun::mcp::read(cx, sub, out, err),
        "companion" => written(
            noun::companion::write(cx, verb(sub, "list"), out, err),
            || noun::companion::read(cx, out, err),
        ),
        "loadout" => noun::companion::loadout(sub, out),
        "search" => noun::search::read(cx, sub, out, err),
        "context" => noun::context::read(cx, verb(sub, "assemble").1, out, err),
        _ => noun::run::read(cx, sub, out, err),
    }
}

/// A write verb's answer, or (it was not one) the read verb's.
fn written(
    write: Result<Option<Exit>, Failure>,
    read: impl FnOnce() -> Result<Exit, Failure>,
) -> Result<Exit, Failure> {
    match write? {
        Some(code) => Ok(code),
        None => read(),
    }
}

/// The notices, said; then the answer's error or its output.
pub(crate) fn answer<T>(
    cx: &Context<'_>,
    outcome: library_application::Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    write: impl FnOnce(T, &mut dyn Write, &mut Vec<String>) -> io::Result<Exit>,
) -> Result<Exit, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let value = outcome.result.map_err(failed)?;
    let mut inert = Vec::new();
    let code = write(value, out, &mut inert).map_err(output_failure)?;
    let said: Vec<Notice> = inert.into_iter().map(Notice::text).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(code)
}

pub(crate) fn output_failure(error: io::Error) -> Failure {
    Failure::new(OutputFailure(error.to_string()), Exit::FAILED)
}

/// The answer could not be written.
#[derive(Debug)]
struct OutputFailure(String);

impl std::fmt::Display for OutputFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for OutputFailure {
    fn code(&self) -> &str {
        "ctxloom-cli-output"
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        vec![("ext_err_txt", self.0.as_str().into())]
    }
}

pub(crate) fn text_arg(matches: &ArgMatches, name: &str) -> String {
    one(matches, name).unwrap_or_default()
}

/// A converted noun's answer (OD-40): the notices, said; then its error,
/// or its json, the view as its API message named under the parent the
/// names give (asked only here), or its text.
pub(crate) fn resource<T, M: Serialize>(
    cx: &Context<'_>,
    outcome: library_application::Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    message: impl FnOnce(&str, &T) -> M,
    text: impl FnOnce(&mut dyn Write, &T) -> io::Result<()>,
) -> Result<Exit, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let value = outcome.result.map_err(failed)?;
    if cx.format == Format::Json {
        let parent = cx.names.parent()?;
        api::print(out, &message(&parent, &value)).map_err(output_failure)?;
    } else {
        text(out, &value).map_err(output_failure)?;
    }
    Ok(Exit::OK)
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
