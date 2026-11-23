//! The screen's loop over a scripted watch and scripted keys: what the
//! frames come to, what is answered, and what the watch is asked for.

use std::cell::RefCell;
use std::sync::mpsc::{channel, Sender};
use std::time::Duration;

use clap::{FromArgMatches, Parser};
use daemon_proto::v1;

use super::{drive, Display, Input, TopAnswers};
use crate::approvals::AlwaysPlan;
use crate::screen::{Frame, Key};
use crate::top::{Top, TopFeed, TopSource, Within};
use crate::Failure;

fn command(line: &[&str]) -> Top {
    #[derive(Parser)]
    struct Cli {
        #[command(flatten)]
        top: Top,
    }
    Cli::parse_from(std::iter::once("top").chain(line.iter().copied())).top
}

fn session(harp: &str, agent: &str) -> v1::Session {
    v1::Session {
        name: format!("projects/p/sessions/{harp}"),
        agent: agent.to_string(),
        state: v1::session::State::Live as i32,
        ..v1::Session::default()
    }
}

fn ask() -> v1::Approval {
    v1::Approval {
        name: "projects/p/approvals/ask-a".to_string(),
        child: "projects/p/sessions/brisk-copper-moth".to_string(),
        tool_name: "Bash".to_string(),
        input_excerpt: "rm -rf build/".to_string(),
        state: "waiting".to_string(),
        options: ["allow-once", "allow-always"]
            .map(|id| v1::ApprovalOption {
                id: id.to_string(),
                kind: id.replace('-', "_"),
                label: String::new(),
                rule: "Bash(rm -rf build/)".to_string(),
            })
            .to_vec(),
        ..v1::Approval::default()
    }
}

fn snapshot() -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::Snapshot(v1::TopSnapshot {
            sessions: vec![
                session("amber-quiet-heron", ""),
                session("brisk-copper-moth", "reviewer"),
            ],
            approvals: vec![ask()],
        })),
    }
}

/// A watch that says its snapshot once and then nothing.
struct Feed(Option<v1::TopEvent>);

impl TopFeed for Feed {
    fn next(&mut self) -> Option<Result<v1::TopEvent, Failure>> {
        self.0.take().map(Ok)
    }

    fn within(&mut self, _: Duration) -> Within {
        match self.0.take() {
            Some(event) => Within::Event(Box::new(event)),
            None => Within::Idle,
        }
    }
}

/// A daemon that serves every time, and remembers the logs asked for.
#[derive(Default)]
struct Source {
    logs: RefCell<Vec<bool>>,
}

impl TopSource for Source {
    fn open(
        &self,
        _: bool,
        make: &dyn Fn(&str) -> Result<v1::WatchTopRequest, Failure>,
    ) -> Result<Option<Box<dyn TopFeed>>, Failure> {
        self.logs.borrow_mut().push(make("projects/p")?.all_logs);
        Ok(Some(Box::new(Feed(Some(snapshot())))))
    }

    fn wait(&self) {}
}

#[derive(Default)]
struct Answers {
    answered: RefCell<Vec<(String, bool)>>,
}

impl TopAnswers for Answers {
    fn preview(&self, _: &v1::Approval, option: &str) -> Result<Option<AlwaysPlan>, String> {
        Ok((option == "allow-always").then(|| AlwaysPlan {
            path: ".ctxloom/config.yaml".to_string(),
            key: "agents.default.engines.claude-code.permissions".to_string(),
            rule: "Bash(rm -rf build/)".to_string(),
            document: "{}".to_string(),
        }))
    }

    fn answer(&self, _: &v1::Approval, option: &str, write_rule: bool) -> Result<String, String> {
        self.answered
            .borrow_mut()
            .push((option.to_string(), write_rule));
        Ok(format!("answered {option}"))
    }
}

/// Draws into a list, and after each frame sends the next scripted input,
/// so the keys come once the screen shows what they are for.
struct Scripted {
    frames: Vec<Frame>,
    script: std::vec::IntoIter<Vec<Input>>,
    send: Sender<Input>,
}

impl Display for Scripted {
    fn draw(&mut self, frame: &Frame) -> Result<(), Failure> {
        self.frames.push(Frame {
            header: frame.header.clone(),
            status: frame.status.clone(),
            columns: frame.columns.clone(),
            rows: frame.rows.clone(),
            selected: frame.selected,
            asks_title: frame.asks_title.clone(),
            asks: frame.asks.clone(),
            log_title: frame.log_title.clone(),
            log: frame.log.clone(),
        });
        if frame.rows.is_empty() {
            return Ok(());
        }
        if let Some(group) = self.script.next() {
            for input in group {
                let _ = self.send.send(input);
            }
        }
        Ok(())
    }
}

fn run(line: &[&str], script: Vec<Vec<Input>>) -> (Vec<Frame>, Source, Answers) {
    let (send, keys) = channel();
    let mut display = Scripted {
        frames: Vec::new(),
        script: script.into_iter(),
        send,
    };
    let source = Source::default();
    let answers = Answers::default();
    drive(&command(line), &source, &answers, &keys, &mut display).unwrap();
    (display.frames, source, answers)
}

fn key(key: Key) -> Input {
    Input::Key(key)
}

fn quit() -> Input {
    key(Key::Char('q'))
}

#[test]
fn it_draws_nothing_until_the_watch_has_spoken_and_then_the_rows() {
    let (frames, ..) = run(&[], vec![vec![quit()]]);
    // The first frame is the snapshot's: no empty inbox is drawn before it.
    assert_eq!(frames[0].rows.len(), 2, "{} frames", frames.len());
    assert!(frames[0].status.is_empty(), "{:?}", frames[0].status);
}

#[test]
fn moving_the_selection_keeps_the_one_watch_that_carries_every_log() {
    let (_, source, _) = run(&[], vec![vec![key(Key::Down)], vec![quit()]]);
    let asked = source.logs.borrow();
    assert_eq!(*asked, [true], "the watch was opened again");
}

#[test]
fn a_digit_in_the_asks_answers_as_the_user_and_the_screen_says_so() {
    let script = vec![
        vec![
            Input::Line("down".to_string()),
            Input::Line("tab".to_string()),
            Input::Line("1".to_string()),
        ],
        vec![quit()],
    ];
    let (frames, _, answers) = run(&[], script);
    assert_eq!(
        *answers.answered.borrow(),
        [("allow-once".to_string(), false)]
    );
    let last = frames.last().unwrap();
    assert!(
        last.status.iter().any(|l| l == "answered allow-once"),
        "{:?}",
        last.status
    );
}

#[test]
fn an_allow_always_is_shown_first_and_written_only_on_a_yes() {
    let script = vec![
        vec![key(Key::Down), key(Key::Digit(2))],
        vec![key(Key::Char('n'))],
        vec![key(Key::Digit(2))],
        vec![key(Key::Char('y'))],
        vec![quit()],
    ];
    let (frames, _, answers) = run(&[], script);
    assert!(frames
        .iter()
        .any(|f| f.status.iter().any(|l| l.contains("[y/N]"))));
    assert_eq!(
        *answers.answered.borrow(),
        [("allow-always".to_string(), true)]
    );
}

#[test]
fn a_filter_typed_on_a_line_narrows_the_table() {
    let script = vec![
        vec![key(Key::Char('f'))],
        vec![Input::Line("agent=reviewer".to_string())],
        vec![quit()],
    ];
    let (frames, ..) = run(&[], script);
    let narrowed = frames
        .iter()
        .rev()
        .find(|f| f.header.contains("agent=reviewer"))
        .unwrap();
    assert_eq!(narrowed.rows.len(), 1);
}

#[test]
fn a_watch_that_ends_ends_the_screen() {
    struct Ends;
    impl TopFeed for Ends {
        fn next(&mut self) -> Option<Result<v1::TopEvent, Failure>> {
            None
        }
    }
    struct Gone;
    impl TopSource for Gone {
        fn open(
            &self,
            _: bool,
            _: &dyn Fn(&str) -> Result<v1::WatchTopRequest, Failure>,
        ) -> Result<Option<Box<dyn TopFeed>>, Failure> {
            Ok(Some(Box::new(Ends)))
        }
        fn wait(&self) {}
    }
    let (_send, keys) = channel();
    let mut display = Scripted {
        frames: Vec::new(),
        script: Vec::new().into_iter(),
        send: _send,
    };
    drive(
        &command(&[]),
        &Gone,
        &Answers::default(),
        &keys,
        &mut display,
    )
    .unwrap();
}

#[test]
fn from_arg_matches_is_how_the_command_is_built() {
    // `command` builds the same `Top` the binary does.
    let top = command(&["--agent", "reviewer"]);
    assert_eq!(top.agent, ["reviewer"]);
    let _ = <Top as FromArgMatches>::from_arg_matches;
}
