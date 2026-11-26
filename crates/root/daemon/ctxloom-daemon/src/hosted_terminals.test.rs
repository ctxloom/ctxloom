//! What the daemon reads back of a frontend's reports, and what a prepared
//! session's end says on its watch.

use angzarr_embedded::v1::{event_page, EventPage};
use angzarr_embedded::Any;
use daemon_proto::v1;
use run_application::{Done, Ended, RunError};

use super::{events_of, seen_in};

fn page(any: Any) -> EventPage {
    EventPage {
        body: Some(event_page::Body::Event(any)),
        ..EventPage::default()
    }
}

fn any_of<M: angzarr_embedded::markup::DomainMessage>(message: &M) -> Any {
    Any::from_msg(&message.to_wire()).unwrap()
}

#[test]
fn the_daemon_reads_the_start_then_the_end_as_the_exit() {
    let started = page(any_of(&runs::RunnerStartedRecorded {
        runtime: "claude-code".to_string(),
        workspace: String::new(),
        pid: 7,
    }));
    let ended = page(any_of(&runs::RunEnded {
        cause: runs::EndCause::RunnerExit,
        exit_code: Some(3),
        detail: String::new(),
    }));
    let seen = seen_in(std::slice::from_ref(&started), &[]);
    assert_eq!(seen.started, Some(7));
    assert_eq!(seen.exited, None);
    let seen = seen_in(&[started, ended.clone()], &[]);
    assert_eq!(seen.exited.map(|e| e.code), Some(Some(3)));
    assert!(seen.failed.is_none());
    // An end before any start: it never started.
    let seen = seen_in(&[ended], &[]);
    assert!(seen.failed.is_some());
    assert!(!seen.closed);
}

#[test]
fn the_daemon_reads_the_session_closed() {
    let closed = page(any_of(&sessions::SessionClosed {
        cause: "user".to_string(),
    }));
    assert!(seen_in(&[], &[closed]).closed);
}

#[test]
fn a_prepared_session_s_end_says_its_notices_then_its_end() {
    let ended = Done::<Ended> {
        result: Ok(Ended {
            harp: "h".to_string(),
            session: "s".to_string(),
            engine: "claude-code".to_string(),
            code: Some(0),
        }),
        notices: vec![library_application::Notice::text("left")],
    };
    let events = events_of(&ended);
    assert_eq!(events.len(), 2);
    assert!(matches!(
        events[0].event,
        Some(v1::run_event::Event::Notice(_))
    ));
    assert_eq!(
        events[1].event,
        Some(v1::run_event::Event::End(v1::RunEnd::default()))
    );
    let failed = Done::<Ended> {
        result: Err(RunError::NotEnded {
            run: "r".to_string(),
        }),
        notices: Vec::new(),
    };
    let events = events_of(&failed);
    assert!(matches!(
        &events[0].event,
        Some(v1::run_event::Event::End(end)) if end.code == "run-application-not-ended"
    ));
}
