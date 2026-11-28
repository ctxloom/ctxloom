//! `fragment|command distill` served (step 10b): how a write that was not
//! made ends the run. The ask, the
//! run and the write end to end are content_distill.feature's (the mock
//! engine as the model).

use coded_diagnostic::Diagnostic;
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};

use super::*;
use daemon_api_contract::ApiRefusal;

const RUN: &str = "0195f0a4-7b1e-7000-8000-000000000001";

// The write is keyed by its run: the same outcome told twice is one write
// (a DUPLICATE), and two runs' are two.
#[test]
fn test_a_distill_s_write_is_keyed_by_its_run() {
    let key = write_key(RUN);
    assert_eq!(key, format!("distill:{RUN}"));
    let id = keyed_request_id(&key);
    assert!(uuid::Uuid::parse_str(&id).is_ok(), "a uuid: {id}");
    assert_eq!(id, keyed_request_id(&write_key(RUN)), "stable");
    assert_ne!(
        id,
        keyed_request_id(&write_key("0195f0a4-7b1e-7000-8000-000000000002")),
        "one per run"
    );
}

#[test]
fn test_a_write_not_made_ends_the_run_in_its_own_words() {
    let stale = ApiError::Refused(crate::library_refusal(
        &library_tree::LibraryTreeRejection::PlanStale {
            path: "bundles/lore/fragments/rules.distilled.md".to_string(),
            seen: 3,
            now: 4,
        },
    ));
    let ended = unwritten(&stale);
    assert_eq!(ended.code(), "library-tree-plan-stale");
    assert_eq!(ended.exit(), Exit::REFUSED);

    let moved = ApiError::Refused(ApiRefusal::of(&v1::LibraryDistillSourceMoved {
        reference: "lore#fragments/rules".to_string(),
    }));
    let ended = unwritten(&moved);
    assert_eq!(ended.code(), "library-application-distill-source-moved");
    assert_eq!(ended.exit(), Exit::REFUSED);

    let broke = ApiError::Failed {
        detail: "the store".to_string(),
    };
    let ended = unwritten(&broke);
    assert_eq!(ended.code(), "daemon-api-contract-failed");
    assert_eq!(ended.exit(), Exit::FAILED);
}

// Owner 2026-11-28: what a distill's write came to is a fact of its run, and
// the run's watch says it past the run's own end.

const SESSION_ID: &str = "5f0c7a52-4444-5444-8444-000000000004";
const FRAGMENT: &str = "projects/calm-quiet-heron/bundles/demo/fragments/x";

/// The run of `session`'s first incarnation, as text.
fn run_text(session: &str) -> String {
    uuid::Uuid::from_slice(&runs::run_root(session, 1).value)
        .unwrap()
        .hyphenated()
        .to_string()
}

fn run_cover(session: &str) -> angzarr_embedded::v1::Cover {
    angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(session, 1)),
        edition: None,
    }
}

/// A distill run of `session` with its answer recorded, as the policy finds
/// it.
async fn answered_run(f: &crate::server::tests::Fixture, session: &str) {
    use angzarr_embedded::CommandBookExt as _;
    let cover = run_cover(session);
    let enqueue = runs::EnqueueRun {
        session: session.to_string(),
        incarnation: 1,
        mode: runs::RunMode::Structured,
        prompt_given: true,
        engine: "claude-code".to_string(),
        drivable_engines: vec!["claude-code".to_string()],
        credential: runs::CredentialEvidence::default(),
        fatal_findings: 0,
        fatal: Vec::new(),
        launcher_lock: String::new(),
        untrusted_settings: Vec::new(),
        grant: None,
        spec: Some(runs::LaunchSpec {
            distill: Some(runs::DistillPurpose {
                reference: FRAGMENT.to_string(),
                kind: "fragment".to_string(),
                digest: "d1".to_string(),
                etag: "3".to_string(),
                cwd: "/work".to_string(),
            }),
            ..Default::default()
        }),
    };
    let queue = runs::QueueTurn {
        prompt_ref: String::new(),
        resume: false,
        message_ids: Vec::new(),
        urgency: runs::TurnUrgency::Wake,
    };
    for command in [
        runs::RunCommand::from(enqueue),
        runs::RunCommand::from(queue),
    ] {
        f.world
            .handle(angzarr_embedded::v1::CommandBook::command_of(
                cover.clone(),
                &command,
            ))
            .await
            .unwrap();
    }
    let completed = runs::RecordTurnCompleted {
        turn: 1,
        answer: "SHORT".to_string(),
        ..Default::default()
    };
    f.world
        .handle(sessions::fact_book(cover, &completed, "turn:d:1:completed"))
        .await
        .unwrap();
}

/// What the run's stream recorded of the write: its end and its notices.
async fn recorded(
    f: &crate::server::tests::Fixture,
    session: &str,
) -> Option<(Vec<u8>, Vec<Vec<u8>>)> {
    use angzarr_embedded::Payload as _;
    use run_ports::Edge as _;
    f.world
        .events(&run_cover(session), 0)
        .await
        .unwrap()
        .into_iter()
        .find_map(|page| {
            let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = page.body else {
                return None;
            };
            match runs::RunEvent::from_any(&any) {
                Ok(runs::RunEvent::DistillWritten(written)) => Some((written.end, written.notices)),
                _ => None,
            }
        })
}

fn notice(text: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::Notice(v1::Notice {
            text: text.to_string(),
            ..v1::Notice::default()
        })),
    }
}

fn event_end(exit: i32, code: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::End(v1::RunEnd {
            exit,
            code: code.to_string(),
            ..v1::RunEnd::default()
        })),
    }
}

/// The watch of `session`'s run over `inner`, read to its end.
async fn watched_distill(
    f: &crate::server::tests::Fixture,
    session: &str,
    inner: Vec<v1::RunEvent>,
) -> Vec<v1::RunEvent> {
    let watch = DistillWatch::new(
        Box::new(inner.into_iter()),
        Arc::clone(&f.world),
        (&run_text(session), FRAGMENT),
    );
    tokio::task::spawn_blocking(move || watch.collect::<Vec<_>>())
        .await
        .unwrap()
}

#[tokio::test(flavor = "multi_thread")]
async fn a_written_distill_is_recorded_on_its_run_with_the_notices_it_said() {
    let f = crate::server::tests::fixture(Vec::new());
    answered_run(&f, SESSION_ID).await;
    assert!(recorded(&f, SESSION_ID).await.is_none(), "not yet");
    let said = v1::Notice {
        text: "distilled".to_string(),
        ..v1::Notice::default()
    };
    f.api
        .record_outcome(&run_text(SESSION_ID), Ok(vec![said.clone()]))
        .await
        .unwrap();
    let (end, notices) = recorded(&f, SESSION_ID).await.expect("DistillWritten");
    assert!(end.is_empty(), "written: the run ends as its engine did");
    let [notice] = notices.as_slice() else {
        panic!("one notice");
    };
    assert_eq!(
        <v1::Notice as prost::Message>::decode(notice.as_slice()).unwrap(),
        said
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_stale_source_is_recorded_as_the_refusal_that_ends_the_runs_watch() {
    let f = crate::server::tests::fixture(Vec::new());
    answered_run(&f, SESSION_ID).await;
    let moved = ApiError::Refused(ApiRefusal::of(&v1::LibraryDistillSourceMoved {
        reference: "demo#fragments/x".to_string(),
    }));
    f.api
        .record_outcome(&run_text(SESSION_ID), Err(moved))
        .await
        .unwrap();
    let (end, _) = recorded(&f, SESSION_ID).await.expect("DistillWritten");
    let end = <v1::RunEnd as prost::Message>::decode(end.as_slice()).unwrap();
    assert_eq!(end.code, "library-application-distill-source-moved");
    assert_eq!(end.exit, i32::from(Exit::REFUSED.code(false)));
    // The run itself ended well; its watch ends as the write was refused,
    // after the notices it had said.
    let events = watched_distill(&f, SESSION_ID, vec![notice("before"), event_end(0, "")]).await;
    assert_eq!(events.len(), 2, "{events:?}");
    let Some(v1::run_event::Event::End(ended)) = &events[1].event else {
        panic!("an end: {events:?}");
    };
    assert_eq!(ended.code, "library-application-distill-source-moved");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_written_distills_watch_says_its_notices_then_ends_as_the_run_did() {
    let f = crate::server::tests::fixture(Vec::new());
    answered_run(&f, SESSION_ID).await;
    let said = v1::Notice {
        text: "distilled".to_string(),
        ..v1::Notice::default()
    };
    f.api
        .record_outcome(&run_text(SESSION_ID), Ok(vec![said.clone()]))
        .await
        .unwrap();
    let events = watched_distill(&f, SESSION_ID, vec![event_end(0, "")]).await;
    assert_eq!(events.len(), 2, "{events:?}");
    assert_eq!(
        events[0].event,
        Some(v1::run_event::Event::Notice(said)),
        "the write's notice first"
    );
    assert_eq!(events[1], event_end(0, ""), "then the run's own end");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_distill_run_that_failed_ends_as_a_failed_distill_without_waiting() {
    let f = crate::server::tests::fixture(Vec::new());
    let events = watched_distill(
        &f,
        SESSION_ID,
        vec![event_end(32, "run-application-engine-failed")],
    )
    .await;
    let [only] = events.as_slice() else {
        panic!("one end: {events:?}");
    };
    let Some(v1::run_event::Event::End(end)) = &only.event else {
        panic!("an end");
    };
    assert_eq!(end.code, "library-application-distill-failed");
    assert_eq!(end.exit, 32);
}
