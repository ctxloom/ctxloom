//! The remote and dependency commands as a driving adapter (S3): arguments
//! in, answers out, no logic. `remote list|show|create|edit|remove|default|
//! discover`, `deps list|pull|check|upgrade|hold|unhold` and the `bundle`
//! verbs `push` and `move`.
//!
//! The whole command line is parsed by the program's own command line (one
//! behaviour for help, usage errors and the exit ladder); this crate gives it
//! its [`nouns`] and [`bundle_verbs`] and, given the matches of one, [`run`]s
//! it: the arguments become a request of the application, and its answer is
//! written as text, or as json, the proto3 JSON of its `ctxloom.v1` API
//! message (OD-40, B4: `api.rs`, `remote.rs`, `bundle.rs`, `dependency.rs`),
//! with what it noticed said on the error stream.

mod api;
mod bundle;
mod commands;
mod pins;
mod read_back;
mod refresh;
mod text;
mod written;

use cli_support::{Exit, ExitClass};
use daemon_proto::v1;
use std::io::{self, Write};

use clap::ArgMatches;
use cli_support::{flag, one, say_notices};
use deps_application::{
    AppError, Deps, DiscoverRequest, MoveRequest, MoveTarget, Notice, Outcome, PushRequest,
};
use presentation::{Catalog, Format};
use serde::Serialize;

pub use commands::{bundle_verbs, is_bundle_verb, nouns};

/// The program's name, for the warnings.
const PROG: &str = "ctxloom";

pub use cli_support::{Call, Failure};

/// Where a remote's, a dependency's and a bundle's resource names are rooted
/// (OD-40; OQ-2, OQ-3): the project as the daemon's `ResolveProject` names
/// it, `projects/{harp}`, or `users/me` outside a project. Asked only when a
/// json answer prints a name, so a text answer reaches no daemon.
pub trait Names {
    /// The parent of this command's resources.
    fn parent(&self) -> Result<String, Failure>;
}

/// The deps reads the daemon answers (step 10a U2; D10: the CLI is the
/// request and the render): one method per read this CLI asks (the rows of
/// `daemon_proto::library_reads!` it serves, D12), each answering the
/// read's message and the notices it said, or why it was not answered. A
/// read's target is the parent the daemon names this command's project by
/// (`users/me` outside one) followed by `under`.
pub trait Served {
    served_reads!(served_read);

    /// `MoveBundle`: a local `bundle move` into another project, the
    /// daemon's one Move (step 10b U9), its answer with the notices inside
    /// it.
    fn move_bundle(
        &self,
        under: &str,
        request: v1::MoveBundleRequest,
    ) -> Result<v1::MoveBundleResponse, Failure>;

    served_writes!(served_write);
}

/// The writes this CLI asks the daemon (step 10c U4), in the shape of
/// `daemon_proto::library_writes!`'s rows, handed to `$each`.
#[macro_export]
macro_rules! served_writes {
    ($each:ident) => {
        $each! {
            create_remote (CreateRemote) CreateRemoteRequest => CreateRemoteResponse,
            update_remote (UpdateRemote) UpdateRemoteRequest => UpdateRemoteResponse,
            delete_remote (DeleteRemote) DeleteRemoteRequest => DeleteRemoteResponse,
            set_default_remote (SetDefaultRemote) SetDefaultRemoteRequest => SetDefaultRemoteResponse,
            pull_dependencies (PullDependencies) PullDependenciesRequest => PullDependenciesResponse,
            upgrade_dependencies (UpgradeDependencies) UpgradeDependenciesRequest => UpgradeDependenciesResponse,
        }
    };
}

/// [`Served`]'s writes, from [`served_writes!`]: each the request, under
/// the parent the daemon names this command's project by followed by
/// `under`.
macro_rules! served_write {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10c U4).")]
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, Failure>;
        )*
    };
}
use served_write;

/// The reads this CLI asks the daemon, in the shape of
/// `daemon_proto::library_reads!`'s rows, handed to `$each` (the trait's,
/// the wiring's and the tests' implementations of [`Served`]).
#[macro_export]
macro_rules! served_reads {
    ($each:ident) => {
        $each! {
            list_remotes (ListRemotes) ListRemotesRequest.parent => ListRemotesResponse,
            browse_remote (BrowseRemote) BrowseRemoteRequest.name => BrowseRemoteResponse,
            list_dependencies (ListDependencies) ListDependenciesRequest.parent => ListDependenciesResponse,
        }
    };
}

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

/// An application error as the failure it ends the command in.
trait IntoFailure {
    fn into_failure(self) -> Failure;
}

impl IntoFailure for AppError {
    fn into_failure(self) -> Failure {
        let exit = self.exit();
        Failure::new(self, exit)
    }
}

struct Context<'a> {
    deps: &'a Deps,
    names: &'a dyn Names,
    served: &'a dyn Served,
    /// The global flags a served write is planned under (E1).
    globals: Option<v1::Globals>,
    format: Format,
    catalog: &'a Catalog,
}

/// Run `noun` (given the matches of the whole line); `names` is where its
/// json answer's names are rooted, `served` the daemon's reads and writes
/// (a pull and its project refresh the daemon's: step 10c U4), each write
/// planned under `globals`.
#[allow(clippy::too_many_arguments)]
pub fn run<G>(
    deps: &Deps,
    names: &dyn Names,
    (served, globals): (&dyn Served, Option<v1::Globals>),
    noun: &str,
    call: &Call<'_, G>,
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
        deps,
        names,
        served,
        globals,
        format,
        catalog,
    };
    let (_, sub) = call
        .matches
        .subcommand()
        .filter(|(name, _)| *name == noun)
        .ok_or_else(|| AppError::DefaultNeedsName.into_failure())?;
    let (verb, args) = match sub.subcommand() {
        Some((name, matches)) => (name, matches),
        None => ("", sub),
    };
    match noun {
        "remote" => remote(&cx, verb, args, out, err),
        "deps" => dependencies(&cx, verb, args, out, err),
        _ => bundle(&cx, verb, args, out, err),
    }
}

/// The notices, said; then the answer's error or the answer.
fn answer<T>(cx: &Context<'_>, outcome: Outcome<T>, err: &mut dyn Write) -> Result<T, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    outcome.result.map_err(IntoFailure::into_failure)
}

/// A served read's answer (step 10a U2, D10): the notices the read said,
/// said; then why it was not answered, or its message as json, or its
/// text, rendered from the message alone.
fn served<M: Serialize>(
    cx: &Context<'_>,
    read: Result<(M, Vec<v1::Notice>), Failure>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    text: impl FnOnce(&M, &mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    let (message, notices) = read?;
    let said: Vec<_> = notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    emit(cx.format, out, &message, |out| text(&message, out))
}

/// A served write's answer (step 10c U4, D10): the notices it said, said;
/// then its message as json (they are said, so not in it), or its text,
/// rendered from the message alone.
fn written<M: Serialize>(
    cx: &Context<'_>,
    (out, err): (&mut dyn Write, &mut dyn Write),
    notices: &[v1::Notice],
    message: &M,
    text: impl FnOnce(&mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    let said: Vec<_> = notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    emit(cx.format, out, message, text)
}

/// Write `value` as json, or as `text` says; a write that fails is a failure.
fn emit<T: Serialize>(
    format: Format,
    out: &mut dyn Write,
    value: &T,
    text: impl FnOnce(&mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    cli_support::emit_or(format, out, value, text, |e| {
        AppError::File {
            what: "the answer".to_string(),
            detail: e.to_string(),
        }
        .into_failure()
    })
}

/// A converted verb's answer (OD-40): as json, `message`, the view as its
/// API message named under the parent the names give (asked only here, so
/// a text answer reaches no daemon); else `text`.
fn respond<M: Serialize>(
    cx: &Context<'_>,
    out: &mut dyn Write,
    message: impl FnOnce(&str) -> M,
    text: impl FnOnce(&mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    if cx.format == Format::Json {
        let parent = cx.names.parent()?;
        emit(cx.format, out, &message(&parent), text)
    } else {
        emit(cx.format, out, &(), text)
    }
}

/// [`respond`], with a name at each of `names` always printed, `null` where
/// it is unresolved (owner ruling 2026-11-14: "list the name as undefined
/// -- don't outright skip the output"; `daemon_proto::with_unresolved_names`).
fn respond_naming<M: Serialize>(
    cx: &Context<'_>,
    out: &mut dyn Write,
    names: &[&str],
    message: impl FnOnce(&str) -> M,
    text: impl FnOnce(&mut dyn Write) -> io::Result<()>,
) -> Result<(), Failure> {
    if cx.format != Format::Json {
        return emit(cx.format, out, &(), text);
    }
    let parent = cx.names.parent()?;
    let printed = daemon_proto::with_unresolved_names(&message(&parent), names).map_err(|e| {
        AppError::File {
            what: "the answer".to_string(),
            detail: e.to_string(),
        }
        .into_failure()
    })?;
    emit(cx.format, out, &printed, text)
}

fn text_arg(matches: &ArgMatches, name: &str) -> String {
    one(matches, name).unwrap_or_default()
}

fn remote(
    cx: &Context<'_>,
    verb: &str,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    match verb {
        "" | "list" => {
            // The daemon's read (step 10a U2), its text from the message.
            let read = cx
                .served
                .list_remotes("", v1::ListRemotesRequest::default());
            served(cx, read, out, err, |listed, out| {
                text::remote_list(out, &read_back::remotes(listed))
            })?;
        }
        "show" => {
            // The daemon's BrowseRemote (step 10a U2, owner ruling
            // 2026-11-17), its text from the message.
            let under = library_api::names::named_target("remotes", &text_arg(args, "remote"));
            let read = cx
                .served
                .browse_remote(&under, v1::BrowseRemoteRequest::default());
            served(cx, read, out, err, |browsed, out| {
                text::remote_show(out, &read_back::browsed(browsed))
            })?;
        }
        "create" => remote_create(cx, args, (out, err))?,
        "edit" => remote_edit(cx, args, (out, err))?,
        "remove" => return remote_remove(cx, args, (out, err)),
        "default" => remote_default(cx, args, (out, err))?,
        _ => discover(cx, args, out, err)?,
    }
    Ok(Exit::OK)
}

/// `remote create`: the daemon's write (step 10c U4), `remotes.yaml`.
fn remote_create(
    cx: &Context<'_>,
    args: &ArgMatches,
    (out, err): (&mut dyn Write, &mut dyn Write),
) -> Result<(), Failure> {
    // The daemon's write (step 10c U4): `remotes.yaml`, one file.
    let forge = text_arg(args, "forge");
    let request = v1::CreateRemoteRequest {
        remote_id: text_arg(args, "name"),
        remote: Some(v1::Remote {
            url: text_arg(args, "url"),
            forge,
            ..Default::default()
        }),
        caller: v1::Caller::of(cx.globals.clone(), None),
        ..v1::CreateRemoteRequest::default()
    };
    let mut answered = cx.served.create_remote("", request)?;
    let notices = std::mem::take(&mut answered.notices);
    answered.etag.clear();
    let remote = answered.remote.clone().unwrap_or_default();
    written(cx, (out, err), &notices, &answered, |out| {
        writeln!(
            out,
            "Added remote '{}' \u{2192} {}",
            written::remote_id(&remote.name),
            remote.url
        )
    })?;
    Ok(())
}

/// `remote edit`: the daemon's write, its text drawn from the listing
/// before it and the answer after.
fn remote_edit(
    cx: &Context<'_>,
    args: &ArgMatches,
    (out, err): (&mut dyn Write, &mut dyn Write),
) -> Result<(), Failure> {
    let asked = text_arg(args, "remote");
    // What it was, for the text: the daemon's listing.
    let (listed, _) = cx
        .served
        .list_remotes("", v1::ListRemotesRequest::default())?;
    let before = listed
        .remotes
        .iter()
        .find(|remote| written::remote_id(&remote.name) == asked)
        .map(|remote| deps_application::RemoteState {
            name: asked.clone(),
            url: remote.url.clone(),
            forge: remote.forge.clone(),
        });
    // The mask names each flag given, an empty one too (`--forge
    // ""` clears the forge).
    let given: Vec<String> = ["name", "url", "forge"]
        .into_iter()
        .filter(|field| one(args, field).is_some())
        .map(str::to_string)
        .collect();
    let request = v1::UpdateRemoteRequest {
        remote: Some(v1::Remote {
            url: one(args, "url").unwrap_or_default(),
            forge: one(args, "forge").unwrap_or_default(),
            ..Default::default()
        }),
        new_remote_id: one(args, "name").unwrap_or_default(),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask { paths: given }),
        caller: v1::Caller::of(cx.globals.clone(), None),
        ..v1::UpdateRemoteRequest::default()
    };
    let under = library_api::names::named_target("remotes", &asked);
    let mut answered = cx.served.update_remote(&under, request)?;
    let notices = std::mem::take(&mut answered.notices);
    answered.etag.clear();
    let edited = written::edited(&asked, before, &answered);
    written(cx, (out, err), &notices, &answered, |out| {
        text::remote_edit(out, &edited)
    })?;
    Ok(())
}

/// `remote remove`: the daemon's write, previewed without `--yes`.
fn remote_remove(
    cx: &Context<'_>,
    args: &ArgMatches,
    (out, err): (&mut dyn Write, &mut dyn Write),
) -> Result<Exit, Failure> {
    let asked = text_arg(args, "remote");
    let request = v1::DeleteRemoteRequest {
        validate_only: !flag(args, "yes"),
        caller: v1::Caller::of(cx.globals.clone(), None),
        ..v1::DeleteRemoteRequest::default()
    };
    let under = library_api::names::named_target("remotes", &asked);
    let mut answered = cx.served.delete_remote(&under, request)?;
    let notices = std::mem::take(&mut answered.notices);
    answered.etag.clear();
    let removed = written::removed(&asked, &answered);
    written(cx, (out, err), &notices, &answered, |out| {
        text::remote_remove(out, &removed)
    })?;
    // Ruling 153: the preview names a remote `--yes` would remove.
    if matches!(removed, deps_application::RemoveResult::Preview { .. }) {
        return Ok(Exit::WOULD_CHANGE);
    }
    Ok(Exit::OK)
}

/// `remote default`: the daemon's write; `--clear` names none.
fn remote_default(
    cx: &Context<'_>,
    args: &ArgMatches,
    (out, err): (&mut dyn Write, &mut dyn Write),
) -> Result<(), Failure> {
    let name = text_arg(args, "remote");
    let chosen = if flag(args, "clear") {
        None
    } else if name.is_empty() {
        return Err(AppError::DefaultNeedsName.into_failure());
    } else {
        Some(name.as_str())
    };
    let request = v1::SetDefaultRemoteRequest {
        remote: chosen.unwrap_or_default().to_string(),
        caller: v1::Caller::of(cx.globals.clone(), None),
        ..v1::SetDefaultRemoteRequest::default()
    };
    let mut answered = cx.served.set_default_remote("", request)?;
    let notices = std::mem::take(&mut answered.notices);
    answered.etag.clear();
    written(cx, (out, err), &notices, &answered, |out| match chosen {
        Some(name) => writeln!(out, "Set default remote to: {name}"),
        None => writeln!(out, "Cleared default remote."),
    })?;
    Ok(())
}

/// `remote discover`: the progress line is the stream's, closed before a
/// warning or an error is said after it.
fn discover(
    cx: &Context<'_>,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<(), Failure> {
    let query = args
        .get_many::<String>("query")
        .map(|words| words.cloned().collect::<Vec<_>>().join(" "))
        .unwrap_or_default();
    let request = DiscoverRequest {
        query: query.clone(),
        source: text_arg(args, "source"),
        limit: args.get_one::<usize>("limit").copied().unwrap_or(30),
        min_stars: args.get_one::<u64>("stars").copied().unwrap_or(0),
    };
    let _ = write!(err, "Searching repositories...");
    let outcome = cx.deps.remote_discover(&request);
    let _ = match &outcome.result {
        Ok(found) => writeln!(err, " found {}", found.count),
        Err(_) => writeln!(err),
    };
    let found = answer(cx, outcome, err)?;
    respond(
        cx,
        out,
        |_| library_api::deps_wire::remote::discovered(&found),
        |out| text::discovered(out, &found, &query),
    )
}

fn dependencies(
    cx: &Context<'_>,
    verb: &str,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let deps = cx.deps;
    match verb {
        "" | "list" => {
            // The daemon's read (step 10a U2), its text from the message.
            let read = cx
                .served
                .list_dependencies("", v1::ListDependenciesRequest::default());
            served(cx, read, out, err, |listed, out| {
                text::deps_list(out, &read_back::dependencies(listed))
            })?;
        }
        "pull" => {
            // The daemon's sequence (step 10c U4): whole trees, then the lock.
            let _ = writeln!(err, "Pulling dependencies...");
            let lock = args.get_one::<bool>("lock").copied().unwrap_or(true);
            let request = v1::PullDependenciesRequest {
                force: flag(args, "force"),
                lock: Some(lock),
                validate_only: !flag(args, "yes"),
                caller: v1::Caller::of(cx.globals.clone(), None),
                ..v1::PullDependenciesRequest::default()
            };
            let mut answered = cx.served.pull_dependencies("", request)?;
            let notices = std::mem::take(&mut answered.notices);
            let refused = answered.refresh_refusal.take();
            let report = written::pulled(&answered);
            let skipped = answered
                .skipped_refresh
                .as_ref()
                .map(library_api::deps_wire::refresh::from_message);
            written(cx, (out, err), &notices, &answered, |out| {
                text::pull(out, &report)?;
                skipped.as_ref().map_or(Ok(()), |skipped| {
                    refresh::text(out, skipped, "ctxloom deps pull --yes")
                })
            })?;
            if let Some(failure) = report.failure() {
                return Err(failure.into_failure());
            }
            // A consented refresh refused once the pull applied: a decline.
            if let Some(refused) = refused {
                return Err(Failure::new(
                    library_api::reads::reported(&refused),
                    Exit::REFUSED,
                ));
            }
        }
        "check" => return check(cx, args, out, err),
        "upgrade" => {
            let _ = writeln!(err, "Resolving latest commits for pinned dependencies...");
            let reference = args.get_one::<String>("reference");
            let request = v1::UpgradeDependenciesRequest {
                reference: reference.cloned().unwrap_or_default(),
                validate_only: !flag(args, "yes"),
                caller: v1::Caller::of(cx.globals.clone(), None),
                ..v1::UpgradeDependenciesRequest::default()
            };
            let mut answered = cx.served.upgrade_dependencies("", request)?;
            let notices = std::mem::take(&mut answered.notices);
            let report = written::upgraded(&answered);
            written(cx, (out, err), &notices, &answered, |out| {
                text::upgrade(out, &report)
            })?;
            // Ruling 153: a preview with a pin `--yes` would move or drop.
            let moves = !report.changes.is_empty() || !report.removed.is_empty();
            if moves && !report.applied {
                return Ok(Exit::WOULD_CHANGE);
            }
        }
        verb => {
            let held = verb == "hold" && !flag(args, "undo");
            let result = answer(cx, deps.hold(&text_arg(args, "name"), held), err)?;
            // A local bundle is no dependency: its name is null (owner
            // 2026-11-14).
            respond_naming(
                cx,
                out,
                &["name"],
                |parent| {
                    // The Dependency after the write, read from the lock.
                    let listing = deps.list().result.unwrap_or(deps_application::DepsListing {
                        deps: Vec::new(),
                        count: 0,
                    });
                    library_api::deps_wire::dependency::held(parent, &result, &listing)
                },
                |out| text::hold(out, &result),
            )?;
        }
    }
    Ok(Exit::OK)
}

fn check(
    cx: &Context<'_>,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let reference = text_arg(args, "reference");
    if !reference.is_empty() {
        let checked = answer(cx, cx.deps.check_reference(&reference), err)?;
        respond(
            cx,
            out,
            |parent| library_api::deps_wire::dependency::checked_one(parent, &checked),
            |out| text::check_reference(out, &checked),
        )?;
        return Ok(Exit::OK);
    }
    let report = answer(cx, cx.deps.check(), err)?;
    if report.entries > 0 {
        let _ = writeln!(err, "Checking {} items for updates...", report.entries);
        let warnings: Vec<Notice> = report
            .refresh_failures
            .iter()
            .map(|f| Notice::text(format!("fetch {}: {}", f.url, f.error)))
            .chain(report.unchecked.iter().filter_map(unchecked_warning))
            .collect();
        say_notices(&warnings, PROG, cx.format, cx.catalog, err);
    }
    respond(
        cx,
        out,
        |parent| library_api::deps_wire::dependency::checked(parent, &report),
        |out| text::check(out, &report),
    )?;
    Ok(Exit::OK)
}

/// The warning an entry that failed to be checked earns (a fact about the
/// entry is in the report instead).
fn unchecked_warning(row: &deps_application::UncheckedRow) -> Option<Notice> {
    let text = match row.reason {
        "unparseable" => format!(
            "{}: could not parse reference ({}); skipping the update check for it",
            row.reference, row.error
        ),
        "unresolvable" => format!(
            "{}: could not resolve {:?} ({}); skipping the update check for it",
            row.reference, row.constraint, row.error
        ),
        _ => return None,
    };
    Some(Notice::text(text))
}

fn bundle(
    cx: &Context<'_>,
    verb: &str,
    args: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "push" {
        let name = text_arg(args, "bundle");
        let pushed = answer(
            cx,
            cx.deps.push(&PushRequest {
                bundle: name.clone(),
                remote: text_arg(args, "remote"),
                message: text_arg(args, "message"),
                pr: flag(args, "pr"),
            }),
            err,
        )?;
        respond(
            cx,
            out,
            |parent| bundle::pushed(parent, &name, &pushed),
            |out| text::pushed(out, &pushed),
        )?;
    } else {
        let name = text_arg(args, "name");
        let to = text_arg(args, "to");
        let target = cx
            .deps
            .move_target(&to)
            .map_err(IntoFailure::into_failure)?;
        let moved = if target == MoveTarget::Remote {
            answer(
                cx,
                cx.deps.move_bundle(&MoveRequest {
                    name,
                    to,
                    force: flag(args, "force"),
                    message: text_arg(args, "message"),
                }),
                err,
            )?
        } else {
            moved_locally(cx, &name, &to, flag(args, "force"), err)?
        };
        // Into another project's directory the bundle's name is that
        // project's, which this one cannot give: null (owner 2026-11-14).
        respond_naming(
            cx,
            out,
            &["bundle.name"],
            |parent| bundle::moved(parent, &moved),
            |out| text::moved(out, &moved),
        )?;
    }
    Ok(Exit::OK)
}

/// A local `bundle move <name> --to <dir>`: the daemon's `MoveBundle`
/// (step 10b U9, QUESTIONS 1(a): one Move out of this project's tree, the
/// other's next read recording the arrival), its notices said, its answer
/// as the move it reports (the same text and json as ever).
fn moved_locally(
    cx: &Context<'_>,
    name: &str,
    to: &str,
    force: bool,
    err: &mut dyn Write,
) -> Result<deps_application::Moved, Failure> {
    let request = v1::MoveBundleRequest {
        destination: to.to_string(),
        force,
        ..v1::MoveBundleRequest::default()
    };
    let under = library_api::names::bundle_target(name);
    let answered = cx.served.move_bundle(&under, request)?;
    let said: Vec<_> = answered
        .notices
        .iter()
        .map(library_api::reads::said)
        .collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(deps_application::Moved {
        status: "moved",
        name: name.to_string(),
        source: answered.source,
        dest_kind: "path",
        dest: answered.path,
        remote: String::new(),
        commit_sha: String::new(),
    })
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
