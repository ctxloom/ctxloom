//! Run a `session` command against the application.

use cli_support::{Exit, ExitClass};
use std::io::{self, Write};

use angzarr_diagnostic::Diagnostic;
#[cfg(feature = "session-destroy")]
use std::time::{SystemTime, UNIX_EPOCH};

use angzarr_presentation::{write_json, Format};
use daemon_api::{CallError, SessionReads};
#[cfg(feature = "session-destroy")]
use daemon_api::{Removed, SessionRemovals};
use daemon_proto::v1;
use isolation_application::{IsolationError, WorktreeUse};
#[cfg(feature = "session-destroy")]
use reclaim_application::ReclaimError;
#[cfg(feature = "session-destroy")]
use sessions_application::Timestamp;
use sessions_application::{
    dead_letter_line, AppError, DeadLetter, ImportRequest, ImportService, KeepRequest,
    RemovedNothing, SessionService, State, TitleRequest, TranscriptQuery, STATES,
};
#[cfg(feature = "session-destroy")]
use sessions_application::{DestroysAuthored, REMOVE_VERB};

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
    CliFault, ListArgs, RenameArgs, Session, SessionVerb, Transcript, TranscriptListArgs,
    TranscriptVerb, Worktrees, WorktreesVerb,
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
    /// command's exit is the distinguishing success 1, said only under
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
    /// `cause`, ending the command with `exit`, with `lines` to say beside it.
    fn new(cause: impl Diagnostic + 'static, exit: Exit, lines: Vec<String>) -> Failure {
        Failure {
            failure: cli_support::Failure::new(cause, exit),
            lines,
        }
    }
}

/// Whether `session`'s command is one of the reads the daemon answers
/// (`list`, the bare noun, `search`, `show`: slice 2 step 6), which
/// [`read`] runs; a destroy ([`is_destroy`]) is the daemon's too, and every
/// other runs on the sessions use cases ([`run`]).
pub fn is_read(session: &Session) -> bool {
    matches!(
        session.verb,
        None | Some(SessionVerb::List(_) | SessionVerb::Search(_) | SessionVerb::Show(_))
    )
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
/// `removals`, writing its answer to `out`.
#[cfg(feature = "session-destroy")]
pub fn destroy<R: SessionRemovals>(
    session: &Session,
    removals: &R,
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
                    sweep(removals, &ask, format, out).await
                }
                (None, Some(harp)) => remove(removals, harp, args, format, out).await,
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
                sweep(removals, &ask, format, out).await
            }
            _ => unreachable!("only a destroy: session_cli::is_destroy"),
        }
    })
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
            // The bare noun lists; any other verb is no read.
            _ => list(reads, &ListArgs::default(), format, out).await,
        }
    })
}

/// Run `session`'s command (one that is not a read, [`is_read`]) on
/// `service`, writing its answer to `out`.
pub fn run<S: SessionService + ImportService + WorktreeUse>(
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
fn call_failure(error: CallError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit, Vec::new())
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
fn fault(fault: CliFault) -> Failure {
    let exit = fault.exit();
    Failure::new(fault, exit, Vec::new())
}

/// A use case's failure: its diagnostic, its exit code, and the dead letters
/// its kernel calls answered.
fn failure(error: AppError) -> Failure {
    let exit = error.exit();
    let lines = error
        .dead_letters()
        .iter()
        .map(|l| dead_letter_line(&l.letter))
        .collect();
    Failure::new(error, exit, lines)
}

/// The answer written, or why it could not be.
fn wrote(written: io::Result<()>) -> Result<(), Failure> {
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

fn dead_letters(letters: &[DeadLetter]) -> Done {
    Done {
        notices: Vec::new(),
        lines: letters
            .iter()
            .map(|l| dead_letter_line(&l.letter))
            .collect(),
        would_change: false,
    }
}

async fn execute<S: SessionService + ImportService + WorktreeUse>(
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
        Some(SessionVerb::Rename(args)) => rename(service, args, format, out).await,
        Some(SessionVerb::Keep(args)) => {
            let kept = service
                .keep(KeepRequest {
                    project: None,
                    harp: args.harp.clone(),
                    keep: !args.undo,
                })
                .await
                .map_err(failure)?;
            wrote(match format {
                Format::Json => write_json(
                    out,
                    &KeepDoc {
                        harp: kept.harp.clone(),
                        kept: kept.kept,
                    },
                ),
                Format::Text if kept.kept => writeln!(
                    out,
                    "{} is marked keep: its transcript is kept at any age",
                    kept.harp
                ),
                Format::Text => writeln!(
                    out,
                    "{} is no longer marked keep: its transcript is reclaimable once session_transcript_retention has passed since it ended",
                    kept.harp
                ),
            })?;
            Ok(dead_letters(&kept.dead_letters))
        }
        // The daemon's: `destroy` runs them.
        #[cfg(feature = "session-destroy")]
        Some(SessionVerb::Remove(_) | SessionVerb::Sweep(_)) => {
            unreachable!("a destroy is the daemon's: session_cli::destroy")
        }
        Some(SessionVerb::Import) => {
            let report = service
                .import(ImportRequest::default())
                .await
                .map_err(failure)?;
            wrote(match format {
                Format::Json => write_json(out, &ImportDoc::of(&report)),
                Format::Text => import_text(&report, out),
            })?;
            Ok(dead_letters(&report.dead_letters))
        }
        Some(SessionVerb::Transcript(transcript)) => {
            transcripts(service, transcript, format, out).await
        }
        Some(SessionVerb::Worktrees(worktrees)) => worktree_verb(service, worktrees, format, out),
    }
}

/// An isolation use case's failure: its diagnostic and exit code.
fn isolation_failure(error: IsolationError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit, Vec::new())
}

/// `session worktrees ...`: the scratch checkouts, listed or purged. A purge
/// reports first, and an action that changed nothing refuses (exit 20) after
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

/// `session rename <harp> <title>` (S55): the title set; the harp stays.
async fn rename<S: SessionService>(
    service: &mut S,
    args: &RenameArgs,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let titled = service
        .set_title(TitleRequest {
            project: None,
            harp: args.harp.clone(),
            title: args.title.clone(),
        })
        .await
        .map_err(failure)?;
    wrote(match format {
        Format::Json => write_json(out, &TitleDoc::of(&titled)),
        Format::Text => writeln!(out, "{}: titled {:?}", titled.harp, titled.title),
    })?;
    Ok(dead_letters(&titled.dead_letters))
}

/// `session remove`: the destroy (owner ruling 98, the session-destroy
/// feature's), which the DAEMON runs (owner, 2026-11-12): reported, and
/// applied on `--yes`. A refused removal prints its plan, then fails.
#[cfg(feature = "session-destroy")]
async fn remove<R: SessionRemovals>(
    removals: &R,
    harp: &str,
    args: &RemoveArgs,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let removed = removals
        .delete_session(harp, args.yes, args.even_if_live)
        .await
        .map_err(call_failure)?;
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
    if !args.yes {
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

/// A reclaim use case's failure: its diagnostic and exit code.
#[cfg(feature = "session-destroy")]
fn reclaim_failure(error: ReclaimError) -> Failure {
    let exit = error.exit();
    Failure::new(error, exit, Vec::new())
}

async fn transcripts<S: SessionService + WorktreeUse>(
    service: &mut S,
    transcript: &Transcript,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    match &transcript.verb {
        None => transcript_list(service, &TranscriptListArgs::default(), format, out).await,
        Some(TranscriptVerb::List(args)) => transcript_list(service, args, format, out).await,
        Some(TranscriptVerb::Show(args)) => {
            let text = service.transcript(&args.harp).await.map_err(failure)?;
            wrote(match format {
                Format::Json if text.ends_with('\n') => write!(out, "{text}"),
                Format::Json => writeln!(out, "{text}"),
                Format::Text => conversation_text(&text, out),
            })?;
            Ok(Done::default())
        }
    }
}

async fn transcript_list<S: SessionService>(
    service: &mut S,
    args: &TranscriptListArgs,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let rows = service
        .transcripts(TranscriptQuery {
            project: None,
            all: args.all,
            harp: args.harp.clone(),
        })
        .await
        .map_err(failure)?;
    wrote(match format {
        Format::Json => write_json(out, &TranscriptsDoc::of(&rows)),
        Format::Text => transcripts_text(&rows, out),
    })?;
    Ok(Done::default())
}

#[cfg(test)]
#[path = "run.test.rs"]
mod tests;
