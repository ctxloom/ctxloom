//! The screen's loop: the watch's events and the person's keys in, frames
//! out. It is the one loop of both forms of the screen: the text seam
//! (frames as plain lines, keys named one a line) and the terminal
//! (ratatui and crossterm); only the [`Display`] and the keys' source
//! differ.
//!
//! The watch is pushed: the loop waits on it for a tick at a time so that a
//! key is never kept waiting, which is a local tick and no look at the
//! daemon. What the watch already holds is applied together, up to
//! [`BATCH`] events, before the frame is drawn, so a backlog is not replayed
//! frame by frame. The one thing looked for on a timer is a daemon to wait for.

use std::io::Write;
use std::sync::mpsc::Receiver;
use std::time::{Duration, Instant};

use daemon_proto::v1;

use crate::approvals::AlwaysPlan;
use crate::error::CliFault;
use crate::run::{fault, wrote, Done, Failure};
use crate::screen::{Effect, Frame, Key, Screen};
use crate::top::{Sort, Top, TopFeed, TopSource, Within};

/// How long the loop waits on the watch before it looks at the keys again.
pub(crate) const TICK: Duration = Duration::from_millis(50);
/// The most events one pass applies before it draws: a backlog the watch
/// already holds (every session's log, after the snapshot) is one frame, and
/// one that never runs dry cannot keep the keys waiting.
pub(crate) const BATCH: usize = 256;
/// How long between looks for a daemon to serve.
const LOOK: Duration = Duration::from_millis(500);

/// What the person typed, before the screen has read it.
#[derive(Debug)]
pub enum Input {
    Key(Key),
    /// A line of the seam's input: a key's name, or the answer to a prompt.
    Line(String),
}

/// The system's clock, whole seconds since the epoch.
// Reads the system clock; the loop's tests give a display its own.
#[cfg_attr(test, mutants::skip)]
fn system_now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| {
            i64::try_from(since.as_secs()).unwrap_or(i64::MAX)
        })
}

/// Where a frame is drawn.
pub trait Display {
    /// The icon of the ask state this display can draw.
    fn icon(&self) -> &'static str {
        crate::screen::ICON_ASCII
    }

    /// The time now, whole seconds since the epoch: the clock each row's
    /// timer counts to. The system's, unless a display stands one in.
    fn now(&self) -> i64 {
        system_now()
    }

    fn draw(&mut self, frame: &Frame) -> Result<(), Failure>;
}

/// Answering an ask, as the person at the screen: the `session approvals
/// answer` of it (the root composes it).
pub trait TopAnswers {
    /// What choosing `option` of `ask` would write, when it writes anything
    /// (an `allow-always`'s rule): to be confirmed first.
    fn preview(&self, ask: &v1::Approval, option: &str) -> Result<Option<AlwaysPlan>, String>;

    /// `ask` answered with `option`, its rule written first when
    /// `write_rule`; what to say of it, or why not.
    fn answer(&self, ask: &v1::Approval, option: &str, write_rule: bool) -> Result<String, String>;
}

/// Text frames, one after the other, each ended by a line `---`. The seam's
/// own clock runs `.1` seconds ahead of the system's (zero unless a
/// test-support build says otherwise), so a scenario reads a timer of hours
/// with no waiting.
pub struct Frames<'a>(pub &'a mut dyn Write, pub i64);

impl Display for Frames<'_> {
    fn now(&self) -> i64 {
        system_now().saturating_add(self.1)
    }

    fn draw(&mut self, frame: &Frame) -> Result<(), Failure> {
        wrote(
            self.0
                .write_all(frame.text().as_bytes())
                .and_then(|()| self.0.flush()),
        )
    }
}

fn scope_of(command: &Top) -> String {
    match (&command.session, command.all) {
        (Some(session), _) => session.rsplit('/').next().unwrap_or(session).to_string(),
        (None, true) => "every project".to_string(),
        (None, false) => "this project".to_string(),
    }
}

/// Run the screen of `command` over `source` until `q`, or the watch ends.
pub fn drive(
    command: &Top,
    source: &dyn TopSource,
    answers: &dyn TopAnswers,
    keys: &Receiver<Input>,
    display: &mut dyn Display,
) -> Result<Done, Failure> {
    let sort = match &command.sort {
        Some(word) => Some(Sort::of(Some(word))?),
        None => None,
    };
    let mut screen = Screen::new(&scope_of(command), sort).with_icon(display.icon());
    let mut feed: Option<Box<dyn TopFeed>> = None;
    let mut looked: Option<Instant> = None;
    let mut idle = 0;
    let mut drawn = String::new();
    loop {
        if let Some(stop) = source.stopped() {
            return Err(stop);
        }
        screen.tick(display.now());
        while let Ok(input) = keys.try_recv() {
            let key = match input {
                Input::Key(key) => key,
                Input::Line(line) if screen.prompting() => Key::Text(line),
                Input::Line(line) => match Key::named(line.trim()) {
                    Some(key) => key,
                    None => continue,
                },
            };
            match screen.key(key) {
                Some(Effect::Quit) => return Ok(Done::default()),
                Some(Effect::Choose { ask, option }) => match answers.preview(&ask, &option) {
                    Ok(Some(plan)) => screen.confirm(ask, &option, plan),
                    Ok(None) => said(&mut screen, answers.answer(&ask, &option, false)),
                    Err(why) => screen.say(&why),
                },
                Some(Effect::Confirmed { ask, option }) => {
                    said(&mut screen, answers.answer(&ask, &option, true));
                }
                None => {}
            }
        }
        match feed.as_mut() {
            Some(open) => match drained(open.as_mut(), &mut screen, &mut idle) {
                Drained::Open => {}
                Drained::Reopen => {
                    feed = None;
                    looked = None;
                }
                Drained::Broke => {
                    feed = None;
                    screen.wait();
                }
                Drained::Ended => return Ok(Done::default()),
            },
            None => {
                if looked.is_none_or(|at| at.elapsed() >= LOOK) {
                    looked = Some(Instant::now());
                    // One watch carries every session's log, so the person
                    // moves between sessions without it being opened again.
                    let make = |parent: &str| {
                        let mut request = command.request(parent)?;
                        request.all_logs = true;
                        Ok(request)
                    };
                    match source.open(command.all, &make)? {
                        Some(open) => feed = Some(open),
                        None => screen.wait(),
                    }
                } else {
                    std::thread::sleep(TICK);
                }
            }
        }
        let mut frame = screen.frame();
        // The bell is news, not state: the text compared is the frame's
        // without it, so a bell never makes a frame that changed nothing
        // else a different one, and is rung in the frame drawn for it.
        let text = frame.text();
        if screen.ready() {
            frame.bell = screen.take_bell();
            if frame.bell || text != drawn {
                display.draw(&frame)?;
                drawn = text;
            }
        }
    }
}

/// How a pass over the watch left it.
enum Drained {
    Open,
    /// An idle watch to be opened again.
    Reopen,
    Broke,
    Ended,
}

/// One pass over the watch: wait a tick for its first event, then apply
/// what it already holds, up to [`BATCH`], so one frame is drawn for them.
fn drained(open: &mut dyn TopFeed, screen: &mut Screen, idle: &mut u32) -> Drained {
    let mut wait = TICK;
    for _ in 0..BATCH {
        match open.within(wait) {
            Within::Event(event) => {
                *idle = 0;
                screen.apply(*event);
                // What is already waiting, never a wait.
                wait = Duration::ZERO;
            }
            // Only the wait for a first event is idleness; the end of a
            // drain is the backlog's.
            Within::Idle if wait == TICK => {
                *idle += 1;
                if open.reopened_when_idle() && *idle >= 10 {
                    *idle = 0;
                    return Drained::Reopen;
                }
                return Drained::Open;
            }
            Within::Idle => return Drained::Open,
            Within::Broke(_) => return Drained::Broke,
            Within::Ended => return Drained::Ended,
        }
    }
    Drained::Open
}

fn said(screen: &mut Screen, answered: Result<String, String>) {
    match answered {
        Ok(text) | Err(text) => screen.say(&text),
    }
}

/// The failure of a terminal that would not draw.
pub fn drawing(e: &std::io::Error) -> Failure {
    fault(CliFault::Output {
        ext_err_txt: e.to_string(),
    })
}

#[cfg(test)]
#[path = "screen_run.test.rs"]
mod tests;
