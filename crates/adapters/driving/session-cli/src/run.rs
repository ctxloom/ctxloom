//! Run a `session` command against the application.

use cli_support::{Exit, ExitClass};
use std::io::{self, Write};

use coded_diagnostic::Diagnostic;
#[cfg(feature = "session-destroy")]
use std::time::{SystemTime, UNIX_EPOCH};

use daemon_api::{CallError, ChildWrites, Launch, SessionReads, Written};
#[cfg(feature = "session-destroy")]
use daemon_api::{Removed, SessionRemovals};
use daemon_proto::v1;
use isolation_application::{IsolationError, WorktreeUse};
use presentation::{write_json, Format};
#[cfg(feature = "session-destroy")]
use reclaim_application::{destroys_held, ReclaimError, SessionHolds};
#[cfg(feature = "session-destroy")]
use sessions_application::Timestamp;
#[cfg(feature = "session-destroy")]
use sessions_application::{DestroysAuthored, DestroysHeld, REMOVE_VERB};
use sessions_application::{RemovedNothing, State, TranscriptRow, Urgency, STATES};

use crate::render::{
    conversation_text, import_text, listing_text, session_text, transcripts_text, worktrees_text,
    ImportDoc, KeepDoc, TitleDoc, TranscriptsDoc,
};
#[cfg(feature = "session-destroy")]
use crate::{
    render::{removal_of, removal_text, report_of, sweep_text},
    RemoveArgs,
};
use crate::{
    CliFault, ListArgs, Session, SessionVerb, Transcript, TranscriptVerb, UrgencyWord, Worktrees,
    WorktreesVerb,
};

/// A command that completed: what it says on the diagnostic channel besides
/// its answer.
#[derive(Debug, Default)]
pub struct Done {
    /// Notices (a report-only run ends with one), each a warning.
    pub notices: Vec<Box<dyn Diagnostic>>,
    /// Dead letters the kernel answered, loud lines for stderr: each almost
    /// certainly marks a bug.
    pub lines: Vec<String>,
    /// A preview that would change something on `--yes` (ruling 153: the
    /// command's exit is the distinguishing success 17, said only under
    /// `--detailed-exitcode`).
    pub would_change: bool,
}

/// A command that did not complete: why and its exit code (the shared
/// [`cli_support::Failure`]), and the dead-letter lines to say beside it.
#[derive(Debug)]
pub struct Failure {
    pub failure: cli_support::Failure,
    pub lines: Vec<String>,
}

impl Failure {
    /// `cause`, ending the command with its own class's exit.
    pub fn of(cause: impl Diagnostic + ExitClass + 'static) -> Failure {
        let exit = cause.exit();
        Failure::new(cause, exit, Vec::new())
    }

    /// `cause`, ending the command with `exit`, with `lines` to say beside it.
    pub(crate) fn new(cause: impl Diagnostic + 'static, exit: Exit, lines: Vec<String>) -> Failure {
        Failure {
            failure: cli_support::Failure::new(cause, exit),
            lines,
        }
    }
}

/// Whether `session`'s command is one of the reads the daemon answers
/// (`list`, the bare noun, `search`, `show`: slice 2 step 6; `transcript
/// show`, owner's H3 follow-up, so the flush on the way in runs before it
/// reads), which [`read`] runs; a destroy ([`is_destroy`]) is the daemon's
/// too, and every other runs on the sessions use cases ([`run`]).
pub fn is_read(session: &Session) -> bool {
    matches!(
        &session.verb,
        None | Some(SessionVerb::List(_) | SessionVerb::Search(_) | SessionVerb::Show(_))
    ) || matches!(&session.verb, Some(SessionVerb::Transcript(_)))
}

/// Whether `session`'s command is a destroy the daemon runs (`remove`,
/// `sweep`: owner, 2026-11-12), which [`destroy`] runs; none in a plain
/// build, which has neither verb.
pub fn is_destroy(session: &Session) -> bool {
    #[cfg(feature = "session-destroy")]
    {
        matches!(
            session.verb,
            Some(SessionVerb::Remove(_) | SessionVerb::Sweep(_))
        )
    }
    #[cfg(not(feature = "session-destroy"))]
    {
        let _ = session;
        false
    }
}

/// Run `session`'s destroy ([`is_destroy`]) on the daemon's client
/// `removals`, writing its answer to `out`; each session it destroys, or
/// plans to, has its hold in this process's `holds` warned of, and once
/// destroyed discarded (s4-Q14, H4: the hold is the client's).
#[cfg(feature = "session-destroy")]
pub fn destroy<R: SessionRemovals>(
    session: &Session,
    removals: &R,
    holds: &dyn SessionHolds,
    asking: &dyn crate::Asking,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    // Time and I/O: the daemon is called over the network.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    runtime.block_on(async {
        match &session.verb {
            Some(SessionVerb::Remove(args)) => match (&args.older_than, &args.harp) {
                (Some(age), _) => {
                    let ask = Sweep {
                        all_projects: false,
                        age: Some((AGE_FLAG, age)),
                        yes: args.yes,
                        verb: REMOVE_VERB,
                    };
                    sweep(removals, holds, &ask, format, out).await
                }
                (None, Some(harp)) => {
                    remove(removals, (holds, asking), harp, args, format, out).await
                }
                (None, None) => unreachable!("clap requires a session or an age"),
            },
            Some(SessionVerb::Sweep(args)) => {
                let ask = Sweep {
                    all_projects: args.all_projects,
                    age: args
                        .purge_older_than
                        .as_deref()
                        .map(|age| (PURGE_FLAG, age)),
                    yes: args.yes,
                    verb: "session sweep",
                };
                sweep(removals, holds, &ask, format, out).await
            }
            _ => unreachable!("only a destroy: session_cli::is_destroy"),
        }
    })
}

/// Whether `session`'s command is a child verb (`launch`, `send`, `clear`,
/// `close`: slice 4 U3), which [`child`] runs as the user.
pub fn is_child(session: &Session) -> bool {
    matches!(
        session.verb,
        Some(
            SessionVerb::Launch(_)
                | SessionVerb::Send(_)
                | SessionVerb::Clear(_)
                | SessionVerb::Close(_)
                | SessionVerb::Stop(_)
                | SessionVerb::Restart(_)
                | SessionVerb::Keep(_)
                | SessionVerb::Rename(_)
                | SessionVerb::Import
        )
    )
}

/// Run `session`'s child verb ([`is_child`]) on the user's `writes`,
/// writing its answer to `out`: the target as the daemon has it after
/// (`--format json`: its `Session`), or in text what was done.
pub fn child<W: ChildWrites>(
    session: &Session,
    writes: &W,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    // Time and I/O: the daemon is called over the network.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    // The session's own writes answer as they always did, not as the session.
    if let Some(done) = own_write(session, writes, &runtime, format, out)? {
        return Ok(done);
    }
    let (written, said) = runtime.block_on(async {
        match &session.verb {
            Some(SessionVerb::Launch(args)) => {
                let launch = Launch {
                    owner: Some(args.owner.clone()),
                    harp: args.name.clone(),
                    engine: args.engine.clone().unwrap_or_default(),
                    agent: args.agent.clone().unwrap_or_default(),
                    task: args.task.clone().unwrap_or_default(),
                    project: args.project.clone(),
                };
                let written = writes.launch(&launch).await.map_err(call_failure)?;
                let said = match written.applied {
                    true => format!("launched, a child of {}", args.owner),
                    false => format!("already launched, a child of {}", args.owner),
                };
                Ok::<_, Failure>((written, said))
            }
            Some(SessionVerb::Send(args)) => {
                let written = writes
                    .send_as(
                        &args.harp,
                        urgency(args.urgency),
                        &args.text,
                        args.id.as_deref(),
                    )
                    .await
                    .map_err(call_failure)?;
                let said = recorded(&written);
                Ok((written, said))
            }
            Some(SessionVerb::Clear(args)) => {
                let written = writes.clear(&args.harp).await.map_err(call_failure)?;
                let said = recorded(&written);
                Ok((written, said))
            }
            Some(SessionVerb::Close(args)) => {
                let written = writes.close(&args.harp).await.map_err(call_failure)?;
                let said = match written.applied {
                    true => "closed".to_string(),
                    false => "already closed".to_string(),
                };
                Ok((written, said))
            }
            Some(SessionVerb::Stop(args)) => {
                let written = writes.stop(&args.harp).await.map_err(call_failure)?;
                let said = match written.applied {
                    true => "its run asked to stop".to_string(),
                    false => "no run of it to stop".to_string(),
                };
                Ok((written, said))
            }
            Some(SessionVerb::Restart(args)) => {
                let written = writes
                    .restart(&args.harp, &args.note)
                    .await
                    .map_err(call_failure)?;
                let said = match written.applied {
                    true => "a restart asked: its engine is restarted, resuming its conversation, once its turn ends".to_string(),
                    false => "a restart is asked already".to_string(),
                };
                Ok((written, said))
            }
            _ => unreachable!("only a child verb: session_cli::is_child"),
        }
    })?;
    let harp = harp_of(&written.session.name).to_string();
    wrote(match format {
        Format::Json => write_json(out, &written.session),
        Format::Text => writeln!(out, "{harp}: {said}"),
    })?;
    let mut done = Done::default();
    // A json answer is the session alone: a close that changed nothing is
    // said beside it, as the text says it on its line.
    if format == Format::Json && is_close(session) && !written.applied {
        done.notices
            .push(Box::new(crate::error::AlreadyClosed { harp }));
    }
    Ok(done)
}

/// `keep` and `rename` (ONE WRITER: commands sent to the daemon as the user,
/// each under an id minted for the call), answered as they always were
/// rather than as the session; none for any other verb.
fn own_write<W: ChildWrites>(
    session: &Session,
    writes: &W,
    runtime: &tokio::runtime::Runtime,
    format: Format,
    out: &mut dyn Write,
) -> Result<Option<Done>, Failure> {
    match &session.verb {
        Some(SessionVerb::Keep(args)) => {
            let kept = !args.undo;
            runtime
                .block_on(writes.keep(&args.harp, kept))
                .map_err(call_failure)?;
            wrote(match format {
                Format::Json => write_json(
                    out,
                    &KeepDoc {
                        harp: args.harp.clone(),
                        kept,
                    },
                ),
                Format::Text if kept => writeln!(
                    out,
                    "{} is marked keep: its transcript is kept at any age",
                    args.harp
                ),
                Format::Text => writeln!(
                    out,
                    "{} is no longer marked keep: its transcript is reclaimable once session_transcript_retention has passed since it ended",
                    args.harp
                ),
            })?;
            Ok(Some(Done::default()))
        }
        Some(SessionVerb::Import) => {
            let report = runtime.block_on(writes.import()).map_err(call_failure)?;
            wrote(match format {
                Format::Json => write_json(out, &ImportDoc::of(&report)),
                Format::Text => import_text(&report, out),
            })?;
            Ok(Some(Done::default()))
        }
        Some(SessionVerb::Rename(args)) => {
            let written = runtime
                .block_on(writes.title(&args.harp, &args.title))
                .map_err(call_failure)?;
            wrote(match format {
                Format::Json => write_json(
                    out,
                    &TitleDoc::new(&args.harp, &args.title, written.applied),
                ),
                Format::Text => writeln!(out, "{}: titled {:?}", args.harp, args.title),
            })?;
            Ok(Some(Done::default()))
        }
        _ => Ok(None),
    }
}

/// Whether `session`'s command is `close`.
fn is_close(session: &Session) -> bool {
    matches!(session.verb, Some(SessionVerb::Close(_)))
}

/// What a message or a clear came to, said.
fn recorded(written: &Written) -> String {
    match written.applied {
        true => format!("{} recorded", written.id),
        false => format!("{} already recorded", written.id),
    }
}

/// The harp a session's resource name ends with.
fn harp_of(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or(name)
}

/// An urgency word, as the domain's.
fn urgency(word: UrgencyWord) -> Urgency {
    match word {
        UrgencyWord::Inform => Urgency::Inform,
        UrgencyWord::Wake => Urgency::Wake,
        UrgencyWord::Steer => Urgency::Steer,
    }
}

/// Run `session`'s read ([`is_read`]) on the daemon's client `reads`,
/// writing its answer to `out`.
pub fn read<R: SessionReads>(
    session: &Session,
    reads: &R,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    // Time and I/O: the daemon is called over the network.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    runtime.block_on(async {
        match &session.verb {
            Some(SessionVerb::List(args)) => list(reads, args, format, out).await,
            Some(SessionVerb::Search(args)) => {
                let state = api_state(args.state.as_ref())?;
                let found = reads
                    .search_sessions(args.words.clone(), args.all, state)
                    .await
                    .map_err(call_failure)?;
                listing(&found, format, out)
            }
            Some(SessionVerb::Show(args)) => {
                let shown = reads.get_session(&args.harp).await.map_err(call_failure)?;
                wrote(match format {
                    Format::Json => write_json(out, &shown),
                    Format::Text => session_text(&shown, out),
                })?;
                Ok(Done::default())
            }
            Some(SessionVerb::Transcript(Transcript {
                verb: Some(TranscriptVerb::Show(args)),
            })) => {
                let read = reads
                    .get_transcript(&args.harp)
                    .await
                    .map_err(call_failure)?;
                wrote(match format {
                    // One entry's proto3 JSON a line (`ctxloom.v1.LogEntry`).
                    Format::Json => read
                        .entries
                        .iter()
                        .try_for_each(|entry| writeln!(out, "{}", json_line(entry))),
                    Format::Text => conversation_text(&read.entries, out),
                })?;
                Ok(Done::default())
            }
            // `transcript list`, and the bare `transcript`: the daemon's
            // listing, each session with whether its events hold a transcript.
            Some(SessionVerb::Transcript(Transcript { verb })) => {
                let (all, harp) = match verb {
                    Some(TranscriptVerb::List(args)) => (args.all, args.harp.as_deref()),
                    _ => (false, None),
                };
                transcript_list(reads, all, harp, format, out).await
            }
            // The bare noun lists; any other verb is no read.
            _ => list(reads, &ListArgs::default(), format, out).await,
        }
    })
}

/// One log entry as one line of proto3 JSON.
fn json_line(entry: &v1::LogEntry) -> String {
    serde_json::to_string(entry).unwrap_or_default()
}

/// Run `session`'s command (one that is not a read, [`is_read`]) on
/// `service`, writing its answer to `out`.
pub fn run<S: WorktreeUse>(
    session: &Session,
    service: &mut S,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_time()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    runtime.block_on(execute(session, service, format, out))
}

/// A call to the daemon that came to nothing: its diagnostic and its exit.
pub(crate) fn call_failure(error: CallError) -> Failure {
    let exit = error.exit();
    let refusals = error.refusals();
    let mut failure = Failure::new(error, exit, Vec::new());
    failure.failure = failure.failure.with_refusals(refusals);
    failure
}

/// A `--state` word as the state the daemon's API names (none: any).
fn api_state(word: Option<&String>) -> Result<v1::session::State, Failure> {
    Ok(match state_of(word)? {
        None => v1::session::State::Unspecified,
        Some(State::Live) => v1::session::State::Live,
        Some(State::Ended) => v1::session::State::Ended,
        Some(State::Unknown) => v1::session::State::Unknown,
    })
}

/// A listing the daemon answered, in `format`: its proto3 JSON (ruling
/// 146), or the table.
fn listing(
    listed: &v1::ListSessionsResponse,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    wrote(match format {
        Format::Json => write_json(out, listed),
        Format::Text => listing_text(&listed.sessions, out),
    })?;
    Ok(Done::default())
}

/// A fault of the command line itself.
pub(crate) fn fault(fault: CliFault) -> Failure {
    let exit = fault.exit();
    Failure::new(fault, exit, Vec::new())
}

/// The answer written, or why it could not be.
pub(crate) fn wrote(written: io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        fault(CliFault::Output {
            ext_err_txt: e.to_string(),
        })
    })
}

/// A `--state` word as a state; the word that is none is refused before
/// anything is asked.
fn state_of(word: Option<&String>) -> Result<Option<State>, Failure> {
    let Some(word) = word else { return Ok(None) };
    State::parse(word).map(Some).ok_or_else(|| {
        fault(CliFault::BadState {
            value: word.clone(),
            states: STATES.join(", "),
        })
    })
}

async fn execute<S: WorktreeUse>(
    session: &Session,
    service: &mut S,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    match &session.verb {
        // The daemon's: `read` runs them.
        None | Some(SessionVerb::List(_) | SessionVerb::Search(_) | SessionVerb::Show(_)) => {
            unreachable!("a read is the daemon's: session_cli::read")
        }
        // The daemon's: `destroy` runs them.
        #[cfg(feature = "session-destroy")]
        Some(SessionVerb::Remove(_) | SessionVerb::Sweep(_)) => {
            unreachable!("a destroy is the daemon's: session_cli::destroy")
        }
        // The daemon's: `child` runs it.
        Some(SessionVerb::Import) => {
            unreachable!("an import is the daemon's: session_cli::child")
        }
        // The daemon's: `read` runs them.
        Some(SessionVerb::Transcript(_)) => {
            unreachable!("a transcript verb is the daemon's: session_cli::read")
        }
        Some(SessionVerb::Worktrees(worktrees)) => worktree_verb(service, worktrees, format, out),
        // The daemon's: `child` runs them.
        Some(
            SessionVerb::Launch(_)
            | SessionVerb::Send(_)
            | SessionVerb::Clear(_)
            | SessionVerb::Close(_)
            | SessionVerb::Stop(_)
            | SessionVerb::Restart(_)
            | SessionVerb::Keep(_)
            | SessionVerb::Rename(_),
        ) => unreachable!("a child verb is the daemon's: session_cli::child"),
        Some(SessionVerb::Approvals(_)) => {
            unreachable!("the asks are the daemon's: session_cli::approvals")
        }
    }
}

/// An isolation use case's failure: its diagnostic and exit code.
fn isolation_failure(error: IsolationError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit, Vec::new())
}

/// `session worktrees ...`: the scratch checkouts, listed or purged. A purge
/// reports first, and an action that changed nothing refuses (exit 48) after
/// the report is out, so an unattended run that found nothing safe cannot be
/// mistaken for one that cleaned up.
fn worktree_verb<S: WorktreeUse>(
    service: &mut S,
    worktrees: &Worktrees,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let (harp, apply) = match &worktrees.verb {
        None => (None, false),
        Some(WorktreesVerb::List(args)) => (args.harp.as_deref(), false),
        Some(WorktreesVerb::Purge(args)) => (Some(args.harp.as_str()), args.yes),
    };
    let report = service.worktrees(harp, apply).map_err(isolation_failure)?;
    wrote(match format {
        Format::Json => write_json(out, &report),
        Format::Text => worktrees_text(&report, out),
    })?;
    let mut done = Done::default();
    let Some(WorktreesVerb::Purge(args)) = &worktrees.verb else {
        return Ok(done);
    };
    if !args.yes {
        done.notices.push(Box::new(RemovedNothing {
            command: format!("ctxloom session worktrees purge {} --yes", args.harp),
        }));
    } else if report.reaped == 0 {
        return Err(isolation_failure(IsolationError::NothingRemoved {
            harp: args.harp.clone(),
            spared: report.spared,
            skipped: report.skipped,
        }));
    }
    Ok(done)
}

async fn list<R: SessionReads>(
    reads: &R,
    args: &ListArgs,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let state = api_state(args.state.as_ref())?;
    let listed = reads
        .list_sessions(args.all, state)
        .await
        .map_err(call_failure)?;
    listing(&listed, format, out)
}

/// `session remove`: the destroy (owner ruling 98, the session-destroy
/// feature's), which the DAEMON runs (owner, 2026-11-12): reported, and
/// applied on `--yes`. A refused removal prints its plan, then fails.
#[cfg(feature = "session-destroy")]
async fn remove<R: SessionRemovals>(
    removals: &R,
    (holds, asking): (&dyn SessionHolds, &dyn crate::Asking),
    harp: &str,
    args: &RemoveArgs,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let mut removed = removals
        .delete_session(harp, args.yes, args.even_if_live)
        .await
        .map_err(call_failure)?;
    // At her terminal (step 10c U11), the preview's confirmation is asked
    // there, and her yes is the apply of what it showed: its tag, and her
    // answer. A no, or no terminal, leaves the preview.
    let mut answered = false;
    if let (false, true, Removed::Done(plan)) = (args.yes, asking.is_terminal(), &removed) {
        if let Some((id, question)) = confirmation(&plan.needs) {
            if asking.confirm(question) {
                let answers = vec![v1::Answer {
                    need_id: id.to_string(),
                    answer: Some(v1::answer::Answer::Confirmed(true)),
                }];
                let etag = plan.etag.clone();
                removed = removals
                    .apply_removal(harp, args.even_if_live, &etag, answers)
                    .await
                    .map_err(call_failure)?;
                answered = true;
            }
        }
    }
    let (plan, why) = match removed {
        Removed::Done(plan) => (plan, None),
        Removed::Refused { plan, why } => (plan, Some(why)),
    };
    // The session's own refusal reached no worktree; a worktree's names them.
    let worktrees = plan
        .worktrees
        .as_ref()
        .filter(|w| !w.worktrees.is_empty())
        .filter(|_| !matches!(why, Some(daemon_api::RemovalRejection::Session(_))))
        .map(report_of);
    wrote(match format {
        Format::Json => write_json(out, &plan),
        Format::Text => removal_text(&removal_of(&plan), out).and_then(|()| match &worktrees {
            Some(report) => worktrees_text(report, out),
            None => Ok(()),
        }),
    })?;
    if let Some(why) = why {
        return Err(Failure::new(why, Exit::REFUSED, Vec::new()));
    }
    let mut done = Done::default();
    // Owner ruling 104: what a person wrote goes with the session, so it is
    // named first, a warning and never a refusal; a refused removal destroys
    // nothing, so it warns of nothing.
    if !plan.authored.is_empty() {
        done.notices.push(Box::new(DestroysAuthored {
            harp: harp.to_string(),
            files: plan.authored.join(", "),
        }));
    }
    // s4-Q14, H-Q8 (a): what its hold keeps goes with it too, warned of
    // the same way; an applied destroy then discards it.
    if let Some(held) =
        destroys_held(holds, &plan.name, plan.applied == Some(true)).map_err(hold_failure)?
    {
        done.notices.push(Box::new(DestroysHeld {
            harp: harp.to_string(),
            facts: held.facts,
            set_aside: held.set_aside,
        }));
    }
    if !args.yes && !answered {
        // Not refused, so `--yes` would destroy the session.
        done.would_change = true;
        let flag = if args.even_if_live {
            " --even-if-live"
        } else {
            ""
        };
        done.notices.push(Box::new(RemovedNothing {
            command: format!("ctxloom {REMOVE_VERB} {harp} --yes{flag}"),
        }));
    }
    Ok(done)
}

/// A removal preview's Confirm need: its id and its question.
#[cfg(feature = "session-destroy")]
fn confirmation(needs: &[v1::Need]) -> Option<(&str, &str)> {
    needs.iter().find_map(|need| match &need.kind {
        Some(v1::need::Kind::Confirm(confirm)) => Some((need.id.as_str(), confirm.text.as_str())),
        _ => None,
    })
}

/// `session sweep`'s purge age flag.
#[cfg(feature = "session-destroy")]
const PURGE_FLAG: &str = "--purge-older-than";

/// `session remove`'s age form's flag: the same sweep, by that age.
#[cfg(feature = "session-destroy")]
const AGE_FLAG: &str = "--older-than";

/// A sweep as asked, by `session sweep` or `session remove --older-than`.
#[cfg(feature = "session-destroy")]
struct Sweep<'a> {
    all_projects: bool,
    /// The purge age, with the flag it was given by.
    age: Option<(&'static str, &'a str)>,
    yes: bool,
    /// The verb the apply is named with.
    verb: &'static str,
}

/// `session sweep` (and `session remove --older-than`, the same sweep):
/// every session's fate reported by the daemon, and with `--yes` acted on
/// there (a destroy: the session-destroy feature's). An age that is none is
/// refused here, naming its flag, before the daemon is asked.
#[cfg(feature = "session-destroy")]
async fn sweep<R: SessionRemovals>(
    removals: &R,
    holds: &dyn SessionHolds,
    args: &Sweep<'_>,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    if let Some((flag, age)) = args.age {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX));
        let now = Timestamp {
            seconds: now,
            nanos: 0,
        };
        reclaim_application::parse_age(flag, age, now).map_err(reclaim_failure)?;
    }
    let report = removals
        .sweep_sessions(
            args.all_projects,
            args.age.map(|(_, age)| age.to_string()),
            args.yes,
        )
        .await
        .map_err(call_failure)?;
    wrote(match format {
        Format::Json => write_json(out, &report),
        Format::Text => sweep_text(&report, out),
    })?;
    let mut done = Done::default();
    // Owner ruling 104: each destroy that takes a person's files warns of
    // them, as `session remove` does.
    for row in report.rows.iter().filter(|row| !row.authored.is_empty()) {
        done.notices.push(Box::new(DestroysAuthored {
            harp: row.harp.clone(),
            files: row.authored.join(", "),
        }));
    }
    // s4-Q14, H-Q8 (a): each session it destroys, or plans to, has its
    // hold warned of; each destroyed has it discarded, before a failed
    // row fails the command.
    for removal in &report.sessions {
        if let Some(held) = destroys_held(holds, &removal.name, removal.applied == Some(true))
            .map_err(hold_failure)?
        {
            done.notices.push(Box::new(DestroysHeld {
                harp: removal.harp.clone(),
                facts: held.facts,
                set_aside: held.set_aside,
            }));
        }
    }
    let counts = report.counts.unwrap_or_default();
    if args.yes && counts.failed > 0 {
        return Err(fault(CliFault::SweepFailed {
            count: counts.failed,
        }));
    }
    if !args.yes {
        done.would_change = counts.planned > 0;
        let age = args
            .age
            .map(|(flag, age)| format!(" {flag} {age}"))
            .unwrap_or_default();
        let all = if args.all_projects {
            " --all-projects"
        } else {
            ""
        };
        done.notices.push(Box::new(RemovedNothing {
            command: format!("ctxloom {}{all}{age} --yes", args.verb),
        }));
    }
    Ok(done)
}

/// A hold this process could not read or discard: a failure (32), never a
/// quiet leftover.
#[cfg(feature = "session-destroy")]
fn hold_failure(error: reclaim_application::HoldError) -> Failure {
    Failure::new(error, Exit::FAILED, Vec::new())
}

/// A reclaim use case's failure: its diagnostic and exit code.
#[cfg(feature = "session-destroy")]
fn reclaim_failure(error: ReclaimError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit, Vec::new())
}

/// `session transcript list`: the sessions the daemon lists, each with
/// whether its events hold a transcript and its size; `harp` restricts it
/// to one session, which must be one the daemon lists.
async fn transcript_list<R: SessionReads>(
    reads: &R,
    all: bool,
    harp: Option<&str>,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let listed = reads.list_transcripts(all).await.map_err(call_failure)?;
    let rows: Vec<TranscriptRow> = listed
        .sessions
        .iter()
        .map(|session| TranscriptRow {
            harp: harp_of(&session.name).to_string(),
            captured: session.transcript_captured,
            bytes: session.transcript_bytes,
        })
        .filter(|row| harp.is_none_or(|asked| row.harp == asked))
        .collect();
    if let (Some(asked), true) = (harp, rows.is_empty()) {
        return Err(call_failure(CallError::Refused(
            daemon_api::Rejection::Api(daemon_api::ApiRejection::SessionNotFound {
                name: asked.to_string(),
            }),
        )));
    }
    wrote(match format {
        Format::Json => write_json(out, &TranscriptsDoc::of(&rows)),
        Format::Text => transcripts_text(&rows, out),
    })?;
    Ok(Done::default())
}

#[cfg(test)]
#[path = "run.test.rs"]
mod tests;
