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
mod read_back;
mod text;
mod write;

/// What every noun's module uses: the shared helpers here, in `write` and
/// in `api`, and the types they speak.
mod prelude {
    pub(crate) use std::io::{self, Write};

    pub(crate) use angzarr_presentation::Format;
    pub(crate) use clap::ArgMatches;
    pub(crate) use cli_support::{flag, many, say_notices, Exit};
    pub(crate) use library_application::{
        AgentRow, AppError, DefaultAgent, ItemKind, SwapPreview, WriteError,
    };

    pub(crate) use crate::write::{
        finish_text, given, given_list, given_map, given_pairs, nothing, text_only,
    };
    pub(crate) use crate::{
        api, output_failure, served, text, text_arg, verb, Context, Failure, IntoFailure, PROG,
    };
    pub(crate) use daemon_proto::v1;
}

use cli_support::{Exit, ExitClass};
use std::io::{self, Write};

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{Catalog, Format};
use clap::ArgMatches;
use cli_support::{one, say_notices};
use daemon_proto::v1;
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
/// answer), so a text answer reaches no daemon; `run --dry-run` alone asks
/// [`Names::named_parent`] always, text too (owner ruling 2026-11-14).
pub trait Names {
    /// The parent of this command's resources.
    fn parent(&self) -> Result<String, Failure>;

    /// The parent, or none where this command's project is a ctxloom
    /// project nothing has named and the command may not name it (a dry run
    /// never mints, owner ruling 2026-11-14): its names are then unresolved.
    /// The wiring's reaches the daemon whatever it answers, starting it if
    /// none serves (every dry run calls the daemon, owner ruling 2026-11-14).
    fn named_parent(&self) -> Result<Option<String>, Failure> {
        self.parent().map(Some)
    }
}

/// The library reads the daemon answers (step 10a U2; D10: the CLI is the
/// request and the render): one method per read this CLI asks (the rows of
/// `daemon_proto::library_reads!` it serves, D12), each answering the read's message
/// and the notices it said (the daemon's `NOTICE_METADATA`), or why it was
/// not answered. A read's target is the parent the daemon names this
/// command's project by (`users/me` outside one) followed by `under`
/// (`library_api::names`' targets).
pub trait Served {
    /// Whether this command is in a project (else its reads are the
    /// home's, `users/me`).
    fn in_project(&self) -> bool;

    served_reads!(served_read);

    /// `context assemble`: `AssembleContext`, its notices among the
    /// message's own.
    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Failure>;

    /// `run --dry-run`: `CreateRun` with `validate_only`, its notices among
    /// the run's own; a project nothing has named is asked unnamed (a dry
    /// run never mints, owner ruling 2026-11-14).
    fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, Failure>;

    served_writes!(served_write);

    /// A write's Edit need presented (step 10b U3): the editor its launch
    /// names, on the user's terminal, opened on the document's text; what
    /// it left. A need with no launch cannot be presented here.
    fn edit_document(&self, edit: &v1::Edit) -> Result<String, Failure>;

    /// Whether this command is at a terminal: its standard input and output
    /// are one (as it asks a person), or a test stands one in. An editor
    /// needs the controlling terminal, so this is what `can_launch` says.
    fn at_terminal(&self) -> bool;

    /// A write's Confirm need presented (step 10b U3): `question` asked of
    /// the person at this command's terminal, `[y/N]`; none when there is
    /// no terminal to ask at (the preview is then printed and nothing is
    /// applied).
    fn confirm(&self, question: &str) -> Option<bool> {
        let _ = question;
        None
    }

    /// A run the daemon answered a call with (a distill's model, step 10b:
    /// the daemon owns the model call as a run, and what follows from it is
    /// made once it ends), followed to its end through `WatchRun`, a signal
    /// heard here asking the daemon to stop it, as a one-shot's: what it
    /// said, and how it ended.
    fn follow_run(&self, run: &str) -> RunFollowed;

    /// Hear the signals that stop a run from now on, for the rest of the
    /// command (ruling 114): asked before a call whose answer is a run to
    /// follow (a distill's), so a stop that arrives before the follow
    /// begins is heard, not taken by its default, and the follow asks the
    /// daemon to stop the run. Asking again changes nothing.
    fn hear_stops(&self);

    /// `loadout`: `GetLoadout`, always asked as `users/me/loadout`, its
    /// notices beside it: ctxloom's own loadout is the same in every
    /// project, and a companion probe asks it where there may be no
    /// project, no git and no identity, so no project is resolved, named
    /// or observed for it.
    fn own_loadout(
        &self,
        request: v1::GetLoadoutRequest,
    ) -> Result<(v1::Loadout, Vec<v1::Notice>), Failure>;
}

/// The reads this CLI asks the daemon, in the shape of
/// `daemon_proto::library_reads!`'s rows, handed to `$each` (the trait's,
/// the wiring's and the tests' implementations of [`Served`]).
#[macro_export]
macro_rules! served_reads {
    ($each:ident) => {
        $each! {
            list_bundles (ListBundles) ListBundlesRequest.parent => ListBundlesResponse,
            list_fragments (ListFragments) ListFragmentsRequest.parent => ListFragmentsResponse,
            list_commands (ListCommands) ListCommandsRequest.parent => ListCommandsResponse,
            get_fragment (GetFragment) GetFragmentRequest.name => Fragment,
            get_command (GetCommand) GetCommandRequest.name => Command,
            get_premise_index (GetPremiseIndex) GetPremiseIndexRequest.name => PremiseIndex,
            list_skills (ListSkills) ListSkillsRequest.parent => ListSkillsResponse,
            get_skill (GetSkill) GetSkillRequest.name => Skill,
            list_companions (ListCompanions) ListCompanionsRequest.parent => ListCompanionsResponse,
            list_profiles (ListProfiles) ListProfilesRequest.parent => ListProfilesResponse,
            get_profile (GetProfile) GetProfileRequest.name => Profile,
            list_agents (ListAgents) ListAgentsRequest.parent => ListAgentsResponse,
            get_agent (GetAgent) GetAgentRequest.name => Agent,
            list_engines (ListEngines) ListEnginesRequest.parent => ListEnginesResponse,
            get_engine (GetEngine) GetEngineRequest.name => Engine,
            list_mcp_servers (ListMcpServers) ListMcpServersRequest.parent => ListMcpServersResponse,
            get_mcp_server (GetMcpServer) GetMcpServerRequest.name => McpServer,
            search_library (SearchLibrary) SearchLibraryRequest.parent => SearchLibraryResponse,
            get_config (GetConfig) GetConfigRequest.name => Config,
            get_bundle (GetBundle) GetBundleRequest.name => Bundle,
        }
    };
}

/// What a run a call was answered with came to ([`Served::follow_run`]):
/// what it said on the way, and its end, a failure when it ended in one.
pub struct RunFollowed {
    pub notices: Vec<v1::Notice>,
    pub ended: Result<(), Failure>,
}

/// The writes this CLI asks the daemon (step 10b U3), in the shape of
/// `daemon_proto::library_writes!`'s rows, handed to `$each` (the trait's,
/// the wiring's and the tests' implementations of [`Served`]).
#[macro_export]
macro_rules! served_writes {
    ($each:ident) => {
        $each! {
            create_bundle (CreateBundle) CreateBundleRequest => CreateBundleResponse,
            update_bundle (UpdateBundle) UpdateBundleRequest => UpdateBundleResponse,
            create_fragment (CreateFragment) CreateFragmentRequest => CreateFragmentResponse,
            update_fragment (UpdateFragment) UpdateFragmentRequest => UpdateFragmentResponse,
            create_command (CreateCommand) CreateCommandRequest => CreateCommandResponse,
            update_command (UpdateCommand) UpdateCommandRequest => UpdateCommandResponse,
            update_config (UpdateConfig) UpdateConfigRequest => UpdateConfigResponse,
            edit_config (EditConfig) EditConfigRequest => EditConfigResponse,
            create_skill (CreateSkill) CreateSkillRequest => CreateSkillResponse,
            update_mcp_server (UpdateMcpServer) UpdateMcpServerRequest => UpdateMcpServerResponse,
            create_profile (CreateProfile) CreateProfileRequest => CreateProfileResponse,
            update_profile (UpdateProfile) UpdateProfileRequest => UpdateProfileResponse,
            create_companion (CreateCompanion) CreateCompanionRequest => CreateCompanionResponse,
            enable_companion (EnableCompanion) EnableCompanionRequest => EnableCompanionResponse,
            disable_companion (DisableCompanion) DisableCompanionRequest => DisableCompanionResponse,
            set_default_agent (SetDefaultAgent) SetDefaultAgentRequest => SetDefaultAgentResponse,
            create_agent (CreateAgent) CreateAgentRequest => CreateAgentResponse,
            update_agent (UpdateAgent) UpdateAgentRequest => UpdateAgentResponse,
            export_bundle (ExportBundle) ExportBundleRequest => ExportBundleResponse,
            move_bundle (MoveBundle) MoveBundleRequest => MoveBundleResponse,
            import_bundle (ImportBundle) ImportBundleRequest => ImportBundleResponse,
            delete_bundle (DeleteBundle) DeleteBundleRequest => DeleteBundleResponse,
            delete_fragment (DeleteFragment) DeleteFragmentRequest => DeleteFragmentResponse,
            delete_command (DeleteCommand) DeleteCommandRequest => DeleteCommandResponse,
            delete_skill (DeleteSkill) DeleteSkillRequest => DeleteSkillResponse,
            delete_profile (DeleteProfile) DeleteProfileRequest => DeleteProfileResponse,
            delete_agent (DeleteAgent) DeleteAgentRequest => DeleteAgentResponse,
            delete_companion (DeleteCompanion) DeleteCompanionRequest => DeleteCompanionResponse,
            distill_fragment (DistillFragment) DistillFragmentRequest => DistillFragmentResponse,
            distill_command (DistillCommand) DistillCommandRequest => DistillCommandResponse,
        }
    };
}

/// [`Served`]'s write methods, from [`served_writes!`]: each answering the
/// write's response, its notices inside it, or why it was not answered.
macro_rules! served_write {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10b U3).")]
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, Failure>;
        )*
    };
}
use served_write;

/// [`Served`]'s methods, from [`served_reads!`].
macro_rules! served_read {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "`.")]
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<(v1::$response, Vec<v1::Notice>), Failure>;
        )*
    };
}
use served_read;

/// Everything a command needs to answer.
pub(crate) struct Context<'a> {
    library: &'a Library,
    names: &'a dyn Names,
    served: &'a dyn Served,
    flags: Flags,
    format: Format,
    explicit: bool,
    catalog: &'a Catalog,
}

impl Context<'_> {
    /// This command's global flags as a read's request carries them (E1).
    pub(crate) fn globals(&self) -> Option<v1::Globals> {
        Some(library_api::reads::globals_of(&self.flags))
    }

    /// What this command can present of a need (step 10b U3): it launches
    /// the editor, and asks nothing else here, and only at a terminal (the
    /// Caller ruling, kept 2026-11-19: an editor needs the controlling
    /// terminal, [`Served::at_terminal`]). Off one the preview carries no
    /// launch and no editor is run (`item::edit_answers`).
    pub(crate) fn presentable(&self) -> Option<v1::Presentable> {
        Some(v1::Presentable {
            can_ask: false,
            can_launch: self.served.at_terminal(),
        })
    }

    /// This context answering in `format` (a verb that answers in text
    /// unless json is asked for by name).
    pub(crate) fn formatted(&self, format: Format) -> Context<'_> {
        Context {
            library: self.library,
            names: self.names,
            served: self.served,
            flags: self.flags.clone(),
            format,
            explicit: self.explicit,
            catalog: self.catalog,
        }
    }
}

/// A served read's answer (step 10a U2, D10: the CLI is the request and
/// the render): the notices the read said, said; then why it was not
/// answered, or its message as json (`explicit_json`: only when json is
/// asked for by name, a verb that answers in text otherwise), or its text,
/// rendered from the message alone, with the notes the text adds said
/// after it.
pub(crate) fn served<M: Serialize>(
    cx: &Context<'_>,
    read: Result<(M, Vec<v1::Notice>), Failure>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    explicit_json: bool,
    text: impl FnOnce(&M, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
) -> Result<Exit, Failure> {
    let (message, notices) = read?;
    let said: Vec<Notice> = notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    let json = cx.format == Format::Json && (cx.explicit || !explicit_json);
    let mut notes = Vec::new();
    if json {
        api::print(out, &message).map_err(output_failure)?;
    } else {
        text(&message, out, &mut notes).map_err(output_failure)?;
    }
    let said: Vec<Notice> = notes.into_iter().map(Notice::text).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(Exit::OK)
}

/// A served write's answer (step 10b U3, D10): the notices inside its
/// response, said; then why it was not answered, or its message as json
/// (without the notices, which were said), or its text, rendered from the
/// message alone; its exit `exit`'s.
pub(crate) fn answer_written<M: Serialize>(
    cx: &Context<'_>,
    written: Result<M, Failure>,
    notices: impl FnOnce(&mut M) -> Vec<v1::Notice>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    text: impl FnOnce(&M, &mut dyn Write) -> io::Result<()>,
    exit: impl FnOnce(&M) -> Exit,
) -> Result<Exit, Failure> {
    let mut message = written?;
    let said: Vec<Notice> = notices(&mut message)
        .iter()
        .map(library_api::reads::said)
        .collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    if cx.format == Format::Json {
        api::print(out, &message).map_err(output_failure)?;
    } else {
        text(&message, out).map_err(output_failure)?;
    }
    Ok(exit(&message))
}

/// Run `noun` (given the matches of the whole line).
#[allow(clippy::too_many_arguments)]
pub fn run(
    library: &Library,
    names: &dyn Names,
    served: &dyn Served,
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
        served,
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

/// The verbs, by noun, whose answer is the daemon's read (step 10a U2):
/// the daemon takes the home step before it reads, so the caller takes
/// none of its own; every other verb runs in process. A noun's bare form
/// is its default verb's ([`is_served_read`]).
pub const SERVED_VERBS: &[(&str, &[&str])] = &[
    ("config", &["show"]),
    ("bundle", &["list", "show", "view"]),
    ("fragment", &["list", "show", "premises"]),
    ("command", &["list", "show"]),
    ("skill", &["list", "show"]),
    ("profile", &["list", "show"]),
    ("agent", &["list", "show"]),
    ("engine", &["list", "show"]),
    ("companion", &["list"]),
    ("context", &["assemble", "refresh"]),
];

/// Whether `noun`, given its matches `sub`, is a read the daemon answers
/// (step 10a U2): a verb of [`SERVED_VERBS`] (a bare noun its default's),
/// `mcp server list|show`, `search`, `loadout`, and `run --dry-run`.
pub fn is_served_read(noun: &str, sub: &ArgMatches) -> bool {
    match noun {
        "mcp" => matches!(
            sub.subcommand(),
            Some(("server", server)) if matches!(verb(server, "list").0, "list" | "show")
        ),
        "search" | "loadout" => true,
        "run" => cli_support::flag(sub, "dry-run"),
        _ => {
            let default = match noun {
                "config" => "show",
                "context" => "assemble",
                _ => "list",
            };
            let (asked, _) = verb(sub, default);
            SERVED_VERBS
                .iter()
                .any(|(served, verbs)| *served == noun && verbs.contains(&asked))
        }
    }
}

/// The write verbs, by noun, the daemon serves (step 10b U3): it takes the
/// home step and observes the trees before it plans, so the caller takes
/// none of its own.
pub const SERVED_WRITES: &[(&str, &[&str])] = &[
    (
        "bundle",
        &["create", "edit", "remove", "import", "export", "distill"],
    ),
    ("fragment", &["create", "edit", "remove", "distill"]),
    ("command", &["create", "edit", "remove", "distill"]),
    ("config", &["set", "edit"]),
    ("skill", &["create", "remove"]),
    ("profile", &["create", "edit", "modify", "remove"]),
    ("agent", &["create", "edit", "remove"]),
    ("companion", &["add", "enable", "disable", "remove"]),
    ("mcp server", &["set", "edit"]),
];

/// Whether `noun`, given its matches `sub`, is a write the daemon serves
/// (a verb of [`SERVED_WRITES`]; `mcp`'s are its `server`'s, the table's
/// `mcp server`).
pub fn is_served_write(noun: &str, sub: &ArgMatches) -> bool {
    let Some((asked, matches)) = sub.subcommand() else {
        return false;
    };
    let (noun, asked) = match (noun, asked) {
        ("mcp", "server") => match matches.subcommand() {
            Some((verb, _)) => ("mcp server", verb),
            None => return false,
        },
        other => other,
    };
    SERVED_WRITES
        .iter()
        .any(|(served, verbs)| *served == noun && verbs.contains(&asked))
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
        "loadout" => noun::companion::loadout(cx, sub, out, err),
        "search" => noun::search::read(cx, sub, out, err),
        "context" => match verb(sub, "assemble") {
            ("refresh", _) => noun::context::refresh(cx, out, err),
            (_, sub) => noun::context::read(cx, sub, out, err),
        },
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

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
