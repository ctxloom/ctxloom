//! The screen's loop: the watch's events and the person's keys in, frames
//! out. It is the one loop of both forms of the screen: the text seam
//! (frames as plain lines, keys named one a line) and the terminal
//! (ratatui and crossterm); only the [`Display`] and the keys' source
//! differ.
//!
//! The watch is pushed: the loop waits on it for a tick at a time so that a
//! key is never kept waiting, which is a local tick and no look at the
//! daemon. The one thing looked for on a timer is a daemon to wait for.

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
const TICK: Duration = Duration::from_millis(50);
/// How long between looks for a daemon to serve.
const LOOK: Duration = Duration::from_millis(500);

/// What the person typed, before the screen has read it.
#[derive(Debug)]
pub enum Input {
    Key(Key),
    /// A line of the seam's input: a key's name, or the answer to a prompt.
    Line(String),
}

/// Where a frame is drawn.
pub trait Display {
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

/// Text frames, one after the other, each ended by a line `---`.
pub struct Frames<'a>(pub &'a mut dyn Write);

impl Display for Frames<'_> {
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
    let mut screen = Screen::new(&scope_of(command), sort);
    let mut feed: Option<Box<dyn TopFeed>> = None;
    let mut looked: Option<Instant> = None;
    let mut drawn = String::new();
    loop {
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
            Some(open) => match open.within(TICK) {
                Within::Idle => {}
                Within::Event(event) => screen.apply(*event),
                Within::Broke(_) => {
                    feed = None;
                    screen.wait();
                }
                Within::Ended => return Ok(Done::default()),
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
        let frame = screen.frame();
        let text = frame.text();
        if screen.ready() && text != drawn {
            display.draw(&frame)?;
            drawn = text;
        }
    }
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
