//! The screen's model: the frames it makes from what the watch said, and
//! what keys do to it.

use daemon_proto::v1;

use super::{Effect, Key, Screen, WAITING};
use crate::approvals::AlwaysPlan;
use crate::top::Sort;

fn session(harp: &str, owner: &str, agent: &str, started: i64) -> v1::Session {
    v1::Session {
        name: format!("projects/p/sessions/{harp}"),
        owner: match owner {
            "" => String::new(),
            owner => format!("projects/p/sessions/{owner}"),
        },
        agent: agent.to_string(),
        engine: "claude-code".to_string(),
        state: v1::session::State::Live as i32,
        start_time: Some(started)
            .map(|seconds| daemon_proto::google::protobuf::Timestamp { seconds, nanos: 0 }),
        ..v1::Session::default()
    }
}

fn ask(name: &str, child: &str) -> v1::Approval {
    v1::Approval {
        name: format!("projects/p/approvals/{name}"),
        child: format!("projects/p/sessions/{child}"),
        tool_name: "Bash".to_string(),
        input_excerpt: "rm -rf build/".to_string(),
        state: "waiting".to_string(),
        options: ["allow-once", "allow-session", "allow-always", "deny"]
            .map(|id| v1::ApprovalOption {
                id: id.to_string(),
                kind: if id == "allow-always" {
                    "allow_always"
                } else {
                    "allow_once"
                }
                .to_string(),
                label: String::new(),
                rule: String::new(),
            })
            .to_vec(),
        ..v1::Approval::default()
    }
}

fn snapshot(sessions: Vec<v1::Session>, approvals: Vec<v1::Approval>) -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::Snapshot(v1::TopSnapshot {
            sessions,
            approvals,
        })),
    }
}

fn tree() -> Screen {
    let mut screen = Screen::new("this project", None);
    screen.apply(snapshot(
        vec![
            session("amber-quiet-heron", "", "", 10),
            session("brisk-copper-moth", "amber-quiet-heron", "reviewer", 20),
            session("quiet-silver-fern", "brisk-copper-moth", "fixer", 30),
        ],
        vec![ask("ask-a", "brisk-copper-moth")],
    ));
    screen
}

fn names(screen: &Screen) -> Vec<String> {
    screen
        .frame()
        .rows
        .iter()
        .map(|row| {
            row.split_whitespace()
                .next()
                .unwrap_or_default()
                .to_string()
        })
        .collect()
}

#[test]
fn before_a_daemon_serves_the_frame_has_no_row_and_says_it_waits() {
    let mut screen = Screen::new("this project", None);
    assert!(!screen.ready());
    screen.wait();
    assert!(screen.ready());
    let frame = screen.frame();
    assert!(frame.rows.is_empty());
    assert!(frame.status.iter().any(|line| line == WAITING));
}

#[test]
fn a_snapshot_ends_the_waiting_and_fills_the_table() {
    let screen = tree();
    let frame = screen.frame();
    assert!(!frame.status.iter().any(|line| line == WAITING));
    assert_eq!(frame.rows.len(), 3);
    assert_eq!(
        frame.columns.split_whitespace().collect::<Vec<_>>(),
        ["NAME", "STATE", "AGENT", "PROFILES", "TAGS", "ENGINE", "OWNER", "ASKS"]
    );
}

#[test]
fn children_sit_under_their_owner_indented_and_the_first_row_is_marked() {
    let text = tree().frame().text();
    let rows: Vec<&str> = text.lines().filter(|l| l.contains("-")).collect();
    assert!(rows[0].starts_with("> amber-quiet-heron"), "{text}");
    assert!(rows[1].starts_with("    brisk-copper-moth"), "{text}");
    assert!(rows[2].starts_with("      quiet-silver-fern"), "{text}");
    assert_eq!(
        text.lines().filter(|l| l.starts_with("> ")).count(),
        1,
        "{text}"
    );
    assert!(text.ends_with("---\n"));
}

#[test]
fn a_row_counts_the_asks_waiting_on_it() {
    let frame = tree().frame();
    assert_eq!(frame.rows[1].split_whitespace().last(), Some("1"));
    assert_eq!(frame.rows[0].split_whitespace().last(), Some("0"));
}

#[test]
fn moving_down_selects_the_next_row_and_the_asks_section_follows() {
    let mut screen = tree();
    assert_eq!(screen.key(Key::Down), None);
    let text = screen.frame().text();
    assert!(text.contains("--- asks of brisk-copper-moth"), "{text}");
    assert!(text.contains("ask-a Bash rm -rf build/"), "{text}");
    for option in [
        "1 allow-once",
        "2 allow-session",
        "3 allow-always",
        "4 deny",
    ] {
        assert!(text.contains(option), "{option} in {text}");
    }
    screen.key(Key::Down);
    assert!(screen
        .frame()
        .text()
        .contains("--- asks of quiet-silver-fern"));
}

#[test]
fn a_digit_in_the_asks_chooses_that_option_of_the_ask() {
    let mut screen = tree();
    screen.key(Key::Down);
    screen.key(Key::Tab);
    let effect = screen.key(Key::Digit(4));
    assert_eq!(
        effect,
        Some(Effect::Choose {
            ask: ask("ask-a", "brisk-copper-moth"),
            option: "deny".to_string()
        })
    );
}

#[test]
fn a_digit_for_an_ask_settled_meanwhile_says_how_it_was_settled() {
    let mut screen = tree();
    screen.apply(v1::TopEvent {
        event: Some(v1::top_event::Event::ApprovalSettled(
            v1::TopApprovalSettled {
                approval: Some(v1::Approval {
                    state: "answered".to_string(),
                    option: "deny".to_string(),
                    ..ask("ask-a", "brisk-copper-moth")
                }),
            },
        )),
    });
    screen.key(Key::Down);
    assert_eq!(screen.key(Key::Digit(1)), None);
    let text = screen.frame().text();
    assert!(text.contains("already settled, answered (deny)"), "{text}");
}

#[test]
fn a_digit_past_the_options_chooses_nothing() {
    let mut screen = tree();
    screen.key(Key::Down);
    assert_eq!(screen.key(Key::Digit(9)), None);
}

#[test]
fn a_confirmation_shows_the_rule_and_its_file_and_n_leaves_the_ask_alone() {
    let mut screen = tree();
    screen.confirm(
        ask("ask-a", "brisk-copper-moth"),
        "allow-always",
        AlwaysPlan {
            path: ".ctxloom/config.yaml".to_string(),
            key: "agents.default.engines.claude-code.permissions".to_string(),
            rule: "Bash(rm -rf build/)".to_string(),
            document: "{}".to_string(),
        },
    );
    let text = screen.frame().text();
    for shown in [".ctxloom/config.yaml", "Bash(rm -rf build/)", "[y/N]"] {
        assert!(text.contains(shown), "{shown} in {text}");
    }
    assert_eq!(screen.key(Key::Char('n')), None);
    assert!(!screen.frame().text().contains("[y/N]"));
}

#[test]
fn a_confirmation_is_dropped_when_its_ask_settles_elsewhere() {
    let mut screen = tree();
    screen.confirm(
        ask("ask-a", "brisk-copper-moth"),
        "allow-always",
        AlwaysPlan::default(),
    );
    screen.apply(v1::TopEvent {
        event: Some(v1::top_event::Event::ApprovalSettled(
            v1::TopApprovalSettled {
                approval: Some(v1::Approval {
                    state: "answered".to_string(),
                    ..ask("ask-a", "brisk-copper-moth")
                }),
            },
        )),
    });
    let text = screen.frame().text();
    assert!(!text.contains("[y/N]"), "{text}");
    assert!(text.contains("settled meanwhile"), "{text}");
    assert_eq!(screen.key(Key::Char('y')), None);
}

#[test]
fn y_confirms_the_pending_answer() {
    let mut screen = tree();
    screen.confirm(ask("ask-a", "x"), "allow-always", AlwaysPlan::default());
    assert!(matches!(
        screen.key(Key::Char('y')),
        Some(Effect::Confirmed { .. })
    ));
}

#[test]
fn a_settled_ask_is_shown_settled_and_no_longer_counted() {
    let mut screen = tree();
    screen.apply(v1::TopEvent {
        event: Some(v1::top_event::Event::ApprovalSettled(
            v1::TopApprovalSettled {
                approval: Some(v1::Approval {
                    state: "answered".to_string(),
                    option: "allow-once".to_string(),
                    ..ask("ask-a", "brisk-copper-moth")
                }),
            },
        )),
    });
    screen.key(Key::Down);
    let text = screen.frame().text();
    assert!(text.contains("answered allow-once"), "{text}");
    assert_eq!(screen.frame().rows[1].split_whitespace().last(), Some("0"));
}

#[test]
fn a_filter_typed_narrows_the_rows_and_an_empty_one_widens_them() {
    let mut screen = tree();
    screen.key(Key::Char('f'));
    screen.key(Key::Text("agent=reviewer".to_string()));
    assert_eq!(names(&screen), ["brisk-copper-moth"]);
    assert!(screen.frame().header.contains("agent=reviewer"));
    screen.key(Key::Char('f'));
    screen.key(Key::Text(String::new()));
    assert_eq!(names(&screen).len(), 3);
}

#[test]
fn a_filter_typed_key_by_key_ends_on_enter() {
    let mut screen = tree();
    screen.key(Key::Char('f'));
    for c in "agent=fixer".chars() {
        screen.key(Key::Char(c));
    }
    screen.key(Key::Enter);
    assert_eq!(names(&screen), ["quiet-silver-fern"]);
}

#[test]
fn s_sorts_flat_by_the_next_column_and_r_reverses_it() {
    let mut screen = tree();
    screen.key(Key::Char('s'));
    let flat = screen.frame().text();
    assert!(flat.contains("sort: name:asc"), "{flat}");
    assert_eq!(
        names(&screen),
        [
            "amber-quiet-heron",
            "brisk-copper-moth",
            "quiet-silver-fern"
        ]
    );
    assert!(screen.frame().rows.iter().all(|row| !row.starts_with(' ')));
    screen.key(Key::Char('r'));
    assert_eq!(names(&screen)[0], "quiet-silver-fern");
}

#[test]
fn a_given_sort_orders_the_rows_and_the_header_names_it() {
    let mut screen = Screen::new("this project", Some(Sort::of(Some("agent")).unwrap()));
    screen.apply(snapshot(
        vec![
            session("brisk-copper-moth", "", "reviewer", 1),
            session("quiet-silver-fern", "", "fixer", 2),
        ],
        vec![],
    ));
    assert_eq!(names(&screen), ["quiet-silver-fern", "brisk-copper-moth"]);
    assert!(screen.frame().header.contains("agent:asc"));
    screen.key(Key::Char('r'));
    assert_eq!(names(&screen), ["brisk-copper-moth", "quiet-silver-fern"]);
}

#[test]
fn the_log_section_lists_the_selected_sessions_entries_oldest_first() {
    let mut screen = tree();
    screen.key(Key::Down);
    for (event, text) in [
        ("started", "started on claude-code"),
        ("exited", "run 1 ended"),
    ] {
        screen.apply(v1::TopEvent {
            event: Some(v1::top_event::Event::Log(v1::TopLog {
                session: "projects/p/sessions/brisk-copper-moth".to_string(),
                kind: v1::top_log::Kind::Daemon as i32,
                event: event.to_string(),
                text: text.to_string(),
                ..v1::TopLog::default()
            })),
        });
    }
    let text = screen.frame().text();
    let started = text.find("started started on").expect("started");
    let exited = text.find("exited run 1 ended").expect("exited");
    assert!(started < exited, "{text}");
}

#[test]
fn q_quits() {
    assert_eq!(tree().key(Key::Char('q')), Some(Effect::Quit));
}

#[test]
fn the_seam_names_its_keys() {
    assert_eq!(Key::named("down"), Some(Key::Down));
    assert_eq!(Key::named("3"), Some(Key::Digit(3)));
    assert_eq!(Key::named("y"), Some(Key::Char('y')));
    assert_eq!(Key::named("what"), None);
}
