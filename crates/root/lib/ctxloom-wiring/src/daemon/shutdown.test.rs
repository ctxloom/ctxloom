//! `ctxloom shutdown`'s pieces; the whole is driven end to end by the
//! binary's acceptance tests (`daemon.test.rs`, `tui_adopt.test.rs`).

use std::sync::Arc;
use std::time::Duration;

use file_system::Rooted;

use angzarr_embedded::v1::CommandBook;
use cli_support::Exit;
use run_ports::{Delivery, FactSink, Held, HeldFact, HeldFacts};
use runs_application::{observe, Undelivered};
use surface_ports::HookEvent;

use super::{
    configured_drain_timeout, epoch_seconds, holds_said, notice_text, span_text, undelivered_in,
    verdict, Running, ShutdownError,
};
use crate::capture::Deadline;
use crate::daemon::flush::{flush_with, Flushed};

fn home(fs: &Rooted, config: Option<&str>) -> std::path::PathBuf {
    let home = std::path::PathBuf::from(os::host().flavor().from_top("/home/u"));
    if let Some(config) = config {
        let dir = home.join(".ctxloom");
        file_system::Fs::create_dir_all(fs, &dir, os::Mode::from_bits(0o700)).unwrap();
        file_system::Fs::write_atomic(
            fs,
            &dir.join("config.yaml"),
            config.as_bytes(),
            file_system::Perms::Exact(os::Mode::from_bits(0o600)),
        )
        .unwrap();
    }
    home
}

// The home config's timeout, read as a command reads the home layer: the
// span it says, none when it is unsaid or the config does not read.
#[test]
fn the_configured_timeout_is_the_home_configs() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let dyn_fs: Arc<dyn file_system::Fs> = fs.clone();
    let at = home(&fs, None);
    assert_eq!(configured_drain_timeout(&dyn_fs, &at), None);
    let at = home(
        &fs,
        Some("schema_version: 9\ndaemon:\n  drain_timeout: 90s\n"),
    );
    assert_eq!(
        configured_drain_timeout(&dyn_fs, &at),
        Some(Duration::from_secs(90))
    );
    let at = home(
        &fs,
        Some("schema_version: 9\ndaemon:\n  drain_timeout: now\n"),
    );
    assert_eq!(configured_drain_timeout(&dyn_fs, &at), Some(Duration::ZERO));
    let at = home(
        &fs,
        Some("schema_version: 9\ndaemon:\n  drain_timeout: soon\n"),
    );
    assert_eq!(
        configured_drain_timeout(&dyn_fs, &at),
        None,
        "refused: none"
    );
}

// What still runs is said by its process ids.
#[test]
fn what_runs_is_said_by_its_pids() {
    let none = Running {
        daemon: None,
        frontends: Vec::new(),
    };
    assert!(none.nothing());
    assert_eq!(none.said(), "no daemon, no frontend");
    let some = Running {
        daemon: Some(42),
        frontends: vec![7, 9],
    };
    assert!(!some.nothing());
    assert_eq!(some.said(), "the daemon (pid 42), frontends pid 7, pid 9");
    let frontend = Running {
        daemon: None,
        frontends: vec![7],
    };
    assert!(!frontend.nothing());
}

// A time that is no span is a decline; what will not stop, a failure.
#[test]
fn the_errors_are_a_decline_and_a_failure() {
    use coded_diagnostic::Diagnostic;
    let bad = ShutdownError::BadWhen {
        value: "soon".to_string(),
        cause: "x".to_string(),
    };
    assert_eq!(bad.code(), "ctxloom-wiring-shutdown-bad-time");
    let left = ShutdownError::Incomplete {
        daemon: "pid 1".to_string(),
        frontends: "none".to_string(),
    };
    assert_eq!(left.code(), "ctxloom-wiring-shutdown-incomplete");
}

// A span is said as the one grammar writes it, longest unit first.
#[test]
fn a_span_is_said_longest_unit_first() {
    for (seconds, text) in [
        (0, "0s"),
        (1, "1s"),
        (2, "2s"),
        (59, "59s"),
        (60, "1m"),
        (300, "5m"),
        (3_600, "1h"),
        (5_400, "1h30m"),
        (3_661, "1h1m1s"),
        (86_400, "1d"),
        (90_061, "1d1h1m1s"),
    ] {
        assert_eq!(span_text(Duration::from_secs(seconds)), text, "{seconds}");
        assert_eq!(
            duration_text::parse(text),
            Ok(Duration::from_secs(seconds)),
            "{text} reads back"
        );
    }
}

// What a live frontend's human is told when a drain starts (OD-63).
#[test]
fn a_frontend_is_told_when_it_will_be_stopped() {
    assert_eq!(
        notice_text(Duration::from_secs(120)),
        "ctxloom is shutting down: this session will be stopped in 2m; finish and exit"
    );
}

#[test]
fn the_epoch_seconds_are_the_hosts_clock() {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    let read = epoch_seconds();
    assert!(read >= now && read <= now + 5, "{read} {now}");
}

// ------------------------- flush before install (owner, 2026-11-16)

const OWN: &str = "v0.2.0-aaaaaaa";
const ONE: &str = "projects/amber-quiet-heron/sessions/amber-swift-owl";
const TWO: &str = "projects/calm-otter/sessions/brisk-copper-moth";

fn root_of(name: &str) -> uuid::Uuid {
    sessions::SessionName::parse(name).unwrap().id()
}

/// A home's holds on a temporary file system.
fn spool() -> fact_spool::Spool {
    let fs = Arc::new(Rooted::temp().unwrap());
    let home = std::path::PathBuf::from(os::host().flavor().from_top("/home/a"));
    fact_spool::Spool::at_home(fs, &home)
}

/// A prompt's fact, held for `name` by a hook of `OWN`.
fn hold(held: &fact_spool::Spool, name: &str, id: &str) {
    let event = HookEvent {
        event: "turn_start".to_string(),
        prompt_id: id.to_string(),
        prompt: format!("prompt {id}"),
        ..HookEvent::default()
    };
    let observation = &observe(&event)[0];
    let session = angzarr_embedded::v1::Uuid::from_bytes(*root_of(name).as_bytes());
    let fact = HeldFact::of_book(
        OWN,
        name,
        observation.external_id(&session),
        &observation.book(&session),
        None,
    );
    assert_eq!(held.hold(&root_of(name), fact).unwrap(), Held::Kept);
}

/// A sink that answers every delivery `answer`.
struct Answering(Delivery);

impl FactSink for Answering {
    fn deliver(&self, _: CommandBook, _: Duration) -> Delivery {
        self.0.clone()
    }
}

fn soon() -> Deadline {
    Deadline(std::time::Instant::now() + Duration::from_secs(2))
}

fn flush(held: &fact_spool::Spool, answer: Delivery) {
    let flushed = flush_with(held, &soon(), OWN, OWN, &Answering(answer));
    assert!(matches!(flushed, Flushed::Done(_)), "{flushed:?}");
}

// A hold a daemon took is empty: what shutdown flushed leaves nothing to
// block an install.
#[test]
fn a_hold_the_daemon_took_is_empty() {
    let held = spool();
    hold(&held, ONE, "p1");
    hold(&held, TWO, "q1");
    assert_eq!(undelivered_in(&held).unwrap().len(), 2, "held, before");
    flush(&held, Delivery::Applied);
    assert_eq!(undelivered_in(&held).unwrap(), Vec::<Undelivered>::new());
}

// A hold no daemon took (the daemon unreachable: an unsettled delivery) is
// undelivered, by session and count.
#[test]
fn a_hold_no_daemon_took_is_undelivered_by_session_and_count() {
    let held = spool();
    hold(&held, ONE, "p1");
    hold(&held, ONE, "p2");
    flush(&held, Delivery::TimedOut);
    assert_eq!(
        undelivered_in(&held).unwrap(),
        vec![Undelivered {
            root: root_of(ONE),
            session: Some(ONE.to_string()),
            facts: 2,
            gaps: 0,
            unreadable: false,
        }]
    );
}

// What the daemon refused is set aside, never delivered again: it holds no
// record a daemon will decode, so it does not block (it is doctor's).
#[test]
fn what_the_daemon_refused_is_set_aside_and_does_not_block() {
    let held = spool();
    hold(&held, ONE, "p1");
    flush(
        &held,
        Delivery::Refused {
            detail: "no".to_string(),
        },
    );
    let survey = held.survey().unwrap();
    assert_eq!(survey.len(), 1, "its set-aside part is kept: {survey:?}");
    assert_eq!(survey[0].set_aside, 1);
    assert_eq!(undelivered_in(&held).unwrap(), Vec::<Undelivered>::new());
}

fn left(session: Option<&str>, root: &str, facts: u64, gaps: u64, unreadable: bool) -> Undelivered {
    Undelivered {
        root: uuid::Uuid::parse_str(root).unwrap(),
        session: session.map(str::to_string),
        facts,
        gaps,
        unreadable,
    }
}

const ROOT_A: &str = "5f0c7a52-1111-5111-8111-000000000001";
const ROOT_B: &str = "5f0c7a52-2222-5222-8222-000000000002";

// What is held is said by each session's harp (else its root) and count.
#[test]
fn the_holds_left_are_said_by_session_and_count() {
    assert_eq!(
        holds_said(&[
            left(Some(ONE), ROOT_A, 3, 0, false),
            left(Some(TWO), ROOT_A, 1, 1, false),
            left(None, ROOT_B, 0, 2, false),
            left(None, ROOT_B, 0, 0, true),
        ]),
        format!(
            "amber-swift-owl (3 facts); brisk-copper-moth (1 fact, 1 gap); {ROOT_B} (2 gaps); \
             {ROOT_B} (in a format this ctxloom cannot read)"
        )
    );
}

fn running(daemon: Option<u32>) -> Running {
    Running {
        daemon,
        frontends: Vec::new(),
    }
}

// The verdict: done when nothing runs and nothing is held; what still runs
// is a failure (32); a hold left undelivered a decline (48), the more
// severe, which wins beside what still runs.
#[test]
fn an_undelivered_hold_is_a_decline_that_outranks_what_still_runs() {
    assert_eq!(verdict(&running(None), &[]).unwrap(), Exit::OK);
    let failed = verdict(&running(Some(4)), &[]).unwrap_err();
    assert_eq!(failed.exit, Exit::FAILED);
    assert_eq!(failed.cause.code(), "ctxloom-wiring-shutdown-incomplete");
    let held = [left(Some(ONE), ROOT_A, 3, 0, false)];
    for still in [None, Some(4)] {
        let declined = verdict(&running(still), &held).unwrap_err();
        assert_eq!(declined.exit, Exit::REFUSED, "{still:?}");
        assert_eq!(declined.cause.code(), "ctxloom-wiring-shutdown-held");
        let args = format!("{:?}", declined.cause.args());
        assert!(args.contains("amber-swift-owl (3 facts)"), "{args}");
    }
}
