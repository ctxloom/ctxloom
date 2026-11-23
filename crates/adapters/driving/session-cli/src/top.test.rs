//! `ctxloom top`'s command line (DRAFT): the request it builds, the order of
//! its snapshot, and its loop over a daemon that comes and goes.

use std::cell::RefCell;
use std::collections::VecDeque;

use angzarr_presentation::Format;
use clap::FromArgMatches as _;
use daemon_proto::v1;

use super::*;

fn args(line: &[&str]) -> Top {
    let matches = top_noun()
        .try_get_matches_from(line)
        .expect("clap parses it");
    Top::from_arg_matches(&matches).expect("it is a top")
}

fn session(name: &str, agent: &str, engine: &str, started: i64) -> v1::Session {
    v1::Session {
        name: format!("projects/p/sessions/{name}"),
        agent: agent.to_string(),
        engine: engine.to_string(),
        start_time: Some(daemon_proto::google::protobuf::Timestamp {
            seconds: started,
            nanos: 0,
        }),
        ..v1::Session::default()
    }
}

fn names(sessions: &[v1::Session]) -> Vec<String> {
    sessions
        .iter()
        .map(|s| s.name.rsplit('/').next().unwrap().to_string())
        .collect()
}

#[test]
fn a_scope_by_harp_is_that_session_of_the_project_and_by_full_name_is_as_named() {
    let top = args(&["top", "brisk-copper-moth"]);
    let request = top.request("projects/calm").unwrap();
    assert_eq!(request.parent, "projects/calm");
    assert_eq!(request.session, "projects/calm/sessions/brisk-copper-moth");
    assert!(!request.subtree);
    let top = args(&["top", "projects/api/sessions/steady-lunar-pike", "--tree"]);
    let request = top.request("projects/calm").unwrap();
    assert_eq!(request.session, "projects/api/sessions/steady-lunar-pike");
    assert_eq!(request.parent, "projects/api");
    assert!(request.subtree);
}

#[test]
fn filters_become_the_requests_and_states_are_words_of_the_listing() {
    let top = args(&[
        "top",
        "--state",
        "live",
        "--state",
        "ended",
        "--agent",
        "reviewer",
        "--agent",
        "fixer",
        "--profile",
        "p",
        "--tag",
        "t",
        "--engine",
        "opencode",
        "--owner",
        "amber-quiet-heron",
    ]);
    let request = top.request("projects/calm").unwrap();
    assert_eq!(
        request.states,
        vec![
            v1::session::State::Live as i32,
            v1::session::State::Ended as i32
        ]
    );
    assert_eq!(request.agents, vec!["reviewer", "fixer"]);
    assert_eq!(request.profiles, vec!["p"]);
    assert_eq!(request.tags, vec!["t"]);
    assert_eq!(request.engines, vec!["opencode"]);
    assert_eq!(request.owners, vec!["amber-quiet-heron"]);
}

#[test]
fn a_state_that_is_none_is_refused_naming_the_states() {
    let failure = args(&["top", "--state", "running"])
        .request("projects/calm")
        .unwrap_err();
    assert_eq!(failure.failure.cause.code(), "session-cli-bad-state");
    let args = failure.failure.cause.args();
    assert!(
        args.iter().any(|(name, _)| *name == "states"),
        "the states are named: {args:?}"
    );
}

#[test]
fn one_session_watches_carry_their_log_and_other_scopes_the_log_asked_for() {
    let one = args(&["top", "brisk-copper-moth"])
        .request("projects/calm")
        .unwrap();
    assert!(
        one.log_session.is_empty(),
        "the daemon logs a one-session scope itself"
    );
    let logged = args(&["top", "--log", "brisk-copper-moth"])
        .request("projects/calm")
        .unwrap();
    assert_eq!(
        logged.log_session,
        "projects/calm/sessions/brisk-copper-moth"
    );
}

#[test]
fn usage_that_names_no_scope_is_refused_before_a_daemon_is_asked() {
    for (line, code) in [
        (
            vec!["top", "--tree"],
            "session-cli-top-tree-without-session",
        ),
        (
            vec!["top", "brisk-copper-moth", "--all"],
            "session-cli-top-all-with-session",
        ),
        (
            vec!["top", "--sort", "mood"],
            "session-cli-top-unknown-sort-column",
        ),
    ] {
        let refused = args(&line).usage().unwrap_err();
        assert_eq!(refused.failure.cause.code(), code, "{line:?}");
    }
}

#[test]
fn the_snapshot_is_ordered_by_the_sort_and_defaults_to_the_latest_started() {
    let mut sessions = vec![
        session("brisk-copper-moth", "reviewer", "claude-code", 10),
        session("quiet-silver-fern", "fixer", "opencode", 20),
    ];
    let sorted = |sort: Option<&str>, sessions: &[v1::Session]| {
        let mut sessions = sessions.to_vec();
        Sort::of(sort).unwrap().order(&mut sessions);
        names(&sessions)
    };
    assert_eq!(
        sorted(None, &sessions),
        ["quiet-silver-fern", "brisk-copper-moth"]
    );
    assert_eq!(
        sorted(Some("started:asc"), &sessions),
        ["brisk-copper-moth", "quiet-silver-fern"]
    );
    assert_eq!(
        sorted(Some("name"), &sessions),
        ["brisk-copper-moth", "quiet-silver-fern"]
    );
    assert_eq!(
        sorted(Some("name:desc"), &sessions),
        ["quiet-silver-fern", "brisk-copper-moth"]
    );
    assert_eq!(
        sorted(Some("agent"), &sessions),
        ["quiet-silver-fern", "brisk-copper-moth"]
    );
    assert_eq!(
        sorted(Some("engine:desc"), &sessions),
        ["quiet-silver-fern", "brisk-copper-moth"]
    );
    sessions.reverse();
    assert_eq!(
        sorted(None, &sessions),
        ["quiet-silver-fern", "brisk-copper-moth"]
    );
}

/// A source whose daemon is not there for the first `absent` opens, then
/// serves each feed in turn.
struct NoAnswers;

impl crate::TopAnswers for NoAnswers {
    fn preview(&self, _: &v1::Approval, _: &str) -> Result<Option<crate::AlwaysPlan>, String> {
        Ok(None)
    }

    fn answer(&self, _: &v1::Approval, _: &str, _: bool) -> Result<String, String> {
        Ok(String::new())
    }
}

struct Source {
    absent: RefCell<usize>,
    feeds: RefCell<VecDeque<Vec<Result<v1::TopEvent, ()>>>>,
    waits: RefCell<usize>,
}

struct Feed(VecDeque<Result<v1::TopEvent, ()>>);

impl TopFeed for Feed {
    fn next(&mut self) -> Option<Result<v1::TopEvent, Failure>> {
        self.0.pop_front().map(|event| {
            event.map_err(|()| {
                fault(CliFault::Output {
                    ext_err_txt: "lost".into(),
                })
            })
        })
    }
}

impl TopSource for Source {
    fn open(
        &self,
        _all: bool,
        make: &dyn Fn(&str) -> Result<v1::WatchTopRequest, Failure>,
    ) -> Result<Option<Box<dyn TopFeed>>, Failure> {
        make("projects/calm")?;
        if *self.absent.borrow() > 0 {
            *self.absent.borrow_mut() -= 1;
            return Ok(None);
        }
        Ok(self
            .feeds
            .borrow_mut()
            .pop_front()
            .map(|events| Box::new(Feed(events.into())) as Box<dyn TopFeed>))
    }

    fn wait(&self) {
        *self.waits.borrow_mut() += 1;
    }
}

fn snapshot(sessions: Vec<v1::Session>) -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::Snapshot(v1::TopSnapshot {
            sessions,
            approvals: Vec::new(),
        })),
    }
}

fn lines(out: &[u8]) -> Vec<serde_json::Value> {
    String::from_utf8(out.to_vec())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).expect("each line is one JSON object"))
        .collect()
}

#[test]
fn with_no_daemon_it_says_waiting_once_and_waits_until_one_serves() {
    let source = Source {
        absent: RefCell::new(3),
        feeds: RefCell::new(VecDeque::from([vec![Ok(snapshot(Vec::new()))]])),
        waits: RefCell::new(0),
    };
    let mut out = Vec::new();
    top(
        &args(&["top"]),
        &source,
        &NoAnswers,
        Format::Json,
        (true, false),
        &mut out,
    )
    .unwrap();
    let said = lines(&out);
    assert_eq!(said.len(), 2, "waiting once, then the snapshot: {said:?}");
    assert!(said[0].get("waiting").is_some(), "{:?}", said[0]);
    assert_eq!(said[1], serde_json::json!({"snapshot": {}}));
    assert_eq!(*source.waits.borrow(), 3);
}

#[test]
fn a_daemon_lost_under_the_stream_puts_it_back_to_waiting_and_the_next_snapshot_follows() {
    let source = Source {
        absent: RefCell::new(0),
        feeds: RefCell::new(VecDeque::from([
            vec![Ok(snapshot(Vec::new())), Err(())],
            vec![Ok(snapshot(vec![session("a-b-c", "", "claude-code", 1)]))],
        ])),
        waits: RefCell::new(0),
    };
    let mut out = Vec::new();
    top(
        &args(&["top"]),
        &source,
        &NoAnswers,
        Format::Json,
        (true, false),
        &mut out,
    )
    .unwrap();
    let said = lines(&out);
    let kinds: Vec<&str> = said
        .iter()
        .map(|line| line.as_object().unwrap().keys().next().unwrap().as_str())
        .collect();
    assert_eq!(kinds, ["snapshot", "waiting", "snapshot"], "{said:?}");
}

#[test]
fn a_watch_that_ends_ends_the_command() {
    let source = Source {
        absent: RefCell::new(0),
        feeds: RefCell::new(VecDeque::from([vec![Ok(snapshot(Vec::new()))]])),
        waits: RefCell::new(0),
    };
    let mut out = Vec::new();
    top(
        &args(&["top", "calm-dusty-wren"]),
        &source,
        &NoAnswers,
        Format::Json,
        (true, false),
        &mut out,
    )
    .unwrap();
    assert_eq!(lines(&out).len(), 1);
}

#[test]
fn the_snapshot_is_printed_in_the_sorted_order() {
    let source = Source {
        absent: RefCell::new(0),
        feeds: RefCell::new(VecDeque::from([vec![Ok(snapshot(vec![
            session("brisk-copper-moth", "reviewer", "claude-code", 10),
            session("quiet-silver-fern", "fixer", "opencode", 20),
        ]))]])),
        waits: RefCell::new(0),
    };
    let mut out = Vec::new();
    top(
        &args(&["top", "--sort", "started:asc"]),
        &source,
        &NoAnswers,
        Format::Json,
        (true, false),
        &mut out,
    )
    .unwrap();
    let said = lines(&out);
    assert!(said[0]["snapshot"]["sessions"][0]["name"]
        .as_str()
        .unwrap()
        .ends_with("brisk-copper-moth"));
}

#[test]
fn text_off_a_terminal_is_refused_naming_json() {
    let source = Source {
        absent: RefCell::new(0),
        feeds: RefCell::new(VecDeque::new()),
        waits: RefCell::new(0),
    };
    let refused = top(
        &args(&["top"]),
        &source,
        &NoAnswers,
        Format::Text,
        (false, false),
        &mut Vec::new(),
    )
    .unwrap_err();
    assert_eq!(
        refused.failure.cause.code(),
        "session-cli-top-needs-terminal"
    );
}
