//! The `top` noun (DRAFT): what the user's daemon hosts, live.
//!
//! ```text
//! ctxloom top [<session> [--tree]] [--all] [--format json|text]
//!             [--state <s>]... [--agent <a>]... [--profile <p>]... [--tag <t>]...
//!             [--engine <e>]... [--owner <harp>]... [--sort <column>[:asc|desc]]
//!             [--log <harp>]
//! ```
//!
//! A client of the daemon's `WatchTop`. With `--format json` (the default
//! off a terminal) it prints each message of the watch as one JSON line, the
//! first a snapshot, until it is stopped. It never starts a daemon: while
//! none serves it says `waiting` once and waits, and a daemon lost under the
//! stream puts it back to waiting; the next daemon's snapshot is the truth.
//! Filtering and scope are the daemon's; the snapshot's order is this
//! command's. The full-screen form is not built.

use std::io::Write;

use angzarr_presentation::Format;
use clap::{Args, Command};
use daemon_proto::v1;
use sessions_application::{State, STATES};

use crate::error::CliFault;
use crate::run::{fault, wrote, Done, Failure};

/// Where `--sort` may sort.
const COLUMNS: [&str; 7] = [
    "name", "state", "agent", "profile", "engine", "owner", "started",
];

/// The `top` noun, shaped for mounting on the command line.
pub fn top_noun() -> Command {
    Top::augment_args(Command::new("top"))
        .about("Watch your sessions live: their state, their permission asks and their log")
        .after_help(
            "Examples:\n  ctxloom top\n  ctxloom top --format json --agent reviewer\n  ctxloom top brisk-copper-moth --format json\n  ctxloom top amber-quiet-heron --tree --format json",
        )
}

/// The `top` command.
#[derive(Args, Clone, Debug)]
pub struct Top {
    /// A session to watch, by its harp or its full name; none, every session of the project
    #[arg(value_name = "SESSION")]
    pub session: Option<String>,
    /// With a session: it and everything under it
    #[arg(long)]
    pub tree: bool,
    /// Every project's sessions (default: the current one)
    #[arg(long)]
    pub all: bool,
    /// Only sessions in this state: live, ended, unknown (default: live)
    #[arg(long, value_name = "STATE")]
    pub state: Vec<String>,
    /// Only sessions of this agent
    #[arg(long, value_name = "AGENT")]
    pub agent: Vec<String>,
    /// Only sessions of this profile
    #[arg(long, value_name = "PROFILE")]
    pub profile: Vec<String>,
    /// Only sessions whose profiles loaded this bundle
    #[arg(long, value_name = "BUNDLE")]
    pub tag: Vec<String>,
    /// Only sessions on this engine
    #[arg(long, value_name = "ENGINE")]
    pub engine: Vec<String>,
    /// Only the direct children of this session
    #[arg(long, value_name = "HARP")]
    pub owner: Vec<String>,
    /// Order the snapshot by a column: name, state, agent, profile, engine, owner, started
    #[arg(long, value_name = "COLUMN")]
    pub sort: Option<String>,
    /// Follow this session's log as well
    #[arg(long, value_name = "HARP")]
    pub log: Option<String>,
}

/// What a watch gave within a wait.
pub enum Within {
    /// Nothing came in the time.
    Idle,
    /// An event came.
    Event(Box<v1::TopEvent>),
    /// The watch broke: the daemon is gone.
    Broke(Box<Failure>),
    /// The watch ended.
    Ended,
}

/// An open watch's events, as the daemon pushes them.
pub trait TopFeed {
    /// The next event; `None` once the watch ended, an error once it broke
    /// (the daemon gone).
    fn next(&mut self) -> Option<Result<v1::TopEvent, Failure>>;

    /// The next event, if it comes within `wait`. The screen waits on the
    /// watch a tick at a time so a key is never kept waiting. The default
    /// waits for it.
    fn within(&mut self, wait: std::time::Duration) -> Within {
        let _ = wait;
        match self.next() {
            Some(Ok(event)) => Within::Event(Box::new(event)),
            Some(Err(failure)) => Within::Broke(Box::new(failure)),
            None => Within::Ended,
        }
    }
}

/// Where a watch is opened.
pub trait TopSource {
    /// Open the watch `make` builds for the project the daemon names (every
    /// project's under `all`); `None` when no daemon serves now. It starts
    /// none.
    fn open(
        &self,
        all: bool,
        make: &dyn Fn(&str) -> Result<v1::WatchTopRequest, Failure>,
    ) -> Result<Option<Box<dyn TopFeed>>, Failure>;

    /// Wait a little for a daemon to serve.
    fn wait(&self);
}

impl Top {
    /// The usage a watch cannot be opened with, refused before a daemon is
    /// asked.
    fn usage(&self) -> Result<(), Failure> {
        if self.tree && self.session.is_none() {
            return Err(fault(CliFault::TopTreeWithoutSession));
        }
        if self.all && self.session.is_some() {
            return Err(fault(CliFault::TopAllWithSession));
        }
        self.states()?;
        Sort::of(self.sort.as_deref()).map(|_| ())
    }

    /// The `--state` words as the API's states: a word that is no state is
    /// refused here, before any daemon is reached.
    fn states(&self) -> Result<Vec<i32>, Failure> {
        self.state
            .iter()
            .map(|word| {
                State::parse(word)
                    .ok_or_else(|| {
                        fault(CliFault::BadState {
                            value: word.clone(),
                            states: STATES.join(", "),
                        })
                    })
                    .map(|state| match state {
                        State::Live => v1::session::State::Live,
                        State::Ended => v1::session::State::Ended,
                        State::Unknown => v1::session::State::Unknown,
                    } as i32)
            })
            .collect()
    }

    /// The request, for the project `parent` names.
    pub(crate) fn request(&self, parent: &str) -> Result<v1::WatchTopRequest, Failure> {
        let states = self.states()?;
        let named = |word: &str, parent: &str| match word.starts_with("projects/") {
            true => word.to_string(),
            false => format!("{parent}/sessions/{word}"),
        };
        let (parent, session) = match &self.session {
            Some(word) => {
                let name = named(word, parent);
                let project = name
                    .split("/sessions/")
                    .next()
                    .unwrap_or(parent)
                    .to_string();
                (project, name)
            }
            None => (parent.to_string(), String::new()),
        };
        let log_session = self
            .log
            .as_deref()
            .map(|word| named(word, &parent))
            .unwrap_or_default();
        Ok(v1::WatchTopRequest {
            parent,
            session,
            subtree: self.tree,
            states,
            agents: self.agent.clone(),
            profiles: self.profile.clone(),
            tags: self.tag.clone(),
            engines: self.engine.clone(),
            owners: self.owner.clone(),
            log_session,
            all_logs: false,
        })
    }
}

/// The screen: the seam's frames on `out`, or the person's terminal.
fn screen(
    command: &Top,
    source: &dyn TopSource,
    answers: &dyn crate::screen_run::TopAnswers,
    stood_in: bool,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{mpsc::channel, Arc};

    use crate::screen_run::{drive, Frames, Input};

    if stood_in {
        let (send, keys) = channel();
        // Keys are named one a line; a thread of its own, since the loop
        // never waits on them.
        std::thread::spawn(move || {
            for line in std::io::stdin().lines().map_while(Result::ok) {
                if send.send(Input::Line(line)).is_err() {
                    return;
                }
            }
        });
        return drive(command, source, answers, &keys, &mut Frames(out));
    }
    let stop = Arc::new(AtomicBool::new(false));
    let keys = crate::tui::keys(&stop);
    let mut terminal = crate::tui::Tui::take()?;
    let done = drive(command, source, answers, &keys, &mut terminal);
    stop.store(true, Ordering::Relaxed);
    done
}

/// The order of a snapshot's sessions: a column and a direction.
#[derive(Clone, Debug)]
pub struct Sort {
    column: &'static str,
    descending: bool,
}

impl Sort {
    /// `word`, `<column>[:asc|desc]`; none, the latest started first.
    pub fn of(word: Option<&str>) -> Result<Sort, Failure> {
        let Some(word) = word else {
            return Ok(Sort {
                column: "started",
                descending: true,
            });
        };
        let (name, direction) = match word.split_once(':') {
            Some((name, direction)) => (name, Some(direction)),
            None => (word, None),
        };
        let column = COLUMNS.iter().find(|column| **column == name).copied();
        let descending = match (direction, name) {
            (Some("desc"), _) => true,
            (Some("asc"), _) | (None, _) if name != "started" => false,
            _ => direction != Some("asc"),
        };
        match (column, direction) {
            (Some(column), None | Some("asc" | "desc")) => Ok(Sort { column, descending }),
            _ => Err(fault(CliFault::TopSortColumn {
                value: word.to_string(),
                columns: COLUMNS.join(", "),
            })),
        }
    }

    /// The first column, ascending: where the screen's `s` starts.
    pub fn first() -> Sort {
        Sort {
            column: COLUMNS[0],
            descending: false,
        }
    }

    /// The next column in turn, ascending.
    pub fn next(&self) -> Sort {
        let at = COLUMNS.iter().position(|c| *c == self.column).unwrap_or(0);
        Sort {
            column: COLUMNS[(at + 1) % COLUMNS.len()],
            descending: false,
        }
    }

    /// The same column the other way.
    pub fn reversed(&self) -> Sort {
        Sort {
            column: self.column,
            descending: !self.descending,
        }
    }

    /// `column:direction`, as the header names it.
    pub fn word(&self) -> String {
        let direction = if self.descending { "desc" } else { "asc" };
        format!("{}:{direction}", self.column)
    }

    /// `sessions`, ordered; ties by name.
    pub fn order(&self, sessions: &mut [v1::Session]) {
        let last = |name: &str| name.rsplit('/').next().unwrap_or_default().to_string();
        let key = |session: &v1::Session| -> (String, String) {
            let text = match self.column {
                "name" => last(&session.name),
                "state" => format!("{:03}", session.state),
                "agent" => session.agent.clone(),
                "profile" => session.profiles.first().cloned().unwrap_or_default(),
                "engine" => session.engine.clone(),
                "owner" => last(&session.owner),
                _ => format!(
                    "{:020}",
                    session.start_time.as_ref().map_or(0, |at| at.seconds)
                ),
            };
            (text, last(&session.name))
        };
        sessions.sort_by(|a, b| match self.descending {
            false => key(a).cmp(&key(b)),
            true => key(b).cmp(&key(a)),
        });
    }
}

fn waiting() -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::Waiting(v1::TopWaiting {
            reason: "no ctxloom daemon is serving".to_string(),
        })),
    }
}

/// One message as one line.
fn emit(event: &v1::TopEvent, out: &mut dyn Write) -> Result<(), Failure> {
    let line = serde_json::to_string(event).map_err(|e| {
        fault(CliFault::Output {
            ext_err_txt: e.to_string(),
        })
    })?;
    wrote(writeln!(out, "{line}").and_then(|()| out.flush()))
}

/// Whether a screen can be drawn where `top` is run: `--format text` is a
/// screen, and wants a terminal.
pub fn needs_terminal(format: Format, terminal: bool) -> Result<(), CliFault> {
    match (format, terminal) {
        (Format::Text, false) => Err(CliFault::TopNeedsTerminal),
        _ => Ok(()),
    }
}

/// Run `top` on `source`: its lines to `out`, until the watch ends; at a
/// terminal, the screen. A person at the screen answers asks through
/// `answers`; `stood_in` is a terminal a test stands in (frames as plain
/// text, keys named a line each on stdin).
pub fn top(
    command: &Top,
    source: &dyn TopSource,
    answers: &dyn crate::screen_run::TopAnswers,
    format: Format,
    (terminal, stood_in): (bool, bool),
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    if format == Format::Text {
        needs_terminal(format, terminal).map_err(fault)?;
        command.usage()?;
        return screen(command, source, answers, stood_in, out);
    }
    command.usage()?;
    let sort = Sort::of(command.sort.as_deref())?;
    let make = |parent: &str| command.request(parent);
    let mut said_waiting = false;
    loop {
        let Some(mut feed) = source.open(command.all, &make)? else {
            if !said_waiting {
                emit(&waiting(), out)?;
                said_waiting = true;
            }
            source.wait();
            continue;
        };
        loop {
            match feed.next() {
                Some(Ok(mut event)) => {
                    if let Some(v1::top_event::Event::Snapshot(snapshot)) = event.event.as_mut() {
                        sort.order(&mut snapshot.sessions);
                    }
                    emit(&event, out)?;
                }
                // The daemon is gone: waiting again, said once.
                Some(Err(_)) => {
                    emit(&waiting(), out)?;
                    said_waiting = true;
                    break;
                }
                None => return Ok(Done::default()),
            }
        }
    }
}

#[cfg(test)]
#[path = "top.test.rs"]
mod tests;
