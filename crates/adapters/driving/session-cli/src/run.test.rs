//! The `session` commands against a fake of the application: what each
//! flag asks for, and what each answer looks like in json and in text.
//! Ported from Go's `session_row_test.go`, `session_query_test.go`,
//! `session_state_test.go` and session.feature (release/0.7); the rows are
//! D16's.

use clap::{Args, Command, FromArgMatches};
use cli_support::Exit;
use presentation::Format;
use serde_json::{json, Value};
use sessions_application::Refusal;

use daemon_proto::v1;

use super::run;
use crate::Session;

// 2026-07-17T12:00:00Z.
const STARTED: i64 = 1_784_289_600;

fn ts(seconds: i64) -> sessions_application::Timestamp {
    sessions_application::Timestamp { seconds, nanos: 0 }
}

/// What the fake answers with and what it was asked.
#[derive(Default)]
struct Fake {
    transcript: Vec<v1::LogEntry>,
    wt_report: Option<isolation_application::Report>,
    /// What an applied worktree question answers, when it differs from the
    /// plan (`wt_report`): a removal's outcome.
    wt_applied: Option<isolation_application::Report>,
    wt_asked: Vec<(Option<String>, bool)>,
    /// Every worktree question and every removal, in the order asked.
    log: Vec<&'static str>,
    /// Answer every worktree question with a refusal that is not "no session".
    wt_fails: bool,
}

impl isolation_application::WorktreeUse for Fake {
    fn worktrees(
        &mut self,
        harp: Option<&str>,
        apply: bool,
    ) -> Result<isolation_application::Report, isolation_application::IsolationError> {
        self.wt_asked.push((harp.map(str::to_string), apply));
        self.log.push(if apply {
            "worktrees apply"
        } else {
            "worktrees plan"
        });
        if self.wt_fails {
            return Err(isolation_application::IsolationError::NothingRemoved {
                harp: harp.unwrap_or_default().to_string(),
                spared: 0,
                skipped: 0,
            });
        }
        let answer = if apply {
            self.wt_applied.clone().or_else(|| self.wt_report.clone())
        } else {
            self.wt_report.clone()
        };
        answer.ok_or(isolation_application::IsolationError::NoSession {
            harp: harp.unwrap_or_default().to_string(),
        })
    }
}

/// The parsed `session ...` of `words`.
fn parsed(words: &[&str]) -> Session {
    let command = Session::augment_args(Command::new("session")).no_binary_name(true);
    let matches = command
        .try_get_matches_from(words.iter().map(|w| w.to_string()))
        .unwrap_or_else(|e| panic!("{e}"));
    Session::from_arg_matches(&matches).unwrap()
}

struct Ran {
    out: String,
    result: Result<crate::Done, crate::Failure>,
}

fn go(fake: &mut Fake, format: Format, words: &[&str]) -> Ran {
    let session = parsed(words);
    let mut out = Vec::new();
    let result = run(&session, fake, format, &mut out);
    Ran {
        out: String::from_utf8(out).unwrap(),
        result,
    }
}

fn json_of(ran: &Ran) -> Value {
    assert!(
        ran.result.is_ok(),
        "{:?}",
        ran.result
            .as_ref()
            .err()
            .map(|f| f.failure.cause.code().to_string())
    );
    serde_json::from_str(&ran.out).unwrap_or_else(|e| panic!("{e}: {:?}", ran.out))
}

// -------------------------------------------------- list, search, show

/// The daemon's client as a test sets it: the sessions it answers, and what
/// it was asked.
#[derive(Default)]
struct Reads {
    sessions: Vec<v1::Session>,
    /// `list all state`, `search words all state`, `get harp`.
    asked: std::cell::RefCell<Vec<String>>,
    /// Answer every call with this, instead.
    refuse: Option<daemon_api::CallError>,
    /// The transcript's entries `get_transcript` answers.
    transcript: Vec<v1::LogEntry>,
}

impl daemon_api::SessionReads for Reads {
    async fn list_transcripts(
        &self,
        all: bool,
    ) -> Result<v1::ListSessionsResponse, daemon_api::CallError> {
        self.asked.borrow_mut().push(format!("transcripts {all}"));
        if let Some(refused) = &self.refuse {
            return Err(refused.clone());
        }
        Ok(v1::ListSessionsResponse {
            sessions: self.sessions.clone(),
            ..v1::ListSessionsResponse::default()
        })
    }

    async fn list_sessions(
        &self,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, daemon_api::CallError> {
        self.asked
            .borrow_mut()
            .push(format!("list {all} {}", state.as_str_name()));
        if let Some(refused) = &self.refuse {
            return Err(refused.clone());
        }
        Ok(v1::ListSessionsResponse {
            sessions: self.sessions.clone(),
            ..v1::ListSessionsResponse::default()
        })
    }

    async fn search_sessions(
        &self,
        words: Vec<String>,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, daemon_api::CallError> {
        self.asked.borrow_mut().push(format!(
            "search {} {all} {}",
            words.join(" "),
            state.as_str_name()
        ));
        Ok(v1::ListSessionsResponse {
            sessions: self.sessions.clone(),
            ..v1::ListSessionsResponse::default()
        })
    }

    async fn get_session(&self, harp: &str) -> Result<v1::Session, daemon_api::CallError> {
        self.asked.borrow_mut().push(format!("get {harp}"));
        if let Some(refused) = &self.refuse {
            return Err(refused.clone());
        }
        self.sessions
            .first()
            .cloned()
            .ok_or(daemon_api::CallError::Call(daemon_ports_like_unreachable()))
    }

    async fn get_transcript(&self, harp: &str) -> Result<v1::Transcript, daemon_api::CallError> {
        self.asked.borrow_mut().push(format!("transcript {harp}"));
        if let Some(refused) = &self.refuse {
            return Err(refused.clone());
        }
        Ok(v1::Transcript {
            name: format!("projects/calm-otter/sessions/{harp}/transcript"),
            entries: self.transcript.clone(),
        })
    }
}

/// `words` (a `transcript show`) read from the daemon, which answers the
/// fake's transcript, its entries as the daemon keeps them.
fn shown(fake: &Fake, format: Format, words: &[&str]) -> Ran {
    let reads = Reads {
        transcript: fake.transcript.clone(),
        ..Reads::default()
    };
    let ran = read_go(&reads, format, words);
    assert_eq!(
        *reads.asked.borrow(),
        vec![format!("transcript {}", words[2])],
        "the daemon was asked for it"
    );
    ran
}

/// A call that came to nothing (no daemon answered).
fn daemon_ports_like_unreachable() -> daemon_api::Called {
    daemon_api::Called::Unreachable {
        detail: "gone".to_string(),
    }
}

fn read_go(reads: &Reads, format: Format, words: &[&str]) -> Ran {
    let session = parsed(words);
    assert!(crate::is_read(&session), "{words:?} is a read");
    let mut out = Vec::new();
    let result = crate::read(&session, reads, format, &mut out);
    Ran {
        out: String::from_utf8(out).unwrap(),
        result,
    }
}

/// The session `harp` of the project `calm-otter`, as the daemon answers it.
fn session(harp: &str, state: v1::session::State) -> v1::Session {
    v1::Session {
        name: format!("projects/calm-otter/sessions/{harp}"),
        uid: "01010101-0101-0101-0101-010101010101".to_string(),
        state: state as i32,
        engine: "claude-code".to_string(),
        summary: format!("Seeded prompt for {harp}."),
        start_time: Some(ts(STARTED).into()),
        project_root: "/work/proj".to_string(),
        ..v1::Session::default()
    }
}

// Ruling 146: a listing in json is the daemon's answer as its proto3 JSON.
#[test]
fn a_listing_in_json_is_the_daemons_answer() {
    let reads = Reads {
        sessions: vec![session("amber-swift-owl", v1::session::State::Ended)],
        ..Reads::default()
    };
    let doc = json_of(&read_go(&reads, Format::Json, &["list"]));
    assert_eq!(
        doc,
        json!({"sessions": [{
            "name": "projects/calm-otter/sessions/amber-swift-owl",
            "uid": "01010101-0101-0101-0101-010101010101",
            "state": "ENDED",
            "engine": "claude-code",
            "summary": "Seeded prompt for amber-swift-owl.",
            "start_time": "2026-07-17T12:00:00Z",
            "project_root": "/work/proj",
        }]})
    );
}

// session.feature: `session list --all --format text` has a STATE column;
// the live row prints no END while the others print theirs.
#[test]
fn the_text_table_has_a_state_column_and_a_blank_end_while_live() {
    let live = session("amber-swift-owl", v1::session::State::Live);
    let mut ended = session("brisk-copper-moth", v1::session::State::Ended);
    ended.end_time = Some(ts(STARTED + 3600).into());
    ended.kept = true;
    let reads = Reads {
        sessions: vec![live, ended],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["list", "--all"]);
    assert!(ran.result.is_ok());
    let lines: Vec<&str> = ran.out.lines().collect();
    for header in ["HARP", "SUMMARY", "STATE", "START", "END"] {
        assert!(lines[0].contains(header), "{header} in {:?}", lines[0]);
    }
    let live_line = lines
        .iter()
        .find(|l| l.contains("amber-swift-owl"))
        .unwrap();
    let ended_line = lines
        .iter()
        .find(|l| l.contains("brisk-copper-moth"))
        .unwrap();
    assert!(
        live_line.contains("live") && !live_line.contains("13:00:00"),
        "{live_line}"
    );
    assert!(
        ended_line.contains("ended") && ended_line.contains("2026-07-17 13:00:00"),
        "{ended_line}"
    );
    assert!(ended_line.contains("kept (transcript kept at any age)"));
    assert!(live_line.contains("2026-07-17 12:00:00"));
    assert!(live_line.contains("Seeded prompt for amber-swift-owl."));
}

// A title the user set is the row's summary, ahead of the first prompt.
#[test]
fn a_set_title_is_the_rows_summary() {
    let mut titled = session("amber-swift-owl", v1::session::State::Ended);
    titled.title = "the auth refactor".to_string();
    let mut bare = session("brisk-copper-moth", v1::session::State::Ended);
    bare.summary = String::new();
    let reads = Reads {
        sessions: vec![titled, bare],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["list"]);
    assert!(ran.out.contains("the auth refactor"), "{}", ran.out);
    assert!(!ran.out.contains("Seeded prompt for amber"), "{}", ran.out);
    assert!(ran.out.contains("(no summary)"), "{}", ran.out);
}

// session.feature: "A fresh project has recorded nothing": an empty
// listing is the empty message in json, the placeholder in text.
#[test]
fn an_empty_listing_is_an_empty_message_or_the_placeholder() {
    let reads = Reads::default();
    let ran = read_go(&reads, Format::Json, &["list"]);
    assert_eq!(json_of(&ran), json!({}));
    let ran = read_go(&reads, Format::Text, &["list"]);
    assert_eq!(ran.out, "(no sessions)\n");
}

// A bare namespace runs its default verb (D28; Go: groupNodeDefault).
#[test]
fn a_bare_session_lists() {
    let reads = Reads {
        sessions: vec![session("amber-swift-owl", v1::session::State::Ended)],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Json, &[]);
    assert_eq!(
        json_of(&ran)["sessions"][0]["name"],
        "projects/calm-otter/sessions/amber-swift-owl"
    );
    assert_eq!(*reads.asked.borrow(), ["list false STATE_UNSPECIFIED"]);
}

#[test]
fn all_and_state_reach_the_daemon() {
    let reads = Reads::default();
    read_go(&reads, Format::Json, &["list", "--all", "--state", "ended"]);
    read_go(&reads, Format::Json, &["list", "--state", "live"]);
    read_go(&reads, Format::Json, &["list", "--state", "unknown"]);
    assert_eq!(
        *reads.asked.borrow(),
        ["list true ENDED", "list false LIVE", "list false UNKNOWN"]
    );
}

// session.feature: "--state refuses a word that is not a state".
#[test]
fn a_state_that_is_not_one_is_refused_naming_the_states() {
    let reads = Reads::default();
    for words in [
        &["list", "--state", "running"][..],
        &["search", "x", "--state", "running"],
    ] {
        let ran = read_go(&reads, Format::Json, words);
        let failure = ran.result.unwrap_err();
        assert_eq!(failure.failure.exit, Exit::REFUSED);
        assert_eq!(failure.failure.cause.code(), "session-cli-bad-state");
        let states = failure
            .failure
            .cause
            .args()
            .into_iter()
            .find(|(n, _)| *n == "states")
            .map(|(_, a)| format!("{a:?}"))
            .unwrap();
        assert!(states.contains("live, ended, unknown"), "{states}");
    }
    assert!(
        reads.asked.borrow().is_empty(),
        "refused before it asked anything"
    );
}

// Go: runSessionQuery.
#[test]
fn a_search_carries_its_words_and_scope() {
    let reads = Reads {
        sessions: vec![session("amber-swift-owl", v1::session::State::Ended)],
        ..Reads::default()
    };
    let ran = read_go(
        &reads,
        Format::Json,
        &[
            "search",
            "lockfile",
            "migration",
            "--all",
            "--state",
            "live",
        ],
    );
    assert_eq!(
        json_of(&ran)["sessions"][0]["name"],
        "projects/calm-otter/sessions/amber-swift-owl"
    );
    assert_eq!(
        *reads.asked.borrow(),
        ["search lockfile migration true LIVE"]
    );
}

// j001200: "A query that matches nothing says so rather than printing
// nothing": the bytes, not the exit code (Go: TestRunSessionQuery_NoMatches).
#[test]
fn a_search_that_matches_nothing_says_so() {
    let reads = Reads::default();
    let ran = read_go(
        &reads,
        Format::Text,
        &["search", "J001200-MATCHES-ABSOLUTELY-NOTHING"],
    );
    assert!(ran.result.is_ok());
    assert_eq!(ran.out.trim(), "(no sessions)");
    let ran = read_go(&reads, Format::Json, &["search", "nothing"]);
    assert_eq!(ran.out.trim(), "{}");
}

// Go: renderSessionShow.
#[test]
fn show_prints_the_record_one_fact_per_line() {
    let mut s = session("amber-swift-owl", v1::session::State::Ended);
    s.native_session = "native-1".to_string();
    s.end_time = Some(ts(STARTED + 3600).into());
    s.kept = true;
    let reads = Reads {
        sessions: vec![s],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["show", "amber-swift-owl"]);
    assert!(ran.result.is_ok());
    assert_eq!(
        ran.out,
        "amber-swift-owl\n\
         Seeded prompt for amber-swift-owl.\n\
         \n\
         state:    ended\n\
         engine:   claude-code\n\
         native:   native-1\n\
         project:  /work/proj\n\
         started:  2026-07-17 12:00:00\n\
         ended:    2026-07-17 13:00:00\n\
         kept:     its transcript is kept at any age (`ctxloom session keep --undo` releases it)\n"
    );
    assert_eq!(*reads.asked.borrow(), ["get amber-swift-owl"]);
}

// Go: "absent facts left out rather than printed blank", and a title-less
// session says so: j001200's "Show prints what the session was about".
#[test]
fn show_leaves_out_what_is_absent_and_names_a_session_with_no_title() {
    let mut s = session("amber-swift-owl", v1::session::State::Live);
    s.summary = String::new();
    let reads = Reads {
        sessions: vec![s],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["show", "amber-swift-owl"]);
    assert!(ran.out.contains("(no summary)"), "{}", ran.out);
    for absent in [
        "native:",
        "ended:",
        "kept:",
        "owner:",
        "closed:",
        "children:",
        "pending:",
    ] {
        assert!(!ran.out.contains(absent), "{absent} in {}", ran.out);
    }
}

// Ruling 146: show in json is the session as its proto3 JSON.
#[test]
fn show_in_json_is_the_session() {
    let mut s = session("amber-swift-owl", v1::session::State::Ended);
    s.title = "the auth refactor".to_string();
    let reads = Reads {
        sessions: vec![s],
        ..Reads::default()
    };
    let doc = json_of(&read_go(&reads, Format::Json, &["show", "amber-swift-owl"]));
    assert_eq!(doc["name"], "projects/calm-otter/sessions/amber-swift-owl");
    assert_eq!(doc["state"], "ENDED");
    assert_eq!(doc["title"], "the auth refactor");
    assert!(doc.get("end_time").is_none(), "{doc}");
}

// D30, D-N35: show lists the pending messages, the owner, the close and the
// children, each by what the daemon answered.
#[test]
fn show_in_text_names_the_owner_the_close_the_children_and_the_pending() {
    let mut s = session("brisk-copper-moth", v1::session::State::Live);
    s.owner = "projects/calm-otter/sessions/amber-quiet-heron".to_string();
    s.closed = true;
    s.children = vec!["projects/calm-otter/sessions/calm-dusty-wren".to_string()];
    s.pending = vec![
        v1::PendingMessage {
            message_id: "m1".to_string(),
            urgency: "inform".to_string(),
            text: "when you are next on\nmore".to_string(),
            sender: String::new(),
        },
        v1::PendingMessage {
            message_id: "m2".to_string(),
            urgency: "wake".to_string(),
            text: "now".to_string(),
            sender: String::new(),
        },
    ];
    let reads = Reads {
        sessions: vec![s],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["show", "brisk-copper-moth"]);
    for line in [
        "owner:    projects/calm-otter/sessions/amber-quiet-heron",
        "closed:   yes",
        "children: 1 held",
        "  projects/calm-otter/sessions/calm-dusty-wren",
        "pending:  2 messages no turn has carried yet",
        "  m1 (inform): when you are next on",
        "  m2 (wake): now",
    ] {
        assert!(ran.out.contains(line), "{line:?} in {}", ran.out);
    }
    assert!(!ran.out.contains("more"), "{}", ran.out);
}

// Step 9 (owner ruling 2026-11-17): show says what the session's latest run
// was launched with, its servers by name and its permissions document, and
// how its last turn failed, in the notice's words.
#[test]
fn show_in_text_names_what_its_run_was_given_and_how_its_last_turn_failed() {
    let mut s = session("brisk-copper-moth", v1::session::State::Live);
    s.grant = Some(v1::SessionGrant {
        mcp_servers: vec!["ctxloom".to_string(), "review-tools".to_string()],
        permissions: r#"{"defaultMode":"plan"}"#.to_string(),
    });
    s.last_failure = Some(v1::SessionFailure {
        kind: "rate-limited".to_string(),
        detail: "429".to_string(),
        notice: Some(v1::Notice {
            code: "daemon-api-turn-rate-limited".to_string(),
            args: Vec::new(),
            text: "its last turn ran into its rate limit".to_string(),
        }),
    });
    let reads = Reads {
        sessions: vec![s],
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Text, &["show", "brisk-copper-moth"]);
    for line in [
        "granted:  MCP servers ctxloom, review-tools",
        r#"          permissions {"defaultMode":"plan"}"#,
        "failed:   rate-limited: its last turn ran into its rate limit",
        "          429",
    ] {
        assert!(ran.out.contains(line), "{line:?} in {}", ran.out);
    }
    // A session whose run journaled no grant, and no failure, says neither.
    let plain = Reads {
        sessions: vec![session("calm-dusty-wren", v1::session::State::Live)],
        ..Reads::default()
    };
    let ran = read_go(&plain, Format::Text, &["show", "calm-dusty-wren"]);
    assert!(!ran.out.contains("granted:"), "{}", ran.out);
    assert!(!ran.out.contains("failed:"), "{}", ran.out);
}

// A call that came to nothing is the command's failure, with its exit.
#[test]
fn a_failed_call_is_the_commands_failure_with_its_exit_code() {
    let reads = Reads {
        refuse: Some(daemon_api::CallError::Unreached(
            daemon_api::Unreached::NotStarted {
                log: "~/.ctxloom/daemon/daemon.log".to_string(),
            },
        )),
        ..Reads::default()
    };
    for words in [&["show", "no-such-harp"][..], &["list"]] {
        let failure = read_go(&reads, Format::Json, words).result.unwrap_err();
        assert_eq!(failure.failure.cause.code(), "daemon-ports-not-started");
        assert_eq!(failure.failure.exit, Exit::FAILED);
    }
}

// Every verb but the reads runs on the sessions use cases; the reads are
// list, search, show and `transcript show` (owner, H3 follow-up: a session
// read reaches the daemon, so its flush runs first).
#[test]
fn only_list_search_show_and_transcript_show_are_the_daemons_reads() {
    for words in [&["keep", "a"][..], &["rename", "a", "b"], &["import"]] {
        assert!(!crate::is_read(&parsed(words)), "{words:?}");
    }
    for words in [
        &["list"][..],
        &["show", "a"],
        &["transcript", "show", "a"],
        &["transcript"],
        &["transcript", "list"],
    ] {
        assert!(crate::is_read(&parsed(words)), "{words:?}");
    }
}

// A transcript the daemon refuses (none recorded, no such session) fails the
// read and writes nothing to stdout.
#[test]
fn a_transcript_the_daemon_refuses_writes_nothing() {
    let reads = Reads {
        refuse: Some(daemon_api::CallError::Call(daemon_ports_like_unreachable())),
        ..Reads::default()
    };
    let ran = read_go(&reads, Format::Json, &["transcript", "show", "h"]);
    assert!(ran.result.is_err());
    assert_eq!(ran.out, "");
}

// ------------------------------------------------------------------------ keep

// Go: runSessionKeep: `{harp, kept}`, and the two sentences; ONE WRITER: a
// command sent to the daemon, as the user, one per call.
#[test]
fn keep_and_undo_say_where_the_mark_stands() {
    let writes = Writes::default();
    let ran = child_go(&writes, Format::Json, &["keep", "amber-swift-owl"]);
    assert_eq!(
        serde_json::from_str::<Value>(&ran.out).unwrap(),
        json!({"harp": "amber-swift-owl", "kept": true})
    );
    let ran = child_go(&writes, Format::Text, &["keep", "amber-swift-owl"]);
    assert_eq!(
        ran.out,
        "amber-swift-owl is marked keep: its transcript is kept at any age\n"
    );
    let ran = child_go(
        &writes,
        Format::Text,
        &["keep", "amber-swift-owl", "--undo"],
    );
    assert_eq!(
        ran.out,
        "amber-swift-owl is no longer marked keep: its transcript is reclaimable once session_transcript_retention has passed since it ended\n"
    );
    assert_eq!(
        *writes.asked.lock().unwrap(),
        [
            "keep amber-swift-owl true",
            "keep amber-swift-owl true",
            "keep amber-swift-owl false"
        ]
    );
}

// ---------------------------------------------------------------------- rename

// S55 (owner, 2026-11-12, slice 2): `session rename <harp> <title>` sets the
// title; the harp is permanent. ONE WRITER: the command goes to the daemon.
#[test]
fn rename_sets_the_title_and_says_so() {
    let writes = Writes::default();
    let ran = child_go(
        &writes,
        Format::Json,
        &["rename", "amber-swift-owl", "the auth refactor"],
    );
    assert_eq!(
        serde_json::from_str::<Value>(&ran.out).unwrap(),
        json!({"harp": "amber-swift-owl", "title": "the auth refactor", "changed": true})
    );
    let ran = child_go(
        &writes,
        Format::Text,
        &["rename", "amber-swift-owl", "the auth refactor"],
    );
    assert_eq!(ran.out, "amber-swift-owl: titled \"the auth refactor\"\n");
    assert_eq!(
        *writes.asked.lock().unwrap(),
        [
            "title amber-swift-owl \"the auth refactor\"",
            "title amber-swift-owl \"the auth refactor\""
        ]
    );
}

#[test]
fn the_harp_changing_edit_is_gone() {
    assert!(Session::augment_args(Command::new("session"))
        .try_get_matches_from(["session", "edit", "amber-swift-owl", "--name", "x"])
        .is_err());
}

// ---------------------------------------------------------------------- remove
// `session remove` and `session sweep` are the daemon's (owner,
// 2026-11-12): the command asks its client, and renders what it answered.

/// What the daemon answers a destroy with, and what it was asked.
#[derive(Default)]
struct Daemon {
    removed: Option<daemon_api::Removed>,
    swept: Option<v1::SweepSessionsResponse>,
    /// Each delete asked: harp, yes, even_if_live.
    deletes: std::cell::RefCell<Vec<(String, bool, bool)>>,
    /// Each sweep asked: all, older_than, yes.
    sweeps: std::cell::RefCell<Vec<(bool, Option<String>, bool)>>,
    /// This process's holds, by session resource name (s4-Q14, H4).
    held: std::collections::BTreeMap<String, reclaim_application::HeldOf>,
    /// The holds' reads and discards fail.
    holds_fail: bool,
    /// Each hold discarded, by name.
    discarded: std::cell::RefCell<Vec<String>>,
    /// The person at the terminal and her answer (step 10c U11); none, no
    /// terminal.
    terminal: Option<bool>,
    /// Each question asked at the terminal.
    asked: std::cell::RefCell<Vec<String>>,
    /// Each apply of a preview sent: harp, even_if_live, etag, answers.
    applies: std::cell::RefCell<Vec<Applied>>,
}

/// An apply of a preview, as sent: harp, even_if_live, etag, answers.
type Applied = (String, bool, String, Vec<v1::Answer>);

impl crate::Asking for Daemon {
    fn is_terminal(&self) -> bool {
        self.terminal.is_some()
    }

    fn confirm(&self, question: &str) -> bool {
        self.asked.borrow_mut().push(question.to_string());
        self.terminal == Some(true)
    }
}

fn hold_failure() -> reclaim_application::HoldError {
    reclaim_application::HoldError::Io {
        path: "/h/.ctxloom/held".to_string(),
        ext_err_txt: "Permission denied (os error 13)".to_string(),
    }
}

impl reclaim_application::SessionHolds for Daemon {
    fn held(
        &self,
        name: &str,
    ) -> Result<Option<reclaim_application::HeldOf>, reclaim_application::HoldError> {
        if self.holds_fail {
            return Err(hold_failure());
        }
        Ok(self.held.get(name).copied())
    }

    fn discard(&self, name: &str) -> Result<(), reclaim_application::HoldError> {
        if self.holds_fail {
            return Err(hold_failure());
        }
        self.discarded.borrow_mut().push(name.to_string());
        Ok(())
    }
}

impl daemon_api::SessionRemovals for Daemon {
    async fn delete_session(
        &self,
        harp: &str,
        yes: bool,
        even_if_live: bool,
    ) -> Result<daemon_api::Removed, daemon_api::CallError> {
        self.deletes
            .borrow_mut()
            .push((harp.to_string(), yes, even_if_live));
        self.removed
            .clone()
            .ok_or(daemon_api::CallError::Call(daemon_api::Called::Failed {
                code: 13,
                detail: "boom".to_string(),
            }))
    }

    async fn apply_removal(
        &self,
        harp: &str,
        even_if_live: bool,
        etag: &str,
        answers: Vec<v1::Answer>,
    ) -> Result<daemon_api::Removed, daemon_api::CallError> {
        self.applies
            .borrow_mut()
            .push((harp.to_string(), even_if_live, etag.to_string(), answers));
        Ok(daemon_api::Removed::Done(plan(true)))
    }

    async fn sweep_sessions(
        &self,
        all: bool,
        older_than: Option<String>,
        yes: bool,
    ) -> Result<v1::SweepSessionsResponse, daemon_api::CallError> {
        self.sweeps.borrow_mut().push((all, older_than, yes));
        Ok(self.swept.clone().unwrap_or_default())
    }
}

fn destroy_go(daemon: &Daemon, format: Format, words: &[&str]) -> Ran {
    let session = parsed(words);
    assert!(crate::is_destroy(&session), "{words:?}");
    let mut out = Vec::new();
    let result = crate::destroy(&session, daemon, daemon, daemon, format, &mut out);
    Ran {
        out: String::from_utf8(out).unwrap(),
        result,
    }
}

fn plan(applied: bool) -> v1::DeleteSessionResponse {
    v1::DeleteSessionResponse {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        harp: "amber-swift-owl".to_string(),
        applied: Some(applied),
        authored: vec!["notes.md".to_string()],
        files: vec![
            v1::RemovalFile {
                path: "/h/.ctxloom/sessions/amber-swift-owl/native/history.jsonl".to_string(),
                rel: "native/history.jsonl".to_string(),
                class: v1::removal_file::Class::Machine as i32,
                bytes: 9,
            },
            v1::RemovalFile {
                path: "/h/.ctxloom/sessions/amber-swift-owl/notes.md".to_string(),
                rel: "notes.md".to_string(),
                class: v1::removal_file::Class::Authored as i32,
                bytes: 4,
            },
        ],
        bytes_freed: if applied { 13 } else { 0 },
        events_destroyed: if applied { 7 } else { 0 },
        ..v1::DeleteSessionResponse::default()
    }
}

fn done_with(plan: v1::DeleteSessionResponse) -> Daemon {
    Daemon {
        removed: Some(daemon_api::Removed::Done(plan)),
        ..Daemon::default()
    }
}

fn refused_with(why: Refusal) -> Daemon {
    Daemon {
        removed: Some(daemon_api::Removed::Refused {
            plan: plan(false),
            why: daemon_api::RemovalRejection::Session(why),
        }),
        ..Daemon::default()
    }
}

/// A preview asking its confirmation, tagged `e-7`, for a daemon whose
/// terminal is `terminal`.
fn asking(terminal: Option<bool>) -> Daemon {
    let mut preview = plan(false);
    preview.etag = "e-7".to_string();
    preview.needs = vec![v1::Need {
        id: "confirm".to_string(),
        kind: Some(v1::need::Kind::Confirm(v1::Confirm {
            text: "Remove session \"amber-swift-owl\"?".to_string(),
            ..v1::Confirm::default()
        })),
        optional: false,
    }];
    Daemon {
        terminal,
        ..done_with(preview)
    }
}

// Step 10c U11: at her terminal the preview's own question is asked, and
// her yes applies what it showed, its tag and her answer sent back; what is
// said is the removal applied, nothing left that would change.
#[test]
fn at_a_terminal_a_yes_applies_the_preview_with_its_tag_and_her_answer() {
    let daemon = asking(Some(true));
    let ran = destroy_go(&daemon, Format::Json, &["remove", "amber-swift-owl"]);
    assert_eq!(
        *daemon.asked.borrow(),
        ["Remove session \"amber-swift-owl\"?"]
    );
    let confirmed = vec![v1::Answer {
        need_id: "confirm".to_string(),
        answer: Some(v1::answer::Answer::Confirmed(true)),
    }];
    assert_eq!(
        *daemon.applies.borrow(),
        [(
            "amber-swift-owl".to_string(),
            false,
            "e-7".to_string(),
            confirmed
        )]
    );
    assert_eq!(json_of(&ran)["applied"], true);
    let done = ran.result.unwrap();
    assert!(!done.would_change);
    assert!(done
        .notices
        .iter()
        .all(|n| n.code() != "sessions-application-removed-nothing"));
}

// Her no, or no terminal at all, applies nothing: the preview stands.
#[test]
fn a_no_or_no_terminal_applies_nothing() {
    for (terminal, questions) in [(Some(false), 1), (None, 0)] {
        let daemon = asking(terminal);
        let ran = destroy_go(&daemon, Format::Json, &["remove", "amber-swift-owl"]);
        assert_eq!(daemon.asked.borrow().len(), questions, "{terminal:?}");
        assert!(daemon.applies.borrow().is_empty(), "{terminal:?}");
        assert_eq!(json_of(&ran)["applied"], false);
        assert!(ran.result.unwrap().would_change, "{terminal:?}");
    }
}

// `--yes` is one call: nothing is asked, even at a terminal.
#[test]
fn yes_at_a_terminal_asks_nothing() {
    let daemon = asking(Some(true));
    destroy_go(
        &daemon,
        Format::Json,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(daemon.asked.borrow().is_empty());
    assert!(daemon.applies.borrow().is_empty());
}

// Owner ruling 98, ruling 146: `session remove` reports the destroy first:
// the daemon's plan as its proto3 JSON, every file of the session's
// directory, authored ones too, and nothing applied.
#[test]
fn a_removal_report_is_the_plan_and_says_nothing_was_removed() {
    let daemon = done_with(plan(false));
    let ran = destroy_go(&daemon, Format::Json, &["remove", "amber-swift-owl"]);
    let doc = json_of(&ran);
    assert_eq!(doc["harp"], "amber-swift-owl");
    // OQ-7: `applied` has presence, so a plan says `false`, never leaves it out.
    assert_eq!(doc["applied"], false, "a plan prints applied false");
    assert_eq!(doc.get("events_destroyed"), None);
    assert_eq!(doc["files"][0]["rel"], "native/history.jsonl");
    assert_eq!(doc["files"][0]["class"], "MACHINE");
    assert_eq!(doc["files"][0]["bytes"], "9");
    assert_eq!(doc["files"][1]["class"], "AUTHORED");
    assert_eq!(doc["authored"], json!(["notes.md"]));
    let notices = without_authored(ran.result.unwrap());
    assert_eq!(notices.len(), 1);
    assert_eq!(notices[0].code(), "sessions-application-removed-nothing");
    let command = format!("{:?}", notices[0].args());
    assert!(
        command.contains("ctxloom session remove amber-swift-owl --yes"),
        "{command}"
    );
    assert_eq!(
        *daemon.deletes.borrow(),
        [("amber-swift-owl".to_string(), false, false)]
    );
}

// The plan's summary line, its table, and one line for the session itself.
#[test]
fn a_removal_report_in_text_names_each_file_and_the_session() {
    let daemon = done_with(plan(false));
    let ran = destroy_go(&daemon, Format::Text, &["remove", "amber-swift-owl"]);
    let lines: Vec<&str> = ran.out.lines().collect();
    assert_eq!(
        lines[0],
        "session amber-swift-owl \u{2014} would destroy 2 file(s) (0 byte(s) freed)"
    );
    for header in ["PATH", "CLASS", "BYTES"] {
        assert!(lines[1].contains(header), "{header}: {}", lines[1]);
    }
    assert!(ran.out.contains("native/history.jsonl"));
    assert!(ran.out.contains("notes.md") && ran.out.contains("authored"));
    assert_eq!(
        *lines.last().unwrap(),
        "session amber-swift-owl: would be destroyed, its events and its runs' with it; its name stays taken"
    );
    assert!(ran.result.unwrap().would_change);
}

#[test]
fn an_applied_removal_says_destroyed_and_adds_no_notice() {
    let daemon = done_with(plan(true));
    let ran = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(ran
        .out
        .starts_with("session amber-swift-owl \u{2014} destroyed 2 file(s) (13 byte(s) freed)"));
    assert!(ran.out.ends_with(
        "session amber-swift-owl: destroyed, 7 event(s) of it and its runs; its name stays taken\n"
    ));
    let done = ran.result.unwrap();
    assert!(!done.would_change);
    assert!(without_authored(done).is_empty());
    assert!(daemon.deletes.borrow()[0].1);
}

/// A command's notices but the authored-files warning (owner ruling 104),
/// which the removal tests below hold on their own.
fn without_authored(done: crate::Done) -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    done.notices
        .into_iter()
        .filter(|n| n.code() != "sessions-application-destroys-authored")
        .collect()
}

/// The warning a removal's notices hold, if any: the authored files it names.
fn authored_warning(done: &crate::Done) -> Option<String> {
    done.notices
        .iter()
        .find(|n| n.code() == "sessions-application-destroys-authored")
        .map(|n| format!("{:?}", n.args()))
}

// Owner ruling 104: a removal that destroys files a person wrote warns first,
// naming each, as a notice (never a refusal): applied, reported, and with
// --even-if-live alike.
#[test]
fn a_removal_that_destroys_authored_files_warns_naming_them() {
    for words in [
        &["remove", "amber-swift-owl", "--yes"][..],
        &["remove", "amber-swift-owl"][..],
        &["remove", "amber-swift-owl", "--yes", "--even-if-live"][..],
    ] {
        let daemon = done_with(plan(words.contains(&"--yes")));
        let done = destroy_go(&daemon, Format::Text, words).result.unwrap();
        let warned = authored_warning(&done).unwrap_or_else(|| panic!("{words:?}: no warning"));
        assert!(warned.contains("notes.md"), "{words:?}: {warned}");
        assert!(
            !warned.contains("native/history.jsonl"),
            "{words:?}: {warned}"
        );
    }
}

// A removal with no authored file says nothing of them, and a refused one
// destroys nothing, so it warns of nothing.
#[test]
fn a_removal_of_machine_files_alone_or_a_refused_one_does_not_warn() {
    let mut machine = plan(true);
    machine.authored.clear();
    let done = destroy_go(
        &done_with(machine),
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap();
    assert_eq!(authored_warning(&done), None);
    let failed = destroy_go(
        &refused_with(Refusal::Kept {
            harp: "amber-swift-owl".to_string(),
        }),
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap_err();
    assert!(!format!("{:?}", failed.lines).contains("notes.md"));
}

const NAME: &str = "projects/calm-otter/sessions/amber-swift-owl";

/// `daemon`, its session holding `facts` and `set_aside` parts.
fn holding(mut daemon: Daemon, name: &str, facts: u64, set_aside: u32) -> Daemon {
    daemon.held.insert(
        name.to_string(),
        reclaim_application::HeldOf { facts, set_aside },
    );
    daemon
}

/// The held-facts warning a command's notices hold, if any: its args.
fn held_warning(done: &crate::Done) -> Option<String> {
    done.notices
        .iter()
        .find(|n| n.code() == "sessions-application-destroys-held")
        .map(|n| format!("{:?}", n.args()))
}

// s4-Q14, H-Q8 (a): a removal's plan warns of the session's held facts,
// naming the count, and keeps them.
#[test]
fn a_removal_report_warns_of_the_sessions_held_facts_and_keeps_them() {
    let daemon = holding(done_with(plan(false)), NAME, 3, 0);
    let done = destroy_go(&daemon, Format::Text, &["remove", "amber-swift-owl"])
        .result
        .unwrap();
    let warned = held_warning(&done).expect("the held facts' warning");
    assert!(warned.contains("amber-swift-owl"), "{warned}");
    assert!(warned.contains("facts") && warned.contains('3'), "{warned}");
    assert!(
        daemon.discarded.borrow().is_empty(),
        "a plan discards nothing"
    );
    assert!(done.would_change);
}

// H-Q8 (a): an applied removal warns, then discards the hold (its
// set-aside parts with it).
#[test]
fn an_applied_removal_warns_of_its_held_facts_and_discards_them() {
    let daemon = holding(done_with(plan(true)), NAME, 3, 1);
    let done = destroy_go(
        &daemon,
        Format::Json,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap();
    let warned = held_warning(&done).expect("the held facts' warning");
    assert!(
        warned.contains("set_aside") && warned.contains('1'),
        "{warned}"
    );
    assert_eq!(*daemon.discarded.borrow(), [NAME]);
}

// A session with no hold is warned of nothing, and no hold is touched.
#[test]
fn a_removal_of_a_session_that_holds_nothing_does_not_warn_of_held_facts() {
    let daemon = holding(
        done_with(plan(true)),
        "projects/calm-otter/sessions/brisk-copper-moth",
        9,
        0,
    );
    let done = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap();
    assert_eq!(held_warning(&done), None);
    assert!(daemon.discarded.borrow().is_empty());
}

// A refused removal destroys nothing: its hold is neither warned of nor
// discarded.
#[test]
fn a_refused_removal_leaves_its_hold_alone() {
    let daemon = holding(
        refused_with(Refusal::Kept {
            harp: "amber-swift-owl".to_string(),
        }),
        NAME,
        3,
        0,
    );
    let failure = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert!(daemon.discarded.borrow().is_empty());
}

// A hold that cannot be read or discarded fails the command (10), after the
// daemon's answer is printed: never a quiet leftover.
#[test]
fn a_removal_whose_hold_cannot_be_reached_fails() {
    let mut daemon = holding(done_with(plan(true)), NAME, 3, 0);
    daemon.holds_fail = true;
    let ran = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(ran.out.contains("destroyed"), "{}", ran.out);
    let failure = ran.result.unwrap_err();
    assert_eq!(failure.failure.exit, Exit::FAILED);
    assert_eq!(failure.failure.cause.code(), "run-ports-hold-io");
}

// --even-if-live reaches the daemon, which judges by the lock; the applying
// command repeats it.
#[test]
fn even_if_live_reaches_the_daemon_and_the_applying_command_repeats_it() {
    let daemon = done_with(plan(false));
    let ran = destroy_go(
        &daemon,
        Format::Json,
        &["remove", "amber-swift-owl", "--even-if-live"],
    );
    assert!(daemon.deletes.borrow()[0].2);
    let notices = without_authored(ran.result.unwrap());
    let command = format!("{:?}", notices[0].args());
    assert!(command.contains("--yes --even-if-live"), "{command}");
}

// Go: a refusal prints the plan (the refusal carries it, owner 2026-11-12),
// then exits refused (48), in the refusing use case's words.
#[test]
fn a_refusal_prints_the_plan_then_fails_refused() {
    let daemon = refused_with(Refusal::Retained {
        harp: "amber-swift-owl".to_string(),
        detail: "it ended an hour ago".to_string(),
    });
    let ran = destroy_go(
        &daemon,
        Format::Json,
        &["remove", "amber-swift-owl", "--yes"],
    );
    let doc: Value = serde_json::from_str(&ran.out).unwrap();
    assert_eq!(
        doc["files"][1]["rel"], "notes.md",
        "the plan is still printed"
    );
    let failure = ran.result.unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert_eq!(
        failure.failure.cause.code(),
        "sessions-application-refused-retained"
    );
    let text = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(text.out.contains("would destroy 2 file(s)"), "{}", text.out);
}

// A worktree that keeps the session is refused in isolation's words, after
// the plan and the worktrees are printed.
#[test]
fn a_session_its_worktrees_keep_prints_them_then_fails_refused() {
    let mut kept = plan(false);
    kept.worktrees = Some(v1::WorktreeReport {
        worktrees: vec![v1::ScratchWorktree {
            harp: "amber-swift-owl".to_string(),
            worktree: "ctxloom-wt-wip".to_string(),
            path: "/h/wt".to_string(),
            owner_state: "dead".to_string(),
            verdict: "spared".to_string(),
            reason: "uncommitted changes".to_string(),
        }],
        spared: 1,
        ..v1::WorktreeReport::default()
    });
    let daemon = Daemon {
        removed: Some(daemon_api::Removed::Refused {
            plan: kept,
            why: daemon_api::RemovalRejection::KeepsSession {
                harp: "amber-swift-owl".to_string(),
                spared: 1,
                skipped: 0,
            },
        }),
        ..Daemon::default()
    };
    let ran = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(ran.out.contains("ctxloom-wt-wip"), "{}", ran.out);
    assert!(ran.out.contains("spared"), "{}", ran.out);
    let failure = ran.result.unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert_eq!(
        failure.failure.cause.code(),
        "isolation-application-keeps-session"
    );
}

// A call that came to nothing is the command's failure, with its exit.
#[test]
fn a_removal_the_daemon_could_not_answer_fails() {
    let failure = destroy_go(
        &Daemon::default(),
        Format::Text,
        &["remove", "amber-swift-owl", "--yes"],
    )
    .result
    .unwrap_err();
    assert_eq!(failure.failure.exit, Exit::FAILED);
}

// ------------------------------------------------------------------ transcript

/// The sessions the daemon lists for `transcript list`: the first has a
/// transcript of nine bytes, the second none.
fn listed_with_transcripts() -> Reads {
    let mut owl = session("amber-swift-owl", v1::session::State::Ended);
    owl.transcript_captured = true;
    owl.transcript_bytes = 9;
    let moth = session("brisk-copper-moth", v1::session::State::Ended);
    Reads {
        sessions: vec![owl, moth],
        ..Reads::default()
    }
}

// Go: sessionTranscriptReport; served by the daemon's listing.
#[test]
fn the_transcript_listing_names_every_session_captured_or_not() {
    let reads = listed_with_transcripts();
    let doc = json_of(&read_go(
        &reads,
        Format::Json,
        &["transcript", "list", "--all"],
    ));
    assert_eq!(
        doc,
        json!({"transcripts": [
            {"harp": "amber-swift-owl", "captured": true, "bytes": 9},
            {"harp": "brisk-copper-moth", "captured": false, "bytes": 0},
        ]})
    );
    assert_eq!(*reads.asked.borrow(), ["transcripts true"]);
}

// session.feature: "A session with no captured transcript is still named": the
// text says "false".
#[test]
fn the_transcript_table_says_false_for_an_uncaptured_one() {
    let reads = listed_with_transcripts();
    let ran = read_go(&reads, Format::Text, &["transcript", "list"]);
    let lines: Vec<&str> = ran.out.lines().collect();
    for header in ["HARP", "CAPTURED", "BYTES"] {
        assert!(lines[0].contains(header), "{header}: {}", lines[0]);
    }
    let moth = lines
        .iter()
        .find(|l| l.contains("brisk-copper-moth"))
        .unwrap();
    assert!(moth.contains("false"), "{moth}");
}

#[test]
fn the_bare_transcript_lists_and_names_the_harp_it_is_given() {
    let reads = listed_with_transcripts();
    let all = json_of(&read_go(&reads, Format::Json, &["transcript"]));
    assert_eq!(all["transcripts"].as_array().unwrap().len(), 2);
    let one = json_of(&read_go(
        &reads,
        Format::Json,
        &["transcript", "list", "amber-swift-owl"],
    ));
    assert_eq!(one["transcripts"].as_array().unwrap().len(), 1);
    let unknown = read_go(
        &reads,
        Format::Json,
        &["transcript", "list", "no-such-harp"],
    );
    let failure = unknown.result.unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert_eq!(failure.failure.cause.code(), "daemon-api-session-not-found");
}

#[test]
fn an_empty_transcript_listing_is_the_placeholder() {
    let reads = Reads::default();
    assert_eq!(
        read_go(&reads, Format::Text, &["transcript", "list"]).out,
        "(no sessions)\n"
    );
}

fn prompt(prompt_id: &str, text: &str) -> v1::LogEntry {
    v1::LogEntry {
        entry: Some(v1::log_entry::Entry::Prompt(v1::LogPrompt {
            prompt_id: prompt_id.to_string(),
            text: text.to_string(),
        })),
        ..v1::LogEntry::default()
    }
}

fn words(message: &str, index: u32, text: &str) -> v1::LogEntry {
    v1::LogEntry {
        entry: Some(v1::log_entry::Entry::Words(v1::LogWords {
            prompt_id: "p1".to_string(),
            message_id: message.to_string(),
            index,
            text: text.to_string(),
            ..v1::LogWords::default()
        })),
        ..v1::LogEntry::default()
    }
}

// session.feature: "Show prints the whole transcript and exits, ready for a
// pipe": every line of stdout is one log entry's JSON object, and nothing
// else is written; the entry names no session, harp or engine.
#[test]
fn transcript_show_prints_each_entry_as_its_proto3_json() {
    let fake = Fake {
        transcript: vec![
            prompt("p1", "Seeded prompt for amber-swift-owl."),
            words("m", 0, "ok"),
        ],
        ..Fake::default()
    };
    let ran = shown(
        &fake,
        Format::Json,
        &["transcript", "show", "amber-swift-owl"],
    );
    assert!(ran.result.is_ok());
    let lines: Vec<Value> = ran
        .out
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    assert_eq!(lines.len(), 2, "{}", ran.out);
    assert_eq!(
        lines[0],
        json!({"prompt": {"prompt_id": "p1", "text": "Seeded prompt for amber-swift-owl."}})
    );
    assert_eq!(
        lines[1],
        json!({"words": {"prompt_id": "p1", "message_id": "m", "text": "ok"}})
    );
}

#[test]
fn transcript_show_in_text_reads_as_a_conversation() {
    let fake = Fake {
        transcript: vec![prompt("p1", "What now?"), words("m", 0, "This.")],
        ..Fake::default()
    };
    let ran = shown(&fake, Format::Text, &["transcript", "show", "h"]);
    assert_eq!(ran.out, "user: What now?\nassistant: This.\n");
}

// The assistant's words are one entry per piece in the transcript; the
// reader joins a message's pieces by index, wherever the pieces came in the
// stream.
#[test]
fn transcript_show_in_text_joins_a_messages_deltas() {
    let tool = v1::LogEntry {
        entry: Some(v1::log_entry::Entry::Tool(v1::LogTool {
            name: "Bash".to_string(),
            ..v1::LogTool::default()
        })),
        ..v1::LogEntry::default()
    };
    let fake = Fake {
        transcript: vec![
            prompt("p1", "Hi"),
            words("m1", 1, "world"),
            tool,
            words("m1", 0, "Hello "),
            words("m1", 1, "world"),
        ],
        ..Fake::default()
    };
    let ran = shown(&fake, Format::Text, &["transcript", "show", "h"]);
    assert_eq!(ran.out, "user: Hi\nassistant: Hello world\n");
}

// ----------------------------------------------------------------------- shape

// session.feature: "The old rename verb is gone, not hidden" and "Watching
// moved under the transcript it watches": neither is a command here.
#[test]
fn the_retired_verbs_are_not_commands() {
    let command = Session::augment_args(Command::new("session")).no_binary_name(true);
    for retired in ["rename", "watch", "distill", "forget"] {
        assert!(
            command
                .clone()
                .try_get_matches_from([retired, "x"])
                .is_err(),
            "{retired}"
        );
    }
}

// ---------------------------------------------------------------------- import

fn report() -> v1::ImportSessionsResponse {
    v1::ImportSessionsResponse {
        imported: vec![v1::import_sessions_response::Imported {
            harp: "amber-swift-owl".to_string(),
            project: "jolly-mute-magma".to_string(),
            id: "5f0c7a52-1111-5111-8111-000000000001".to_string(),
        }],
        already: vec!["brisk-copper-moth".to_string()],
        left: vec![v1::import_sessions_response::Left {
            harp: "calm-dusty-wren".to_string(),
            code: "sessions-application-left-no-project".to_string(),
            args: [("project_dir".to_string(), "/work/elsewhere".to_string())].into(),
        }],
    }
}

#[test]
fn an_import_reports_what_it_imported_what_it_held_and_what_it_left() {
    let writes = Writes {
        report: report(),
        ..Writes::default()
    };
    let doc = json_of(&child_go(&writes, Format::Json, &["import"]));
    assert_eq!(
        doc,
        json!({
            "imported": [{"harp": "amber-swift-owl", "project": "jolly-mute-magma", "id": "5f0c7a52-1111-5111-8111-000000000001"}],
            "already": ["brisk-copper-moth"],
            "left": [{"harp": "calm-dusty-wren", "code": "sessions-application-left-no-project", "args": {"project_dir": "/work/elsewhere"}}],
        })
    );
    assert_eq!(*writes.asked.lock().unwrap(), ["import"]);
}

#[test]
fn an_import_in_text_counts_and_names_each_left_session() {
    let writes = Writes {
        report: report(),
        ..Writes::default()
    };
    let ran = child_go(&writes, Format::Text, &["import"]);
    let lines: Vec<&str> = ran.out.lines().collect();
    assert_eq!(lines[0], "imported 1, already held 1, left 1");
    assert!(
        lines
            .iter()
            .any(|l| l.contains("calm-dusty-wren") && l.contains("left-no-project")),
        "{lines:?}"
    );
}

#[test]
fn an_import_of_nothing_says_so() {
    let writes = Writes::default();
    assert_eq!(
        child_go(&writes, Format::Text, &["import"]).out,
        "imported 0, already held 0, left 0\n"
    );
}

/// A writer that refuses everything.
struct Broken;

impl std::io::Write for Broken {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        Err(std::io::Error::other("broken pipe"))
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

// An answer that cannot be written is the command's failure, not a success.
#[test]
fn an_answer_that_cannot_be_written_fails_the_command() {
    let reads = Reads::default();
    let session = parsed(&["list"]);
    let failure = crate::read(&session, &reads, Format::Json, &mut Broken).unwrap_err();
    assert_eq!(failure.failure.cause.code(), "session-cli-output");
    assert_eq!(failure.failure.exit, Exit::FAILED);
}

// ---------------------------------------------------------------- worktrees
// Ported from Go's cli/session_worktrees.go (release/0.7) and the scenarios
// of cli/session.feature and j001300_closeout.feature.

fn checkout(
    harp: &str,
    name: &str,
    verdict: isolation_application::Verdict,
    reason: &str,
) -> isolation_application::Row {
    isolation_application::Row {
        harp: harp.to_string(),
        worktree: format!("ctxloom-wt-{name}"),
        path: format!("/home/.ctxloom/sessions/{harp}/work/ctxloom-wt-{name}"),
        owner_state: "dead",
        verdict,
        reason: reason.to_string(),
    }
}

fn wt_report(
    rows: Vec<isolation_application::Row>,
    applied: bool,
) -> isolation_application::Report {
    use isolation_application::Verdict;
    let count = |v: Verdict| rows.iter().filter(|r| r.verdict == v).count() as u64;
    isolation_application::Report {
        reaped: count(Verdict::Reaped),
        spared: count(Verdict::Spared),
        skipped: count(Verdict::Skipped),
        worktrees: rows,
        applied,
    }
}

// "The scratch worktrees are a population with their own listing": the bare
// form lists, and so does `list`, with or without a session.
#[test]
fn worktrees_lists_bare_and_by_verb_and_by_session() {
    let mut fake = Fake {
        wt_report: Some(wt_report(Vec::new(), false)),
        ..Fake::default()
    };
    for words in [
        vec!["worktrees"],
        vec!["worktrees", "list"],
        vec!["worktrees", "list", "amber-swift-owl"],
    ] {
        assert!(go(&mut fake, Format::Text, &words).result.is_ok());
    }
    assert_eq!(
        fake.wt_asked,
        vec![
            (None, false),
            (None, false),
            (Some("amber-swift-owl".to_string()), false)
        ]
    );
}

#[test]
fn worktrees_with_nothing_to_list_says_so() {
    let mut fake = Fake {
        wt_report: Some(wt_report(Vec::new(), false)),
        ..Fake::default()
    };
    assert_eq!(
        go(&mut fake, Format::Text, &["worktrees"]).out,
        "no ctxloom-owned scratch worktrees\n"
    );
}

// j001300 "The scratch worktrees are listed before anything is removed".
#[test]
fn the_text_listing_names_harp_owner_and_verdict_and_says_it_removed_nothing() {
    use isolation_application::Verdict;
    let mut fake = Fake {
        wt_report: Some(wt_report(
            vec![
                checkout("amber-quiet-heron", "clean", Verdict::Reapable, ""),
                checkout(
                    "amber-quiet-heron",
                    "wip",
                    Verdict::Spared,
                    "session amber-quiet-heron has ended, but the worktree has uncommitted changes (or unknown state)",
                ),
            ],
            false,
        )),
        ..Fake::default()
    };
    let ran = go(&mut fake, Format::Text, &["worktrees"]);
    for expected in [
        "HARP",
        "WORKTREE",
        "OWNER STATE",
        "VERDICT",
        "amber-quiet-heron",
        "ctxloom-wt-clean",
        "dead",
        "reapable",
        "spared",
        "uncommitted changes",
        "2 worktree(s) reviewed; 1 spared, 0 skipped (read-only - nothing was removed)",
    ] {
        assert!(ran.out.contains(expected), "{expected}: {}", ran.out);
    }
}

#[test]
fn the_json_listing_is_the_report() {
    use isolation_application::Verdict;
    let mut fake = Fake {
        wt_report: Some(wt_report(
            vec![checkout(
                "amber-quiet-heron",
                "clean",
                Verdict::Reapable,
                "",
            )],
            false,
        )),
        ..Fake::default()
    };
    let doc = json_of(&go(&mut fake, Format::Json, &["worktrees", "list"]));
    assert_eq!(doc["worktrees"][0]["harp"], "amber-quiet-heron");
    assert_eq!(doc["worktrees"][0]["worktree"], "ctxloom-wt-clean");
    assert_eq!(doc["worktrees"][0]["verdict"], "reapable");
    assert_eq!(doc["applied"], false);
}

// "Purging worktrees reports before it removes": succeeds, says it removed
// nothing and names the command that would.
#[test]
fn purge_without_yes_reports_and_names_the_command_that_removes() {
    use isolation_application::Verdict;
    let mut fake = Fake {
        wt_report: Some(wt_report(
            vec![checkout("amber-swift-owl", "clean", Verdict::Reapable, "")],
            false,
        )),
        ..Fake::default()
    };
    let ran = go(
        &mut fake,
        Format::Text,
        &["worktrees", "purge", "amber-swift-owl"],
    );
    let notices = ran.result.unwrap().notices;
    assert_eq!(notices.len(), 1);
    assert_eq!(notices[0].code(), "sessions-application-removed-nothing");
    let command = notices[0]
        .args()
        .into_iter()
        .find(|(name, _)| *name == "command")
        .map(|(_, value)| format!("{value:?}"))
        .unwrap();
    assert!(
        command.contains("ctxloom session worktrees purge amber-swift-owl --yes"),
        "{command}"
    );
    assert_eq!(
        fake.wt_asked,
        vec![(Some("amber-swift-owl".to_string()), false)]
    );
}

// j001300 "Purging removes only what it can prove is safe": --yes applies
// and the tally says what happened.
#[test]
fn purge_with_yes_applies_and_tallies() {
    use isolation_application::Verdict;
    let mut fake = Fake {
        wt_report: Some(wt_report(
            vec![
                checkout("amber-quiet-heron", "clean", Verdict::Reaped, ""),
                checkout(
                    "amber-quiet-heron",
                    "wip",
                    Verdict::Spared,
                    "has uncommitted changes",
                ),
            ],
            true,
        )),
        ..Fake::default()
    };
    let ran = go(
        &mut fake,
        Format::Text,
        &["worktrees", "purge", "amber-quiet-heron", "--yes"],
    );
    assert!(
        ran.out.contains("removed 1, spared 1, skipped 0"),
        "{}",
        ran.out
    );
    let done = ran.result.unwrap();
    assert!(done.notices.is_empty());
    assert_eq!(
        fake.wt_asked,
        vec![(Some("amber-quiet-heron".to_string()), true)]
    );
}

// j001300 "Purging a session that is still running removes nothing": an
// action that changed nothing refuses (exit 48), after the report is out.
#[test]
fn purge_with_yes_that_removed_nothing_refuses_after_reporting() {
    use isolation_application::Verdict;
    let mut fake = Fake {
        wt_report: Some(wt_report(
            vec![checkout(
                "teal-running-wren",
                "clean",
                Verdict::Skipped,
                "its session is running",
            )],
            true,
        )),
        ..Fake::default()
    };
    let ran = go(
        &mut fake,
        Format::Text,
        &["worktrees", "purge", "teal-running-wren", "--yes"],
    );
    assert!(ran.out.contains("skipped"), "{}", ran.out);
    let failure = ran.result.unwrap_err();
    assert_eq!(
        failure.failure.cause.code(),
        "isolation-application-nothing-removed"
    );
    assert_eq!(failure.failure.exit, Exit::REFUSED);
}

// "The retired reap switch is gone, not hidden".
#[test]
fn the_retired_reap_switch_is_refused() {
    let command = Session::augment_args(Command::new("session")).no_binary_name(true);
    let refused = command.try_get_matches_from(["worktrees", "--reap", "--yes"]);
    assert!(refused.is_err());
}

// A session the use case cannot find is its error, with its exit code.
#[test]
fn a_session_with_no_directory_fails_the_listing() {
    let mut fake = Fake::default();
    let failure = go(&mut fake, Format::Text, &["worktrees", "list", "nobody"])
        .result
        .unwrap_err();
    assert_eq!(
        failure.failure.cause.code(),
        "isolation-application-no-session"
    );
    assert_eq!(failure.failure.exit, Exit::REFUSED);
}

// ----------------------------------------------------------------------- sweep

fn notice_command(done: &crate::Done) -> String {
    let notices: Vec<_> = done
        .notices
        .iter()
        .filter(|n| n.code() != "sessions-application-destroys-authored")
        .collect();
    assert_eq!(notices.len(), 1);
    assert_eq!(notices[0].code(), "sessions-application-removed-nothing");
    notices[0]
        .args()
        .into_iter()
        .find(|(n, _)| *n == "command")
        .map(|(_, a)| format!("{a:?}"))
        .unwrap()
}

fn row(harp: &str, action: &str, verdict: &str, reason: &str, command: &str) -> v1::SweepRow {
    v1::SweepRow {
        harp: harp.to_string(),
        action: action.to_string(),
        verdict: verdict.to_string(),
        reason: reason.to_string(),
        command: command.to_string(),
        ..v1::SweepRow::default()
    }
}

fn swept(rows: Vec<v1::SweepRow>, counts: v1::SweepCounts) -> Daemon {
    Daemon {
        swept: Some(v1::SweepSessionsResponse {
            rows,
            purge_cutoff: "2026-07-16".to_string(),
            counts: Some(counts),
            ..v1::SweepSessionsResponse::default()
        }),
        ..Daemon::default()
    }
}

// The daemon's report, a line per row with its reason and command, and the
// tally; without --yes it names the command that applies it.
#[test]
fn a_sweep_reports_each_sessions_fate_with_its_reason_and_command_and_removes_nothing() {
    let daemon = swept(
        vec![
            row("live-one", "skip", "skipped", "it is running", ""),
            row(
                "plan-one",
                "purge",
                "planned",
                "",
                "ctxloom session remove plan-one --yes",
            ),
        ],
        v1::SweepCounts {
            planned: 1,
            skipped: 1,
            ..v1::SweepCounts::default()
        },
    );
    let ran = destroy_go(
        &daemon,
        Format::Text,
        &["sweep", "--purge-older-than", "1d"],
    );
    assert!(
        ran.out
            .starts_with("Session sweep: purge before 2026-07-16."),
        "{}",
        ran.out
    );
    assert!(ran.out.contains("it is running"), "{}", ran.out);
    assert!(
        ran.out
            .contains("run: ctxloom session remove plan-one --yes"),
        "{}",
        ran.out
    );
    assert!(ran
        .out
        .contains("0 done, 1 planned, 0 held, 0 left, 1 skipped, 0 failed."));
    let done = ran.result.unwrap();
    assert!(done.would_change);
    let command = notice_command(&done);
    assert!(
        command.contains("ctxloom session sweep --purge-older-than 1d --yes"),
        "{command}"
    );
    assert_eq!(
        *daemon.sweeps.borrow(),
        [(false, Some("1d".to_string()), false)]
    );
}

#[test]
fn a_sweep_without_a_purge_age_says_purges_are_held() {
    let mut daemon = swept(Vec::new(), v1::SweepCounts::default());
    if let Some(swept) = daemon.swept.as_mut() {
        swept.purge_cutoff.clear();
    }
    let ran = destroy_go(&daemon, Format::Text, &["sweep", "--all-projects"]);
    assert!(ran.out.contains("not set (purges are held)"), "{}", ran.out);
    let done = ran.result.unwrap();
    assert!(!done.would_change);
    let command = notice_command(&done);
    assert!(
        command.contains("ctxloom session sweep --all-projects --yes"),
        "{command}"
    );
    assert_eq!(*daemon.sweeps.borrow(), [(true, None, false)]);
}

// Ruling 146: the report in json is the daemon's answer as its proto3 JSON.
#[test]
fn a_sweep_report_in_json_is_the_report() {
    let daemon = swept(
        vec![row("plan-one", "purge", "planned", "", "")],
        v1::SweepCounts {
            planned: 1,
            ..v1::SweepCounts::default()
        },
    );
    let doc = json_of(&destroy_go(
        &daemon,
        Format::Json,
        &["sweep", "--purge-older-than", "1d"],
    ));
    assert_eq!(doc["rows"][0]["harp"], "plan-one");
    assert_eq!(doc["rows"][0]["verdict"], "planned");
    assert_eq!(doc["counts"]["planned"], "1");
    assert_eq!(doc["purge_cutoff"], "2026-07-16");
}

// Owner ruling 104: each session the sweep destroys (or plans to) that holds
// a person's files gets the same warning `session remove` gives, naming them.
#[test]
fn a_sweep_warns_of_the_authored_files_each_destroy_takes() {
    let mut planned = row("plan-one", "purge", "planned", "", "");
    planned.authored = vec!["notes.md".to_string()];
    let daemon = swept(vec![planned], v1::SweepCounts::default());
    let done = destroy_go(&daemon, Format::Text, &["sweep"])
        .result
        .unwrap();
    let warned = authored_warning(&done).expect("the warning");
    assert!(warned.contains("notes.md"), "{warned}");
}

/// A sweep's removal of `harp` (of calm-otter), `applied` or planned.
fn swept_removal(harp: &str, applied: bool) -> v1::DeleteSessionResponse {
    v1::DeleteSessionResponse {
        name: format!("projects/calm-otter/sessions/{harp}"),
        harp: harp.to_string(),
        applied: Some(applied),
        ..v1::DeleteSessionResponse::default()
    }
}

// s4-Q14, H-Q8 (a): each session a sweep plans to destroy warns of its held
// facts; nothing is discarded by a report.
#[test]
fn a_sweep_report_warns_of_each_planned_destroys_held_facts() {
    let mut daemon = swept(
        vec![
            row("plan-one", "purge", "planned", "", ""),
            row("plan-two", "purge", "planned", "", ""),
        ],
        v1::SweepCounts {
            planned: 2,
            ..v1::SweepCounts::default()
        },
    );
    if let Some(swept) = daemon.swept.as_mut() {
        swept.sessions = vec![
            swept_removal("plan-one", false),
            swept_removal("plan-two", false),
        ];
    }
    let daemon = holding(daemon, "projects/calm-otter/sessions/plan-two", 4, 0);
    let done = destroy_go(&daemon, Format::Text, &["sweep"])
        .result
        .unwrap();
    let warnings: Vec<String> = done
        .notices
        .iter()
        .filter(|n| n.code() == "sessions-application-destroys-held")
        .map(|n| format!("{:?}", n.args()))
        .collect();
    assert_eq!(warnings.len(), 1, "{warnings:?}");
    assert!(warnings[0].contains("plan-two"), "{warnings:?}");
    assert!(daemon.discarded.borrow().is_empty());
}

// Each session a sweep destroyed has its hold discarded, even when another
// row failed (the failure is still the command's exit).
#[test]
fn a_sweep_discards_the_hold_of_each_session_it_destroyed() {
    let mut daemon = swept(
        vec![
            row("gone-one", "purge", "done", "", ""),
            row("bad-one", "purge", "failed", "boom", ""),
        ],
        v1::SweepCounts {
            done: 1,
            failed: 1,
            ..v1::SweepCounts::default()
        },
    );
    if let Some(swept) = daemon.swept.as_mut() {
        swept.sessions = vec![swept_removal("gone-one", true)];
    }
    let daemon = holding(daemon, "projects/calm-otter/sessions/gone-one", 2, 0);
    let failure = destroy_go(&daemon, Format::Text, &["sweep", "--yes"])
        .result
        .unwrap_err();
    assert_eq!(failure.failure.cause.code(), "session-cli-sweep-failed");
    assert_eq!(
        *daemon.discarded.borrow(),
        ["projects/calm-otter/sessions/gone-one"]
    );
}

#[test]
fn a_sweep_with_yes_acts_and_leaves_no_notice() {
    let daemon = swept(
        vec![row("plan-one", "purge", "done", "", "")],
        v1::SweepCounts {
            done: 1,
            ..v1::SweepCounts::default()
        },
    );
    let ran = destroy_go(&daemon, Format::Text, &["sweep", "--yes"]);
    assert!(ran.out.contains("1 done"), "{}", ran.out);
    assert!(without_authored(ran.result.unwrap()).is_empty());
    assert_eq!(*daemon.sweeps.borrow(), [(false, None, true)]);
}

#[test]
fn a_sweep_that_failed_while_acting_exits_nonzero_but_a_report_of_a_failure_does_not() {
    let daemon = swept(
        vec![row("plan-one", "purge", "failed", "boom", "")],
        v1::SweepCounts {
            failed: 1,
            ..v1::SweepCounts::default()
        },
    );
    let ran = destroy_go(&daemon, Format::Text, &["sweep"]);
    assert!(ran.out.contains("1 failed"), "{}", ran.out);
    assert!(ran.result.is_ok(), "a report only reports");
    let failure = destroy_go(&daemon, Format::Text, &["sweep", "--yes"])
        .result
        .unwrap_err();
    assert_eq!(failure.failure.cause.code(), "session-cli-sweep-failed");
    assert_eq!(failure.failure.exit, Exit::FAILED);
}

// A --purge-older-than that is no age is refused here (exit 48), naming the
// flag, before the daemon is asked.
#[test]
fn a_purge_age_that_is_none_is_refused_before_the_daemon_is_asked() {
    let daemon = swept(Vec::new(), v1::SweepCounts::default());
    let failure = destroy_go(
        &daemon,
        Format::Text,
        &["sweep", "--purge-older-than", "soon"],
    )
    .result
    .unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert_eq!(failure.failure.cause.code(), "reclaim-application-bad-age");
    assert!(daemon.sweeps.borrow().is_empty());
}

// The age form of the one destroyer (docs/plans/ctxloom-features.md,
// "Sessions": `session remove --older-than <age>`, owner ruling 98): this
// project's sweep, its purge age the flag's, reported with the command that
// applies it in the same verb; no single session is asked for.
#[test]
fn remove_older_than_sweeps_this_project_by_that_age_and_names_the_apply() {
    let daemon = swept(
        vec![row(
            "plan-one",
            "purge",
            "planned",
            "",
            "ctxloom session remove plan-one --yes",
        )],
        v1::SweepCounts {
            planned: 1,
            ..v1::SweepCounts::default()
        },
    );
    let ran = destroy_go(&daemon, Format::Text, &["remove", "--older-than", "90d"]);
    assert!(
        ran.out
            .starts_with("Session sweep: purge before 2026-07-16."),
        "{}",
        ran.out
    );
    let done = ran.result.unwrap();
    assert!(done.would_change);
    let command = notice_command(&done);
    assert!(
        command.contains("ctxloom session remove --older-than 90d --yes"),
        "{command}"
    );
    assert_eq!(
        *daemon.sweeps.borrow(),
        [(false, Some("90d".to_string()), false)]
    );
    assert!(daemon.deletes.borrow().is_empty());
}

#[test]
fn remove_older_than_with_yes_applies_the_sweep() {
    let daemon = swept(
        vec![row("plan-one", "purge", "done", "", "")],
        v1::SweepCounts {
            done: 1,
            ..v1::SweepCounts::default()
        },
    );
    let ran = destroy_go(
        &daemon,
        Format::Text,
        &["remove", "--older-than", "90d", "--yes"],
    );
    let done = ran.result.unwrap();
    assert!(!done.would_change);
    assert!(without_authored(done).is_empty());
    assert_eq!(
        *daemon.sweeps.borrow(),
        [(false, Some("90d".to_string()), true)]
    );
}

// An age that is none is refused naming the flag the user typed, before the
// daemon is asked.
#[test]
fn remove_older_than_an_age_that_is_none_is_refused_naming_the_flag() {
    let daemon = swept(Vec::new(), v1::SweepCounts::default());
    let failure = destroy_go(&daemon, Format::Text, &["remove", "--older-than", "soon"])
        .result
        .unwrap_err();
    assert_eq!(failure.failure.exit, Exit::REFUSED);
    assert_eq!(failure.failure.cause.code(), "reclaim-application-bad-age");
    let args = format!("{:?}", failure.failure.cause.args());
    assert!(args.contains("\"--older-than\""), "{args}");
    assert!(daemon.sweeps.borrow().is_empty());
}

// One session or an age: exactly one of them, and `--even-if-live` is the
// single session's override alone.
#[test]
fn remove_takes_a_session_or_an_age_never_both_nor_neither() {
    let refused = |words: &[&str]| {
        Session::augment_args(Command::new("session"))
            .no_binary_name(true)
            .try_get_matches_from(words.iter().map(|w| w.to_string()))
            .is_err()
    };
    assert!(refused(&["remove"]));
    assert!(refused(&[
        "remove",
        "amber-swift-owl",
        "--older-than",
        "90d"
    ]));
    assert!(refused(&[
        "remove",
        "--older-than",
        "90d",
        "--even-if-live"
    ]));
    assert!(!refused(&["remove", "amber-swift-owl"]));
    assert!(!refused(&["remove", "--older-than", "90d"]));
}

// Two messages whose pieces interleave in the stream are two `assistant:`
// lines, each of its own pieces only (a piece of another message, or an entry
// of another kind, is not joined in).
#[test]
fn transcript_show_in_text_keeps_two_messages_apart() {
    let fake = Fake {
        transcript: vec![
            words("m1", 0, "One "),
            words("m2", 0, "Two "),
            words("m1", 1, "again"),
            words("m2", 1, "too"),
        ],
        ..Fake::default()
    };
    let ran = shown(&fake, Format::Text, &["transcript", "show", "h"]);
    assert_eq!(ran.out, "assistant: One again\nassistant: Two too\n");
}

// Live probe (2026-11-09): the engine's Stop hook can fire before (or within
// milliseconds of) the MessageDisplay hook of the same turn, so a turn's words
// may reach the transcript after its turn_end and even after the NEXT prompt.
// The reader places an assistant message by its prompt_id, not by position.
#[test]
fn transcript_show_in_text_places_a_message_under_its_own_prompt_whatever_arrived_first() {
    let tool = |prompt: &str| v1::LogEntry {
        entry: Some(v1::log_entry::Entry::Tool(v1::LogTool {
            prompt_id: prompt.to_string(),
            name: "Bash".to_string(),
            ..v1::LogTool::default()
        })),
        ..v1::LogEntry::default()
    };
    let end = |prompt: &str| v1::LogEntry {
        entry: Some(v1::log_entry::Entry::TurnEnd(v1::LogTurnEnd {
            prompt_id: prompt.to_string(),
        })),
        ..v1::LogEntry::default()
    };
    let said = |prompt: &str, text: &str| v1::LogEntry {
        entry: Some(v1::log_entry::Entry::Words(v1::LogWords {
            prompt_id: prompt.to_string(),
            turn_id: prompt.to_string(),
            message_id: format!("m-{prompt}"),
            index: 0,
            is_final: true,
            text: text.to_string(),
        })),
        ..v1::LogEntry::default()
    };
    // The probe's own order (Stop first), then the worst case: turn 1's words
    // after turn 2's prompt.
    let orders = [
        vec![
            prompt("p1", "q1"),
            tool("p1"),
            end("p1"),
            said("p1", "a1"),
            prompt("p2", "q2"),
            end("p2"),
            said("p2", "a2"),
        ],
        vec![
            prompt("p1", "q1"),
            tool("p1"),
            end("p1"),
            prompt("p2", "q2"),
            said("p1", "a1"),
            end("p2"),
            said("p2", "a2"),
        ],
    ];
    for entries in orders {
        let fake = Fake {
            transcript: entries.clone(),
            ..Fake::default()
        };
        let ran = shown(&fake, Format::Text, &["transcript", "show", "h"]);
        assert_eq!(
            ran.out, "user: q1\nassistant: a1\nuser: q2\nassistant: a2\n",
            "{entries:?}"
        );
    }
}

// Ruling 87a, 94b: the transcript has no destroyer of its own (no file holds
// it; the session's events are destroyed whole, by `session remove`).
#[test]
fn there_is_no_transcript_purge() {
    let command = Session::augment_args(Command::new("session")).no_binary_name(true);
    let refused = command.try_get_matches_from(["transcript", "purge", "amber-swift-owl"]);
    assert!(refused.is_err());
}

// Ruling 153: a removal's or a sweep's preview that `--yes` would act on would
// change something; the command applied, or a preview with nothing planned,
// does not.
#[test]
fn a_preview_that_would_destroy_says_it_would_change_and_an_applied_one_does_not() {
    let previewed = destroy_go(
        &done_with(plan(false)),
        Format::Json,
        &["remove", "amber-swift-owl"],
    );
    assert!(previewed.result.unwrap().would_change);
    let applied = destroy_go(
        &done_with(plan(true)),
        Format::Json,
        &["remove", "amber-swift-owl", "--yes"],
    );
    assert!(!applied.result.unwrap().would_change);

    let sweep = |planned: u64, words: &[&str]| {
        let daemon = swept(
            Vec::new(),
            v1::SweepCounts {
                planned,
                ..v1::SweepCounts::default()
            },
        );
        destroy_go(&daemon, Format::Json, words)
            .result
            .unwrap()
            .would_change
    };
    let words = ["sweep", "--purge-older-than", "1d", "--all-projects"];
    assert!(sweep(1, &words), "one is planned");
    assert!(!sweep(0, &words), "nothing is planned");
    let yes = [
        "sweep",
        "--purge-older-than",
        "1d",
        "--all-projects",
        "--yes",
    ];
    assert!(!sweep(1, &yes), "applied");
}

// Only `remove` and `sweep` are the daemon's destroys; a read or a
// worktree listing is not.
#[test]
fn only_remove_and_sweep_are_destroys() {
    for words in [
        vec![],
        vec!["list"],
        vec!["show", "amber-swift-owl"],
        vec!["worktrees"],
    ] {
        assert!(!crate::is_destroy(&parsed(&words)), "{words:?}");
    }
    for words in [vec!["remove", "amber-swift-owl"], vec!["sweep"]] {
        assert!(crate::is_destroy(&parsed(&words)), "{words:?}");
    }
}

// ---- the child verbs (slice 4 U3, S2, S19)

/// The user's writes a test scripts: each answers with the target's
/// session, applied unless `duplicate`, and records what it was asked.
#[derive(Default)]
struct Writes {
    asked: std::sync::Mutex<Vec<String>>,
    duplicate: bool,
    /// What an import answers.
    report: v1::ImportSessionsResponse,
}

impl Writes {
    fn written(&self, harp: &str, id: &str) -> daemon_api::Written {
        daemon_api::Written {
            session: session(harp, v1::session::State::Live),
            id: id.to_string(),
            applied: !self.duplicate,
        }
    }
}

impl daemon_api::ChildWrites for Writes {
    async fn launch(
        &self,
        launch: &daemon_api::Launch,
    ) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked.lock().unwrap().push(format!("{launch:?}"));
        Ok(self.written(launch.harp.as_deref().unwrap_or("minted-name-here"), "c1"))
    }

    async fn send_as(
        &self,
        target: &str,
        urgency: sessions_application::Urgency,
        text: &str,
        id: Option<&str>,
    ) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("send {target} {urgency:?} {text} {id:?}"));
        Ok(self.written(target, id.unwrap_or("minted")))
    }

    async fn clear(&self, target: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked.lock().unwrap().push(format!("clear {target}"));
        Ok(self.written(target, "k1"))
    }

    async fn close(&self, target: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked.lock().unwrap().push(format!("close {target}"));
        Ok(self.written(target, "uid"))
    }

    async fn stop(&self, target: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked.lock().unwrap().push(format!("stop {target}"));
        Ok(self.written(target, "uid"))
    }

    async fn restart(
        &self,
        target: &str,
        note: &str,
    ) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("restart {target} {note:?}"));
        Ok(self.written(target, "uid"))
    }

    async fn import(&self) -> Result<v1::ImportSessionsResponse, daemon_api::CallError> {
        self.asked.lock().unwrap().push("import".to_string());
        Ok(self.report.clone())
    }

    async fn keep(
        &self,
        target: &str,
        kept: bool,
    ) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("keep {target} {kept}"));
        Ok(self.written(target, "keep-call"))
    }

    async fn title(
        &self,
        target: &str,
        title: &str,
    ) -> Result<daemon_api::Written, daemon_api::CallError> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("title {target} {title:?}"));
        Ok(self.written(target, "title-call"))
    }
}

fn child_go(writes: &Writes, format: Format, words: &[&str]) -> Ran {
    let command = parsed(words);
    assert!(crate::is_child(&command), "{words:?} is a child verb");
    let mut out = Vec::new();
    let result = crate::child(&command, writes, format, &mut out);
    Ran {
        out: String::from_utf8(out).unwrap(),
        result,
    }
}

#[test]
fn a_launch_names_its_owner_child_agent_engine_and_task() {
    let writes = Writes::default();
    let ran = child_go(
        &writes,
        Format::Text,
        &[
            "launch",
            "amber-quiet-heron",
            "--name",
            "brisk-copper-moth",
            "--agent",
            "reviewer",
            "--engine",
            "claude-code",
            "--project",
            "../api",
            "review the change",
        ],
    );
    assert!(ran.result.is_ok());
    assert_eq!(
        ran.out,
        "brisk-copper-moth: launched, a child of amber-quiet-heron\n"
    );
    let asked = writes.asked.lock().unwrap().clone();
    assert_eq!(
        asked,
        [format!(
            "{:?}",
            daemon_api::Launch {
                owner: Some("amber-quiet-heron".to_string()),
                harp: Some("brisk-copper-moth".to_string()),
                engine: "claude-code".to_string(),
                agent: "reviewer".to_string(),
                task: "review the change".to_string(),
                project: Some("../api".to_string()),
            }
        )]
    );
}

#[test]
fn a_launch_in_json_is_the_childs_session() {
    let writes = Writes::default();
    let ran = child_go(
        &writes,
        Format::Json,
        &["launch", "amber-quiet-heron", "--name", "brisk-copper-moth"],
    );
    assert!(ran.result.is_ok());
    assert_eq!(
        json_of(&ran)["name"],
        "projects/calm-otter/sessions/brisk-copper-moth"
    );
}

#[test]
fn a_send_carries_its_urgency_text_and_id_and_says_a_duplicate() {
    let writes = Writes::default();
    let ran = child_go(
        &writes,
        Format::Text,
        &[
            "send",
            "brisk-copper-moth",
            "steer",
            "look at main.rs",
            "--id",
            "m1",
        ],
    );
    assert!(ran.result.is_ok());
    assert_eq!(ran.out, "brisk-copper-moth: m1 recorded\n");
    assert_eq!(
        *writes.asked.lock().unwrap(),
        ["send brisk-copper-moth Steer look at main.rs Some(\"m1\")".to_string()]
    );
    let again = Writes {
        duplicate: true,
        ..Writes::default()
    };
    let ran = child_go(
        &again,
        Format::Text,
        &["send", "brisk-copper-moth", "wake", "x", "--id", "m1"],
    );
    assert_eq!(ran.out, "brisk-copper-moth: m1 already recorded\n");
}

#[test]
fn a_send_with_no_urgency_is_a_usage_error_naming_the_three() {
    let command = Session::augment_args(Command::new("session")).no_binary_name(true);
    let refused = command
        .try_get_matches_from(["send", "brisk-copper-moth", "check the tests"])
        .unwrap_err()
        .to_string();
    assert!(refused.contains("inform, wake, steer"), "{refused}");
}

#[test]
fn a_clear_and_a_close_say_what_they_did_and_a_second_close_says_so() {
    let writes = Writes::default();
    let ran = child_go(&writes, Format::Text, &["clear", "brisk-copper-moth"]);
    assert_eq!(ran.out, "brisk-copper-moth: k1 recorded\n");
    let ran = child_go(&writes, Format::Text, &["close", "brisk-copper-moth"]);
    assert_eq!(ran.out, "brisk-copper-moth: closed\n");
    let again = Writes {
        duplicate: true,
        ..Writes::default()
    };
    let ran = child_go(&again, Format::Text, &["close", "brisk-copper-moth"]);
    assert_eq!(ran.out, "brisk-copper-moth: already closed\n");
    assert!(ran.result.unwrap().notices.is_empty(), "said on its line");
    let ran = child_go(&again, Format::Json, &["close", "brisk-copper-moth"]);
    let notices = ran.result.unwrap().notices;
    assert_eq!(notices.len(), 1);
    assert_eq!(notices[0].code(), "session-cli-already-closed");
    let ran = child_go(&writes, Format::Json, &["close", "brisk-copper-moth"]);
    assert!(ran.result.unwrap().notices.is_empty(), "closed now");
    let ran = child_go(
        &again,
        Format::Json,
        &["send", "brisk-copper-moth", "wake", "x"],
    );
    assert!(ran.result.unwrap().notices.is_empty(), "no close");
}

#[test]
fn a_refused_child_verb_fails_as_its_refusal_says() {
    struct Refusing;
    impl daemon_api::ChildWrites for Refusing {
        async fn launch(
            &self,
            _: &daemon_api::Launch,
        ) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn send_as(
            &self,
            _: &str,
            _: sessions_application::Urgency,
            _: &str,
            _: Option<&str>,
        ) -> Result<daemon_api::Written, daemon_api::CallError> {
            Err(daemon_api::CallError::Refused(daemon_api::Rejection::Api(
                daemon_api::ApiRejection::SessionNotFound {
                    name: "brisk-copper-moth".to_string(),
                },
            )))
        }
        async fn clear(&self, _: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn close(&self, _: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn stop(&self, _: &str) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn restart(
            &self,
            _: &str,
            _: &str,
        ) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn import(&self) -> Result<v1::ImportSessionsResponse, daemon_api::CallError> {
            unreachable!()
        }
        async fn keep(
            &self,
            _: &str,
            _: bool,
        ) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
        async fn title(
            &self,
            _: &str,
            _: &str,
        ) -> Result<daemon_api::Written, daemon_api::CallError> {
            unreachable!()
        }
    }
    let command = parsed(&["send", "brisk-copper-moth", "wake", "one more thing"]);
    let mut out = Vec::new();
    let failed = crate::child(&command, &Refusing, Format::Text, &mut out).unwrap_err();
    assert_eq!(failed.failure.exit, Exit::REFUSED);
    assert_eq!(failed.failure.cause.code(), "daemon-api-session-not-found");
}

// S19's `session stop` (OD-57): the child's run is asked to stop, and the
// child stays open; a stop of a run already stopped or ended says so.
#[test]
fn a_stop_stops_the_childs_run_and_says_so() {
    let writes = Writes::default();
    let ran = child_go(&writes, Format::Text, &["stop", "quiet-silver-fern"]);
    assert_eq!(ran.out, "quiet-silver-fern: its run asked to stop\n");
    assert_eq!(*writes.asked.lock().unwrap(), ["stop quiet-silver-fern"]);
    let again = Writes {
        duplicate: true,
        ..Writes::default()
    };
    let ran = child_go(&again, Format::Text, &["stop", "quiet-silver-fern"]);
    assert_eq!(ran.out, "quiet-silver-fern: no run of it to stop\n");
    let ran = child_go(&writes, Format::Json, &["stop", "quiet-silver-fern"]);
    assert!(
        ran.out.contains("projects/"),
        "the session as json: {}",
        ran.out
    );
}

// Step 10e Stage 2a, `session restart`: the session is asked to restart its
// engine with resume (the daemon waits for its turn in flight); asked again
// while one is owed, it says so.
#[test]
fn a_restart_asks_the_session_and_says_so() {
    let writes = Writes::default();
    let ran = child_go(&writes, Format::Text, &["restart", "quiet-silver-fern"]);
    assert_eq!(
        ran.out,
        "quiet-silver-fern: a restart asked: its engine is restarted, resuming its conversation, once its turn ends\n"
    );
    assert_eq!(
        *writes.asked.lock().unwrap(),
        ["restart quiet-silver-fern \"\""]
    );
    let noted = child_go(
        &writes,
        Format::Text,
        &["restart", "quiet-silver-fern", "--note", "new context"],
    );
    assert!(noted.result.is_ok());
    assert_eq!(
        writes.asked.lock().unwrap()[1],
        "restart quiet-silver-fern \"new context\""
    );
    let again = Writes {
        duplicate: true,
        ..Writes::default()
    };
    let ran = child_go(&again, Format::Text, &["restart", "quiet-silver-fern"]);
    assert_eq!(ran.out, "quiet-silver-fern: a restart is asked already\n");
    let ran = child_go(&writes, Format::Json, &["restart", "quiet-silver-fern"]);
    assert!(
        ran.out.contains("projects/"),
        "the session as json: {}",
        ran.out
    );
}

#[test]
fn only_the_four_child_verbs_are_child_verbs() {
    assert!(!crate::is_child(&parsed(&["list"])));
    assert!(!crate::is_child(&parsed(&["show", "x"])));
    assert!(crate::is_child(&parsed(&["close", "x"])));
}

// The terminal wake (owner ruling 2026-11-18): show says a wake sent and not
// answered, and one still outstanding; nothing when there is neither.
#[test]
fn show_in_text_says_a_wake_sent_and_not_answered() {
    let shown = |wake: &str| {
        let mut s = session("amber-quiet-heron", v1::session::State::Live);
        s.wake = wake.to_string();
        let reads = Reads {
            sessions: vec![s],
            ..Reads::default()
        };
        read_go(&reads, Format::Text, &["show", "amber-quiet-heron"]).out
    };
    let out = shown("not answered");
    assert!(out.contains("wake:     sent, not answered"), "{out}");
    let out = shown("outstanding");
    assert!(out.contains("wake:     sent, awaiting its turn"), "{out}");
    let out = shown("");
    assert!(!out.contains("wake:"), "{out}");
}

// Owner ruling 2026-11-21: a launch refused for the plan's fatal findings
// lists each of them, its remedy as the fix, beside the summary.
#[test]
fn a_launch_refused_for_fatal_findings_lists_them() {
    let error = daemon_api::CallError::Refused(daemon_api::Rejection::Runs(
        daemon_api::RunRejection::LaunchRefused {
            refusals: Vec::new(),
            fatal_findings: 2,
            findings: vec![
                daemon_api::LaunchFinding {
                    text: "failed to load bundle \"hub/demo\"".to_string(),
                    remedy: "run `ctxloom deps pull`".to_string(),
                },
                daemon_api::LaunchFinding {
                    text: "second".to_string(),
                    remedy: String::new(),
                },
            ],
        },
    ));
    let failure = crate::run::call_failure(error);
    let said: Vec<String> = failure
        .failure
        .refusals
        .iter()
        .map(|r| r.code().to_string())
        .collect();
    assert_eq!(said, ["runs-fatal-finding", "runs-fatal-finding-said"]);
}
