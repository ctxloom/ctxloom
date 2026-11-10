//! The setup commands as a driving adapter (S12): arguments in, answers out,
//! no logic. `auth status`, `clean`, `doctor` and `init` (with `init prompt`).
//!
//! The whole command line is parsed by the program's own (one behaviour for
//! help, usage errors and the exit ladder); this crate gives it these nouns
//! and, given the matches of one, runs it on the use cases.
//!
//! - `auth [status]`: per engine, whether its credential is exported, by
//!   variable name and never its value; the only verb, so no command here
//!   takes a credential.
//! - `clean [--yes]`: reports the cache it would remove and says outright
//!   that it removed nothing, until `--yes`.
//! - `doctor [--all] [--deps] [--fix]`: one row per check, each with its
//!   `DOCTOR-CHECK-*` marker; json is `{checks: [...]}`. Exit 3 when a row
//!   failed, after the whole report. The text report lists the warnings;
//!   `--all` lists every check.
//! - `init` and `init prompt` print text whatever the format: the prompt is
//!   the body an engine is handed.

mod commands;
mod text;

use std::io::Write;

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{write_json, Catalog, Format};
use auth_application::Auth;
use clap::ArgMatches;
use cli_support::{say_notices, say_warning};
use deps_application::Notice;
use doctor_checks::Doctor;
use init_application::{interview, Init, InitRequest, Outcome, Sessions, Terminal};
use reclaim_application::Cleaner;
use sessions_application::RemovedNothing;

pub use commands::nouns;

/// The program's name, for the warnings.
const PROG: &str = "ctxloom";

pub use cli_support::{Call, Failure};

/// The use cases the commands run on.
pub struct UseCases<'a> {
    pub auth: &'a Auth,
    pub cleaner: &'a Cleaner,
    pub doctor: &'a Doctor,
    pub init: &'a Init,
    /// Whether a person is there, and her answer.
    pub terminal: &'a dyn Terminal,
    /// The interactive sessions the interview launches.
    pub sessions: &'a dyn Sessions,
}

fn failure(cause: impl Diagnostic + 'static, exit: u8) -> Failure {
    Failure::new(cause, exit)
}

fn output(error: std::io::Error) -> Failure {
    failure(
        CliFault::Output {
            ext_err_txt: error.to_string(),
        },
        1,
    )
}

mod fault;
#[cfg(any(test, feature = "test-support"))]
pub use fault::diagnostic_samples;
pub use fault::{CliFault, EN_US};

/// Run `noun` (given the matches of the whole line).
pub fn run<G>(
    uses: &UseCases<'_>,
    noun: &str,
    call: &Call<'_, G>,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    let format = call.explicit_format.unwrap_or(call.default_format);
    let sub = call.matches.subcommand_matches(noun).ok_or_else(|| {
        failure(
            CliFault::NoSuchCommand {
                noun: noun.to_string(),
            },
            2,
        )
    })?;
    match noun {
        "auth" => auth(uses, format, out),
        "clean" => clean(uses, sub, format, catalog, out, err),
        "doctor" => doctor(uses, sub, format, out, err),
        _ => init(uses, sub, format, catalog, out, err),
    }
}

/// `auth` and `auth status`: the credential report.
fn auth(uses: &UseCases<'_>, format: Format, out: &mut dyn Write) -> Result<u8, Failure> {
    let rows = uses.auth.status(&uses.doctor.layer_texts());
    match format {
        Format::Json => write_json(out, &rows),
        Format::Text => text::auth(&rows, out),
    }
    .map_err(output)?;
    Ok(0)
}

fn clean(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    let yes = sub.get_flag("yes");
    let report = uses.cleaner.clean(yes).map_err(|e| failure(e, 1))?;
    match format {
        Format::Json => write_json(out, &report),
        Format::Text => text::clean(&report, out),
    }
    .map_err(output)?;
    if !yes {
        say_warning(
            &RemovedNothing {
                command: "ctxloom clean --yes".to_string(),
            },
            PROG,
            format,
            catalog,
            err,
        );
    }
    Ok(0)
}

fn doctor(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    if sub.get_flag("fix") {
        let removed = uses
            .doctor
            .fix()
            .map_err(|e| Failure { cause: e, exit: 1 })?;
        for r in &removed {
            let _ = writeln!(
                err,
                "removed stale hook entry: {} runs `ctxloom hook {}` in {}",
                r.event, r.sub, r.file
            );
        }
    }
    let report = uses.doctor.report(sub.get_flag("deps"));
    match format {
        Format::Json => write_json(out, &report),
        Format::Text => text::doctor(&report, sub.get_flag("all"), out),
    }
    .map_err(output)?;
    // A failing row ends the run with run's own status for a fatal finding,
    // after the whole report is written.
    Ok(exit_of(&report))
}

/// The exit status a report ends the run with.
fn exit_of(report: &doctor_checks::Report) -> u8 {
    if report.failed() {
        3
    } else {
        0
    }
}

/// What init says when no token is exported, from claude's own docs:
/// `claude setup-token` makes a one-year token for a Pro, Max, Team or
/// Enterprise plan.
fn token_guide() -> Notice {
    Notice::with_fix(
        "delegated agents need a token, and none is exported (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY)",
        "run `claude setup-token` (it needs a Pro, Max, Team or Enterprise plan, and the token lasts a year), \
         then export CLAUDE_CODE_OAUTH_TOKEN with it, or store it in your secret manager",
    )
}

fn init(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<u8, Failure> {
    if sub.subcommand_matches("prompt").is_some() {
        write!(out, "{}", init_application::prompt()).map_err(output)?;
        return Ok(0);
    }
    let request = InitRequest {
        remotes: sub
            .get_many::<String>("remote")
            .map(|v| v.cloned().collect())
            .unwrap_or_default(),
        forge: sub.get_one::<String>("forge").cloned().unwrap_or_default(),
    };
    let done = uses.init.init(&request).map_err(|e| failure(e, 1))?;
    if done.existed {
        writeln!(out, "ctxloom directory already exists: {}", done.app_dir).map_err(output)?;
    } else {
        writeln!(out, "Created ctxloom directory: {}", done.app_dir).map_err(output)?;
        writeln!(out, "Pulled {} dependencies", done.pulled).map_err(output)?;
    }
    for remote in &done.remotes {
        writeln!(out, "Added remote \"{}\": {}", remote.name, remote.url).map_err(output)?;
    }
    let notices: &[Notice] = &done.notices;
    for notice in notices {
        say_warning(notice, PROG, Format::Text, catalog, err);
    }
    // Delegated agents authenticate with a token the human mints and
    // exports; init guides her to it, and never blocks on it or asks for it.
    if !uses.auth.agent_token_exported() {
        say_notices(&[token_guide()], PROG, format, catalog, err);
    }
    // On a terminal, the setup interview, and the offer to restart into the
    // session it configured. Off one, nothing is launched and nothing asked.
    let interviewed = interview(uses.terminal, uses.sessions).map_err(|launch| Failure {
        cause: launch.cause,
        exit: launch.exit,
    })?;
    for notice in &interviewed.notices {
        say_warning(notice.as_ref(), PROG, Format::Text, catalog, err);
    }
    Ok(match interviewed.outcome {
        Outcome::NotATerminal => 0,
        Outcome::Declined => {
            writeln!(
                out,
                "Start your configured session when you are ready: {}",
                init_application::RESTART_COMMAND
            )
            .map_err(output)?;
            0
        }
        Outcome::Interrupted { code } | Outcome::Restarted { code } => exit_of_session(code),
    })
}

/// The exit status a session ended with: the engine's, 1 where a signal
/// ended it or it is not a status.
fn exit_of_session(code: Option<i32>) -> u8 {
    code.and_then(|code| u8::try_from(code).ok()).unwrap_or(1)
}

#[cfg(test)]
#[path = "text.test.rs"]
mod text_tests;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
