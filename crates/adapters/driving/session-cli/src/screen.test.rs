//! The screen's model: the frames it makes from what the watch said, and
//! what keys do to it.

use daemon_proto::v1;
use daemon_proto::v1::log_lifecycle::Kind;

use super::{Effect, Key, Screen, ICON_ASCII, ICON_UTF8, WAITING};
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
        ["NAME", "STATE", "AGENT", "PROFILES", "TAGS", "ENGINE", "OWNER"]
    );
}

#[test]
fn children_sit_under_their_owner_indented_and_the_first_row_is_marked() {
    let text = tree().frame().text();
    let rows: Vec<&str> = text
        .lines()
        .filter(|l| l.contains("-") && !l.starts_with("highlighted"))
        .collect();
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
fn a_row_with_an_ask_waiting_is_in_the_ask_state_instead_of_its_own() {
    let frame = tree().frame();
    let ask = format!("{ICON_ASCII} ASK");
    assert!(frame.rows[1].contains(&ask), "{}", frame.rows[1]);
    assert!(!frame.rows[1].contains("live"), "{}", frame.rows[1]);
    assert!(frame.rows[0].contains("live"), "{}", frame.rows[0]);
    assert!(!frame.rows[0].contains("ASK"), "{}", frame.rows[0]);
}

#[test]
fn test_the_ask_state_says_the_count_when_more_than_one_waits() {
    let mut screen = tree();
    screen.apply(asked("ask-b", "brisk-copper-moth"));
    let frame = screen.frame();
    assert!(
        frame.rows[1].contains(&format!("{ICON_ASCII} ASK x2")),
        "{}",
        frame.rows[1]
    );
}

#[test]
fn test_the_state_goes_back_to_the_sessions_own_when_the_ask_settles() {
    let mut screen = tree();
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
    let row = &screen.frame().rows[1];
    assert!(row.contains("live") && !row.contains("ASK"), "{row}");
}

#[test]
fn test_the_frame_carries_no_colour_code_or_escape() {
    let text = tree().frame().text();
    assert!(!text.contains('\u{1b}'), "{text:?}");
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
    let row = &screen.frame().rows[1];
    assert!(row.contains("live") && !row.contains("ASK"), "{row}");
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
    for kind in [Kind::Started, Kind::Exited] {
        screen.apply(lifecycle_log("brisk-copper-moth", kind));
    }
    let text = screen.frame().text();
    let started = text.find("started").expect("started");
    let exited = text.find("run 1 ended").expect("exited");
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

fn removed(harp: &str, reason: v1::top_session_removed::Reason) -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::SessionRemoved(
            v1::TopSessionRemoved {
                name: format!("projects/p/sessions/{harp}"),
                reason: reason as i32,
            },
        )),
    }
}

fn lifecycle_log(harp: &str, kind: Kind) -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::Log(v1::TopLog {
            session: format!("projects/p/sessions/{harp}"),
            entry: Some(v1::LogEntry {
                entry: Some(v1::log_entry::Entry::Lifecycle(v1::LogLifecycle {
                    kind: kind as i32,
                    run: 1,
                    cause: "runner-exit".to_string(),
                    engine: "claude-code".to_string(),
                })),
                ..v1::LogEntry::default()
            }),
            ..v1::TopLog::default()
        })),
    }
}

#[test]
fn test_the_selected_row_that_leaves_the_view_stays_marked_ended_with_its_last_words() {
    let mut screen = tree();
    screen.key(Key::Down);
    assert_eq!(screen.frame().asks_title, "--- asks of brisk-copper-moth");
    screen.apply(removed(
        "brisk-copper-moth",
        v1::top_session_removed::Reason::LeftFilter,
    ));
    screen.apply(lifecycle_log("brisk-copper-moth", Kind::Exited));
    let frame = screen.frame();
    let row = frame
        .rows
        .iter()
        .find(|row| row.contains("brisk-copper-moth"))
        .expect("the row stays while it is selected");
    assert!(row.contains("ended"), "{row}");
    assert_eq!(frame.log_title, "--- log of brisk-copper-moth");
    assert!(frame.log.iter().any(|line| line.contains("run 1 ended")));
}

#[test]
fn test_the_row_that_left_goes_when_the_selection_moves() {
    let mut screen = tree();
    screen.key(Key::Down);
    screen.apply(removed(
        "brisk-copper-moth",
        v1::top_session_removed::Reason::LeftFilter,
    ));
    screen.key(Key::Down);
    assert!(!names(&screen).contains(&"brisk-copper-moth".to_string()));
}

#[test]
fn test_a_row_that_leaves_while_another_is_selected_goes_at_once() {
    let mut screen = tree();
    screen.apply(removed(
        "brisk-copper-moth",
        v1::top_session_removed::Reason::LeftFilter,
    ));
    assert!(!names(&screen).contains(&"brisk-copper-moth".to_string()));
}

#[test]
fn test_a_destroyed_session_goes_even_when_selected() {
    let mut screen = tree();
    screen.key(Key::Down);
    screen.apply(removed(
        "brisk-copper-moth",
        v1::top_session_removed::Reason::Destroyed,
    ));
    assert!(!names(&screen).contains(&"brisk-copper-moth".to_string()));
}

#[test]
fn test_a_kept_row_that_comes_back_live_is_not_dropped_when_the_selection_moves() {
    let mut screen = tree();
    screen.key(Key::Down);
    screen.apply(removed(
        "brisk-copper-moth",
        v1::top_session_removed::Reason::LeftFilter,
    ));
    screen.apply(v1::TopEvent {
        event: Some(v1::top_event::Event::SessionChanged(
            v1::TopSessionChanged {
                session: Some(session(
                    "brisk-copper-moth",
                    "amber-quiet-heron",
                    "reviewer",
                    20,
                )),
            },
        )),
    });
    screen.key(Key::Down);
    assert!(names(&screen).contains(&"brisk-copper-moth".to_string()));
}

fn asked(name: &str, child: &str) -> v1::TopEvent {
    v1::TopEvent {
        event: Some(v1::top_event::Event::ApprovalAsked(v1::TopApprovalAsked {
            approval: Some(ask(name, child)),
        })),
    }
}

#[test]
fn test_a_row_with_an_ask_waiting_is_highlighted_and_named_in_the_text_frame() {
    let frame = tree().frame();
    assert_eq!(frame.alerts, [1]);
    let text = frame.text();
    assert!(
        text.lines().any(|l| l == "highlighted: brisk-copper-moth"),
        "{text}"
    );
}

#[test]
fn test_no_row_is_highlighted_when_no_ask_waits() {
    let mut screen = Screen::new("this project", None);
    screen.apply(snapshot(
        vec![session("amber-quiet-heron", "", "", 10)],
        vec![],
    ));
    let frame = screen.frame();
    assert!(frame.alerts.is_empty());
    assert!(!frame.text().contains("highlighted"));
}

#[test]
fn test_the_asks_in_the_snapshot_at_open_ring_no_bell() {
    let mut screen = tree();
    assert!(!screen.take_bell());
}

#[test]
fn test_a_new_ask_rings_the_bell_once() {
    let mut screen = tree();
    screen.apply(asked("ask-b", "quiet-silver-fern"));
    assert!(screen.take_bell());
    assert!(!screen.take_bell(), "taken once");
}

#[test]
fn test_the_same_ask_told_again_rings_no_second_bell() {
    let mut screen = tree();
    screen.apply(asked("ask-b", "quiet-silver-fern"));
    assert!(screen.take_bell());
    screen.apply(asked("ask-b", "quiet-silver-fern"));
    assert!(!screen.take_bell());
}

#[test]
fn test_an_ask_of_the_snapshot_told_again_as_asked_rings_no_bell() {
    let mut screen = tree();
    screen.apply(asked("ask-a", "brisk-copper-moth"));
    assert!(!screen.take_bell());
}

#[test]
fn test_a_snapshot_after_a_reconnect_rings_no_bell_for_asks_it_holds() {
    let mut screen = tree();
    screen.apply(snapshot(
        vec![session("brisk-copper-moth", "", "", 20)],
        vec![
            ask("ask-a", "brisk-copper-moth"),
            ask("ask-c", "brisk-copper-moth"),
        ],
    ));
    assert!(!screen.take_bell());
}

#[test]
fn test_the_frame_says_a_bell_in_a_line_of_its_own() {
    let mut frame = tree().frame();
    assert!(!frame.text().lines().any(|l| l == "bell"));
    frame.bell = true;
    assert!(frame.text().lines().any(|l| l == "bell"));
}

#[test]
fn test_the_raised_hand_takes_two_columns_and_the_table_stays_aligned() {
    use ratatui::text::Span;
    let mut screen = Screen::new("this project", None).with_icon(ICON_UTF8);
    screen.apply(snapshot(
        vec![
            session("amber-quiet-heron", "", "", 10),
            session("brisk-copper-moth", "amber-quiet-heron", "reviewer", 20),
        ],
        vec![ask("ask-a", "brisk-copper-moth")],
    ));
    let frame = screen.frame();
    assert!(frame.rows[1].contains("\u{270b} ASK"), "{}", frame.rows[1]);
    let before = |line: &str, word: &str| Span::raw(&line[..line.find(word).expect(word)]).width();
    assert_eq!(
        before(&frame.rows[1], "reviewer"),
        before(&frame.columns, "AGENT"),
        "{frame:?}"
    );
    assert_eq!(
        before(&frame.rows[1], "claude-code"),
        before(&frame.columns, "ENGINE")
    );
}

#[test]
fn test_the_seam_icon_is_ascii_until_the_display_chooses_another() {
    let frame = tree().frame();
    assert!(frame.rows[1].contains("! ASK"), "{}", frame.rows[1]);
    assert!(frame.rows[1].is_ascii());
}
