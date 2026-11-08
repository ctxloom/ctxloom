//! `run --one-shot` as a driving adapter: arguments in, the engine's reply
//! out, no logic. The `run` command itself is the library's (it previews with
//! `--dry-run`); this crate adds the flags a one-shot takes ([`with_one_shot`])
//! and, for a line that names `--one-shot`, [`run`]s it: the prompt is given
//! as arguments, with `--prompt`, as a saved command, or piped on standard
//! input; the reply is written as the text itself, or as json with the name of
//! the session that made it; what the plan noticed is said on the error
//! stream.

use std::io::{self, Write};

use angzarr_presentation::{Catalog, Format};
use clap::{Arg, ArgAction, ArgMatches, Command};
use cli_support::{flag, many, one, say_notices};
use library_application::{Flags, RunRequest};
use run_application::{Answer, OneShot, RunService};
use serde::Serialize;

pub use cli_support::Failure;

/// One command, with the global flags the library's use cases take.
pub type Call<'a> = cli_support::Call<'a, Flags>;

/// The program's name, for the warnings.
const PROG: &str = "ctxloom";

/// `run` with the flags of a one-shot added.
pub fn with_one_shot(run: Command) -> Command {
    run.arg(
        Arg::new("one-shot")
            .long("one-shot")
            .action(ArgAction::SetTrue)
            .help("Take one turn on the prompt, print the reply and exit"),
    )
    .arg(
        Arg::new("engine")
            .long("engine")
            .value_name("ENGINE")
            .help("Drive this engine instead of the agent's"),
    )
}

/// Whether the line given to `run` asked for a one-shot.
pub fn is_one_shot(run: &ArgMatches) -> bool {
    flag(run, "one-shot")
}

/// What a one-shot answers as json.
#[derive(Serialize)]
struct Reply<'a> {
    harp: &'a str,
    session: &'a str,
    engine: &'a str,
    answer: &'a str,
}

/// Run the one-shot the line of `run` asks for. `piped` gives the text piped
/// on standard input, or nothing where standard input is a terminal; it is
/// asked only when the line gave no prompt by any other door.
pub fn run<S: RunService>(
    service: &mut S,
    run: &ArgMatches,
    call: &Call<'_>,
    catalog: &Catalog,
    piped: &mut dyn FnMut() -> Option<String>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    let format = call.format();
    let mut prompt = prompt_of(run);
    if prompt.trim().is_empty() && one(run, "command").unwrap_or_default().is_empty() {
        prompt = piped().unwrap_or_default();
    }
    let request = request_of(run, prompt);
    let done = service.one_shot(&call.globals, &request);
    say_notices(&done.notices, PROG, format, catalog, err);
    let answer = done.result.map_err(|error| {
        let exit = error.exit_code();
        Failure::new(error, exit)
    })?;
    write_answer(format, &answer, out).map_err(|e| {
        Failure::new(
            OutputFailure {
                ext_err_txt: e.to_string(),
            },
            1,
        )
    })?;
    Ok(0)
}

/// The prompt the line gave: `--prompt`, else the words.
fn prompt_of(run: &ArgMatches) -> String {
    let prompt = one(run, "prompt").unwrap_or_default();
    if prompt.is_empty() {
        many(run, "prompt_words").join(" ")
    } else {
        prompt
    }
}

fn request_of(run: &ArgMatches, prompt: String) -> OneShot {
    OneShot {
        run: RunRequest {
            agent: one(run, "agent").unwrap_or_default(),
            profile: one(run, "profile").unwrap_or_default(),
            fragments: many(run, "fragment"),
            tags: many(run, "tag"),
            llm: one(run, "llm").unwrap_or_default(),
            prompt,
            saved_command: one(run, "command").unwrap_or_default(),
            workspace: one(run, "workspace").unwrap_or_default(),
        },
        engine: one(run, "engine").unwrap_or_default(),
    }
}

/// `run` without `--one-shot`: the human's session. The engine is given her
/// terminal, so nothing is read from standard input and nothing is printed
/// here; what the plan noticed is said on the error stream, and the exit is
/// the engine's.
pub fn session<S: RunService>(
    service: &mut S,
    run: &ArgMatches,
    call: &Call<'_>,
    catalog: &Catalog,
    _out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    let request = request_of(run, prompt_of(run));
    let done = service.interactive(&call.globals, &request);
    say_notices(&done.notices, PROG, call.format(), catalog, err);
    let ended = done.result.map_err(|error| {
        let exit = error.exit_code();
        Failure::new(error, exit)
    })?;
    Ok(ended
        .code
        .and_then(|code| u8::try_from(code).ok())
        .unwrap_or(1))
}

fn write_answer(format: Format, answer: &Answer, out: &mut dyn Write) -> io::Result<()> {
    let reply = Reply {
        harp: &answer.harp,
        session: &answer.session,
        engine: &answer.engine,
        answer: &answer.answer,
    };
    cli_support::emit(format, out, &reply, |out| {
        writeln!(out, "{}", answer.answer.trim_end())
    })
}

/// The reply could not be written.
#[derive(Debug)]
struct OutputFailure {
    ext_err_txt: String,
}

impl std::fmt::Display for OutputFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl angzarr_diagnostic::Diagnostic for OutputFailure {
    fn code(&self) -> &str {
        angzarr_diagnostic::code!("output")
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        vec![("ext_err_txt", self.ext_err_txt.as_str().into())]
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn angzarr_diagnostic::Diagnostic>> {
    vec![Box::new(OutputFailure {
        ext_err_txt: "broken pipe".to_string(),
    })]
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
