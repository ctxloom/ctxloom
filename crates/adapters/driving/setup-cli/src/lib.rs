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
//!   `DOCTOR-CHECK-*` marker; json is `{checks: [...]}`. Exit 20 when a row
//!   failed, after the whole report. The text report lists the warnings;
//!   `--all` lists every check.
//! - `init` and `init prompt` print text whatever the format: the prompt is
//!   the body an engine is handed.

mod commands;
mod text;
pub mod wire;

use cli_support::Exit;
use std::io::Write;

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{write_json, Catalog, Format};
use auth_application::Auth;
use clap::ArgMatches;
use cli_support::{say_notices, say_warning};
use daemon_proto::v1;
use deps_application::Notice;
use doctor_checks::Doctor;
use init_application::{interview, Outcome, Prompts, Sessions, Terminal};
use reclaim_application::{CleanReport, CleanTarget};
use sessions_application::RemovedNothing;

pub use commands::nouns;

/// The program's name, for the warnings.
const PROG: &str = "ctxloom";

pub use cli_support::{Call, Failure};

/// The use cases the commands run on.
/// `clean` as the daemon answers it (step 10b U9, `Clean`): its cache trees
/// taken into the project's trash, or (`validate_only`) reported.
pub trait Cleaning {
    fn clean(&self, request: v1::CleanRequest) -> Result<v1::CleanResponse, Failure>;
}

/// `doctor --fix` through the daemon (step 10c U10): the stale entries
/// taken out as one sequence of other tools' files.
pub trait DoctorFixing {
    fn fix(&self, request: v1::RunDoctorRequest) -> Result<v1::RunDoctorResponse, Failure>;
}

/// The setup reads through the daemon (step 10c U5 and U6, D1): each
/// answered where the caller stands, none in process.
pub trait SetupReading {
    fn doctor(&self, request: v1::RunDoctorRequest) -> Result<v1::RunDoctorResponse, Failure>;
    fn auth_status(
        &self,
        request: v1::GetAuthStatusRequest,
    ) -> Result<v1::GetAuthStatusResponse, Failure>;
    fn init_prompt(
        &self,
        request: v1::GetInitPromptRequest,
    ) -> Result<v1::GetInitPromptResponse, Failure>;
}

/// `init` through the daemon (step 10c U6): its preview's needs and its
/// apply, then what the client does with what it answered.
pub trait Initializing {
    /// `InitProject`: a preview (`validate_only`) or an apply.
    fn init_project(
        &self,
        request: v1::InitProjectRequest,
    ) -> Result<v1::InitProjectResponse, Failure>;

    /// The project at the root bound (taskloom's marker and index; owner
    /// rulings 2026-11-13 and 14: init names its project): as the daemon
    /// named it, `project_id`, for a new one; for one that was there
    /// (`project_id` empty), the id it has, else one minted.
    fn bind_project(&self, project_id: &str) -> Result<(), Failure>;
}

/// The report `answered` says, as `clean` has always printed it.
pub fn report_of(answered: &v1::CleanResponse) -> CleanReport {
    CleanReport {
        applied: answered.applied,
        targets: answered
            .items
            .iter()
            .map(|item| CleanTarget {
                rel: item.rel.clone(),
                path: item.path.clone(),
                present: item.present,
                bytes: item.bytes,
                rebuild: item.rebuild.clone(),
                removed: item.removed,
            })
            .collect(),
        bytes: answered.bytes,
    }
}

pub struct UseCases<'a> {
    pub auth: &'a Auth,
    /// `clean`, the daemon's (step 10b U9).
    pub cleaning: &'a dyn Cleaning,
    /// `doctor --fix`, the daemon's (step 10c U10).
    pub fixing: &'a dyn DoctorFixing,
    /// `doctor`, `auth status` and `init prompt`, the daemon's (step 10c
    /// U5, U6).
    pub reading: &'a dyn SetupReading,
    /// The call's global flags, as each request carries them.
    pub globals: v1::Globals,
    pub doctor: &'a Doctor,
    /// `init`, the daemon's (step 10c U6).
    pub initializing: &'a dyn Initializing,
    /// Whether a person is there, and her answer.
    pub terminal: &'a dyn Terminal,
    /// The interactive sessions the interview launches.
    pub sessions: &'a dyn Sessions,
    /// The setup interview, read through the project's catalog (owner
    /// ruling 128).
    pub prompts: &'a dyn Prompts,
}

fn failure(cause: impl Diagnostic + 'static, exit: Exit) -> Failure {
    Failure::new(cause, exit)
}

fn output(error: std::io::Error) -> Failure {
    failure(
        CliFault::Output {
            ext_err_txt: error.to_string(),
        },
        Exit::FAILED,
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
) -> Result<Exit, Failure> {
    let format = call.explicit_format.unwrap_or(call.default_format);
    let sub = call.matches.subcommand_matches(noun).ok_or_else(|| {
        failure(
            CliFault::NoSuchCommand {
                noun: noun.to_string(),
            },
            Exit::REFUSED,
        )
    })?;
    match noun {
        "auth" => auth(uses, format, catalog, out, err),
        "clean" => clean(uses, sub, format, catalog, out, err),
        "doctor" => doctor(uses, sub, format, catalog, out, err),
        _ => init(uses, sub, format, catalog, out, err),
    }
}

/// The daemon's notices, said as the use case's own were.
fn say_v1(notices: &[v1::Notice], format: Format, catalog: &Catalog, err: &mut dyn Write) {
    let said: Vec<_> = notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, format, catalog, err);
}

/// `auth` and `auth status`: the credential report, the daemon's.
fn auth(
    uses: &UseCases<'_>,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let answered = uses.reading.auth_status(v1::GetAuthStatusRequest {
        caller: v1::Caller::of(Some(uses.globals.clone()), None),
        ..v1::GetAuthStatusRequest::default()
    })?;
    say_v1(&answered.notices, format, catalog, err);
    let rows = wire::rows_of(&answered.rows);
    match format {
        Format::Json => write_json(out, &rows),
        Format::Text => text::auth(&rows, out),
    }
    .map_err(output)?;
    Ok(Exit::OK)
}

fn clean(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let yes = sub.get_flag("yes");
    let answered = uses.cleaning.clean(v1::CleanRequest {
        validate_only: !yes,
        ..v1::CleanRequest::default()
    })?;
    let said: Vec<_> = answered
        .notices
        .iter()
        .map(library_api::reads::said)
        .collect();
    say_notices(&said, PROG, format, catalog, err);
    let report = report_of(&answered);
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
        // Ruling 153: a preview with a target present that `--yes` would
        // remove.
        if report.targets.iter().any(|target| target.present) {
            return Ok(Exit::WOULD_CHANGE);
        }
    }
    Ok(Exit::OK)
}

fn doctor(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if sub.get_flag("fix") {
        let answered = uses.fixing.fix(v1::RunDoctorRequest {
            fix: true,
            deps: sub.get_flag("deps"),
            all: sub.get_flag("all"),
            caller: v1::Caller::of(Some(uses.globals.clone()), None),
            ..v1::RunDoctorRequest::default()
        })?;
        for r in &answered.removed {
            let _ = writeln!(
                err,
                "removed stale hook entry: {} runs `ctxloom hook {}` in {}",
                r.event, r.sub, r.file
            );
        }
    }
    // The checks, the daemon's (step 10c U5): diagnosed where the caller
    // stands, never in process.
    let answered = uses.reading.doctor(v1::RunDoctorRequest {
        deps: sub.get_flag("deps"),
        all: sub.get_flag("all"),
        caller: v1::Caller::of(Some(uses.globals.clone()), None),
        ..v1::RunDoctorRequest::default()
    })?;
    say_v1(&answered.notices, format, catalog, err);
    let report = wire::report(&answered.checks);
    match format {
        Format::Json => write_json(out, &report),
        Format::Text => text::doctor(&report, sub.get_flag("all"), out),
    }
    .map_err(output)?;
    // A failing row ends the run with run's own status for a fatal finding,
    // after the whole report is written.
    Ok(exit_of(&report))
}

/// The exit a report ends the run with: refused (20) when a row failed, the
/// status of the run's refusal that row guarantees (ruling 152: 21 is the run
/// plan's fatal findings alone).
fn exit_of(report: &doctor_checks::Report) -> Exit {
    if report.failed() {
        Exit::REFUSED
    } else {
        Exit::OK
    }
}

/// What init says when no token is exported, from claude's own docs:
/// `claude setup-token` makes a one-year token for a Pro, Max, Team or
/// Enterprise plan.
fn token_guide() -> CliFault {
    CliFault::TokenNotExported
}

/// The names the `--remote` repositories are registered under.
fn personal_name(index: usize) -> String {
    match index {
        0 => "personal".to_string(),
        n => format!("personal-{}", n + 1),
    }
}

/// Each of init's needs put at the terminal, in order, and answered as the
/// person answered (step 10c U6): an `Ask` with what she typed (none typed,
/// empty: its suggestion stands), a `Choose` with one of its words (asked
/// again, at most [`init_application::PREMISED_TRIES`] times, while her
/// answer is neither; then, or for a bare Enter or a closed input, the
/// first), a `Confirm` with her yes or no.
fn answered(terminal: &dyn Terminal, needs: &[v1::Need]) -> Vec<v1::Answer> {
    use v1::answer::Answer as Given;
    use v1::need::Kind;
    needs
        .iter()
        .filter_map(|need| {
            let given = match need.kind.as_ref()? {
                Kind::Ask(ask) => Given::Text(
                    terminal
                        .ask(&ask.text, &ask.suggestion)
                        .unwrap_or_default()
                        .trim()
                        .to_string(),
                ),
                Kind::Choose(choose) => Given::Chosen(chosen(terminal, choose)),
                Kind::Confirm(confirm) => Given::Confirmed(terminal.confirm(&confirm.text)),
                Kind::Edit(_) => return None,
            };
            Some(v1::Answer {
                need_id: need.id.clone(),
                answer: Some(given),
            })
        })
        .collect()
}

/// A `Choose` asked as [`answered`] asks it.
fn chosen(terminal: &dyn Terminal, choose: &v1::Choose) -> String {
    let first = choose.choices.first().cloned().unwrap_or_default();
    for _ in 0..init_application::PREMISED_TRIES {
        let typed = terminal.ask(&choose.text, &first);
        match typed.map(|line| line.trim().to_lowercase()).as_deref() {
            None | Some("") => break,
            Some(word) if choose.choices.iter().any(|c| c == word) => return word.to_string(),
            Some(_) => {}
        }
    }
    first
}

/// Each `--remote` as the apply registers it (owner ruling 2026-11-19: init
/// answers one message), named `personal` and then numbered.
fn remotes_of(sub: &ArgMatches) -> Vec<v1::InitRemote> {
    let forge = sub.get_one::<String>("forge").cloned().unwrap_or_default();
    sub.get_many::<String>("remote")
        .map(|urls| {
            urls.enumerate()
                .map(|(index, url)| v1::InitRemote {
                    remote_id: personal_name(index),
                    url: url.clone(),
                    forge: forge.clone(),
                })
                .collect()
        })
        .unwrap_or_default()
}

/// What the apply answered of the companions and the remotes, said in
/// text: each companion added, each one that was not (a warning), each
/// remote added, each one that would not register (a warning); init goes on.
fn say_registered(
    done: &v1::InitProjectResponse,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<(), Failure> {
    for companion in &done.companions {
        let name = &companion.name;
        match companion.outcome.as_str() {
            "added" => writeln!(out, "Added companion \"{name}\""),
            "already" => writeln!(out, "Companion \"{name}\" is already registered"),
            "missing" => writeln!(
                err,
                "{PROG}: could not add companion \"{name}\": its program {} is not on this machine",
                companion.program
            ),
            _ => writeln!(
                err,
                "{PROG}: could not add companion \"{name}\": {}",
                companion.error.as_ref().map_or("", |e| e.text.as_str())
            ),
        }
        .map_err(output)?;
    }
    for remote in &done.remotes {
        say_v1(&remote.notices, Format::Text, catalog, err);
        if remote.registered {
            writeln!(out, "Added remote \"{}\": {}", remote.remote_id, remote.url)
                .map_err(output)?;
        } else {
            let why = Notice::text(format!(
                "failed to add remote \"{}\" ({}): {}",
                remote.remote_id,
                remote.url,
                remote.error.as_ref().map_or("", |e| e.text.as_str())
            ));
            say_warning(&why, PROG, Format::Text, catalog, err);
        }
    }
    Ok(())
}

fn init(
    uses: &UseCases<'_>,
    sub: &ArgMatches,
    format: Format,
    catalog: &Catalog,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if sub.subcommand_matches("prompt").is_some() {
        // The daemon's (step 10c U6): read through the caller's catalog
        // under the project's trust. Said with their remedy: a passed-over
        // prompt names `ctxloom trust`.
        let read = uses.reading.init_prompt(v1::GetInitPromptRequest {
            caller: v1::Caller::of(Some(uses.globals.clone()), None),
            ..v1::GetInitPromptRequest::default()
        })?;
        say_v1(&read.notices, format, catalog, err);
        write!(out, "{}", read.text).map_err(output)?;
        return Ok(Exit::OK);
    }
    // The daemon's (step 10c U6): its preview asks; at a terminal she
    // answers, off one nobody does and each question keeps its suggestion
    // unwritten; then the apply creates the project whole.
    let at_terminal = uses.terminal.is_terminal();
    let preview = uses.initializing.init_project(v1::InitProjectRequest {
        validate_only: true,
        caller: v1::Caller::of(
            Some(uses.globals.clone()),
            Some(v1::Presentable {
                can_ask: at_terminal,
                can_launch: at_terminal,
            }),
        ),
        ..v1::InitProjectRequest::default()
    })?;
    let answers = if at_terminal {
        answered(uses.terminal, &preview.needs)
    } else {
        Vec::new()
    };
    let mut done = uses.initializing.init_project(v1::InitProjectRequest {
        etag: preview.etag.clone(),
        answers,
        remotes: remotes_of(sub),
        caller: v1::Caller::of(Some(uses.globals.clone()), None),
        ..v1::InitProjectRequest::default()
    })?;
    say_v1(&done.notices, format, catalog, err);
    uses.initializing.bind_project(&done.project_id)?;
    if format == Format::Json {
        // ONE message (ruling 146): what the apply answered, its notices
        // (and each remote's) said, so not in it.
        for remote in &mut done.remotes {
            say_v1(&std::mem::take(&mut remote.notices), format, catalog, err);
        }
        done.notices.clear();
        done.etag.clear();
        write_json(out, &done).map_err(output)?;
    } else {
        say_created(&done, out)?;
        say_registered(&done, catalog, out, err)?;
    }
    // Delegated agents authenticate with a token the human mints and
    // exports; init guides her to it, and never blocks on it or asks for it.
    let layers = uses.doctor.layer_texts();
    if !uses.auth.agent_token_exported(&layers) {
        say_notices(&[token_guide()], PROG, format, catalog, err);
    }
    // An engine whose source is a file alone authenticates no child (owner
    // ruling 64): said as its children's runs are refused.
    say_notices(&uses.auth.file_only(&layers), PROG, format, catalog, err);
    // On a terminal, the setup interview, and the offer to restart into the
    // session it configured. Off one, nothing is launched and nothing asked.
    let interviewed = interview(uses.terminal, uses.sessions, uses.prompts)
        .map_err(|launch| Failure::boxed(launch.cause, launch.exit))?;
    for notice in &interviewed.notices {
        say_warning(notice.as_ref(), PROG, Format::Text, catalog, err);
    }
    Ok(match interviewed.outcome {
        Outcome::NotATerminal => Exit::OK,
        Outcome::Declined => {
            if format == Format::Text {
                writeln!(
                    out,
                    "Start your configured session when you are ready: {}",
                    init_application::RESTART_COMMAND
                )
                .map_err(output)?;
            }
            Exit::OK
        }
        Outcome::Interrupted { code } | Outcome::Restarted { code } => {
            Exit::from_child_status(code, None)
        }
    })
}

/// What a text init says of the directory it made or kept.
fn say_created(done: &v1::InitProjectResponse, out: &mut dyn Write) -> Result<(), Failure> {
    if done.existed {
        // A re-run is no refusal: the directory is kept and init goes on, to
        // the remotes it was given, the token guide and the interview.
        writeln!(
            out,
            "ctxloom directory already exists: {} (kept as it is; init goes on)",
            done.app_dir
        )
        .map_err(output)?;
    } else {
        writeln!(out, "Created ctxloom directory: {}", done.app_dir).map_err(output)?;
        if !done.output_dir.is_empty() {
            writeln!(
                out,
                "Session outputs go to {} (not gitignored: they can be committed)",
                done.output_dir
            )
            .map_err(output)?;
        }
        if !done.premised.is_empty() {
            let said = if done.premised == init_application::PREMISED_ALWAYS {
                "Conditional guidance always loads"
            } else {
                "Conditional guidance loads only when it applies"
            };
            writeln!(out, "{said} (config.premised: {})", done.premised).map_err(output)?;
        }
    }
    Ok(())
}

#[cfg(test)]
#[path = "text.test.rs"]
mod text_tests;

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
