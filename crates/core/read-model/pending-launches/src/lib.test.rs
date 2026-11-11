use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Any, Payload, ProjectorHandler};
use runs::{
    run_root, EndCause, RunEnded, RunEnqueued, RunEvent, RunMode, RunnerAttachedRecorded,
    RunnerStartedRecorded, TurnStartedRecorded, RUN_DOMAIN,
};
use sessions::{IncarnationOwed, SessionEvent, SESSION_DOMAIN};

use crate::{catch_up, Owed, Pending, PendingLaunch, PendingModel, StartedRun};

/// The session stream root `n`, and its text as a run names its session.
fn session_root(n: u8) -> (Uuid, String) {
    let id = uuid::Uuid::from_bytes([n; 16]);
    (Uuid::from_bytes([n; 16]), id.hyphenated().to_string())
}

fn page(sequence: u64, any: Any) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(any)),
        meta: None,
    }
}

fn book(domain: &str, root: Uuid, pages: Vec<EventPage>) -> EventBook {
    let next = pages.last().map_or(0, |p| p.sequence + 1);
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(root),
            edition: None,
        }),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

fn owed(n: u8, incarnation: u32) -> EventBook {
    let (root, _) = session_root(n);
    let event = SessionEvent::IncarnationOwed(IncarnationOwed {
        incarnation,
        reason: "new".to_string(),
    });
    book(SESSION_DOMAIN, root, vec![page(0, event.to_any())])
}

fn enqueued(n: u8, incarnation: u32, mode: RunMode) -> EventBook {
    let (_, session) = session_root(n);
    let root = run_root(&session, incarnation);
    let event = RunEvent::RunEnqueued(RunEnqueued {
        session,
        incarnation,
        mode,
        runner_credential_hash: vec![1; 32],
        model_credential_hash: vec![2; 32],
        hook_credential_hash: vec![3; 32],
        launcher_lock: lock_of(n),
    });
    book(RUN_DOMAIN, root, vec![page(0, event.to_any())])
}

/// The lock of session `n`'s runs (rulings 86, 92).
fn lock_of(n: u8) -> String {
    format!("/h/.ctxloom/runs/{n}.lock")
}

/// A later event of the run of session `n`'s `incarnation`, at `sequence`.
fn later(n: u8, incarnation: u32, sequence: u64, event: RunEvent) -> EventBook {
    let (_, session) = session_root(n);
    book(
        RUN_DOMAIN,
        run_root(&session, incarnation),
        vec![page(sequence, event.to_any())],
    )
}

fn attached() -> RunEvent {
    RunEvent::RunnerAttachedRecorded(RunnerAttachedRecorded {
        runner_id: "r".to_string(),
        protocol: "v1".to_string(),
    })
}

fn ended_by(cause: EndCause) -> RunEvent {
    RunEvent::RunEnded(RunEnded {
        cause,
        exit_code: None,
        detail: String::new(),
    })
}

fn pending(model: &Pending) -> Vec<(u8, u32, RunMode)> {
    model
        .launches()
        .into_iter()
        .map(|l| (l.session.as_bytes()[1] - b'0', l.incarnation, l.mode))
        .collect()
}

fn fold(books: &[EventBook]) -> Pending {
    let mut model = Pending::new();
    for b in books {
        model.project(b).unwrap();
    }
    model
}

#[test]
fn an_enqueued_run_is_owed_a_runner() {
    let model = fold(&[enqueued(1, 1, RunMode::OneShot)]);
    let launches = model.launches();
    assert_eq!(launches.len(), 1);
    let (_, session) = session_root(1);
    assert_eq!(
        launches[0],
        PendingLaunch {
            run: run_root(&session, 1),
            session,
            incarnation: 1,
            mode: RunMode::OneShot,
            launcher_lock: lock_of(1),
        }
    );
}

#[test]
fn an_incarnation_owed_is_listed_until_its_run_is_enqueued() {
    let mut model = fold(&[owed(1, 1)]);
    let (_, session) = session_root(1);
    assert_eq!(
        model.owed(),
        vec![Owed {
            session: session.clone(),
            incarnation: 1
        }]
    );
    assert!(model.launches().is_empty(), "no run yet: nothing to launch");
    model.project(&enqueued(1, 1, RunMode::OneShot)).unwrap();
    assert!(model.owed().is_empty());
    assert_eq!(model.launches().len(), 1);
}

#[test]
fn an_incarnation_owed_after_its_run_was_enqueued_is_not_owed_again() {
    // The streams of two domains arrive in no order across each other.
    let model = fold(&[enqueued(1, 1, RunMode::OneShot), owed(1, 1)]);
    assert!(model.owed().is_empty());
    assert_eq!(model.launches().len(), 1);
}

fn started() -> RunEvent {
    RunEvent::RunnerStartedRecorded(RunnerStartedRecorded {
        runtime: "claude-code".to_string(),
        workspace: "/w".to_string(),
        pid: 7,
    })
}

#[test]
fn a_run_whose_runner_was_started_is_no_longer_owed() {
    let model = fold(&[
        enqueued(1, 1, RunMode::OneShot),
        enqueued(2, 1, RunMode::Structured),
        later(1, 1, 1, started()),
    ]);
    assert_eq!(pending(&model), vec![(2, 1, RunMode::Structured)]);
}

// The double launch this closes: a turn spawned and silent has recorded only
// its start. A runner's first sign of life is not what a launch waits for, and
// a run with only signs of life (an attach, a turn started) but no start was
// never launched by this model's reckoning, so it is still owed.
#[test]
fn a_started_runner_that_says_nothing_is_not_owed_again() {
    let silent = fold(&[enqueued(1, 1, RunMode::OneShot), later(1, 1, 1, started())]);
    assert!(silent.launches().is_empty());
}

#[test]
fn a_sign_of_life_without_a_start_does_not_clear_the_launch() {
    let model = fold(&[
        enqueued(1, 1, RunMode::OneShot),
        enqueued(2, 1, RunMode::Structured),
        later(1, 1, 1, attached()),
        later(
            2,
            1,
            1,
            RunEvent::TurnStartedRecorded(TurnStartedRecorded { turn: 1 }),
        ),
    ]);
    assert_eq!(
        pending(&model),
        vec![(1, 1, RunMode::OneShot), (2, 1, RunMode::Structured)]
    );
}

#[test]
fn a_run_that_ended_before_it_started_is_no_longer_owed() {
    let model = fold(&[
        enqueued(1, 1, RunMode::OneShot),
        later(1, 1, 1, ended_by(EndCause::LaunchFailed)),
    ]);
    assert!(model.launches().is_empty());
}

#[test]
fn launches_are_ordered_by_session_then_incarnation() {
    let model = fold(&[
        enqueued(2, 1, RunMode::OneShot),
        enqueued(1, 2, RunMode::OneShot),
        enqueued(1, 1, RunMode::OneShot),
    ]);
    assert_eq!(
        pending(&model),
        vec![
            (1, 1, RunMode::OneShot),
            (1, 2, RunMode::OneShot),
            (2, 1, RunMode::OneShot)
        ]
    );
}

#[test]
fn a_stream_projected_again_from_its_start_is_folded_afresh() {
    let mut model = fold(&[enqueued(1, 1, RunMode::OneShot), later(1, 1, 1, started())]);
    assert!(model.launches().is_empty());
    // The same stream replayed from sequence 0, as a rebuild hands it.
    model.project(&enqueued(1, 1, RunMode::OneShot)).unwrap();
    assert_eq!(model.launches().len(), 1);
    // And the same session's owed, replayed, does not double.
    model.project(&owed(4, 1)).unwrap();
    model.project(&owed(4, 1)).unwrap();
    assert_eq!(model.owed().len(), 1);
}

#[test]
fn a_purged_stream_leaves_the_model() {
    let mut model = fold(&[enqueued(1, 1, RunMode::OneShot), owed(2, 1)]);
    let (_, session) = session_root(1);
    let gone = book(RUN_DOMAIN, run_root(&session, 1), vec![]);
    model.project(&gone).unwrap();
    assert!(model.launches().is_empty());
    let (root, _) = session_root(2);
    model.project(&book(SESSION_DOMAIN, root, vec![])).unwrap();
    assert!(model.owed().is_empty());
}

#[test]
fn another_domains_event_is_not_this_models() {
    // A meta page (no event body) is skipped, an unknown domain ignored.
    let (root, _) = session_root(1);
    let meta = EventPage {
        sequence: 0,
        body: None,
        meta: None,
    };
    let mut model = Pending::new();
    model
        .project(&book(RUN_DOMAIN, root.clone(), vec![meta]))
        .unwrap();
    model
        .project(&book("elsewhere", root, vec![page(0, Any::default())]))
        .unwrap();
    assert!(model.launches().is_empty() && model.owed().is_empty());
}

#[test]
fn the_model_is_a_projector_that_reads_both_domains() {
    let model = PendingModel::default();
    assert_eq!(model.name(), "pending-launches");
    let request = ProjectRequest {
        events: Some(enqueued(1, 1, RunMode::OneShot)),
    };
    ready(model.dispatch(&request)).unwrap();
    assert_eq!(model.pending().launches().len(), 1);
    let _ = catch_up();
}

#[test]
fn an_unreadable_event_fails_the_projector_with_a_code_and_the_type_url() {
    let model = PendingModel::default();
    let (root, _) = session_root(1);
    let stranger = page(
        0,
        Any {
            type_url: "/toy.v1.Stranger".to_string(),
            value: vec![],
        },
    );
    let error = ready(model.dispatch(&ProjectRequest {
        events: Some(book(RUN_DOMAIN, root, vec![stranger])),
    }))
    .unwrap_err();
    assert!(!error.code.is_empty());
    assert!(
        error.extras.values().any(|v| v.contains("toy.v1.Stranger")),
        "{:?}",
        error.extras
    );
}

// Only a book from sequence 0 starts a stream afresh: a later page of a stream
// leaves what the model already holds of it, run and session alike.
#[test]
fn a_book_after_the_first_page_does_not_start_its_stream_afresh() {
    let failed = RunEvent::TurnFailedRecorded(runs::TurnFailedRecorded {
        turn: 1,
        class: runs::TurnFailureClass::RateLimited,
        detail: String::new(),
    });
    let model = fold(&[enqueued(1, 1, RunMode::OneShot), later(1, 1, 1, failed)]);
    assert_eq!(
        pending(&model),
        vec![(1, 1, RunMode::OneShot)],
        "the run is still owed its runner"
    );
    let (root, _) = session_root(3);
    let second = {
        let event = SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "again".to_string(),
        });
        book(SESSION_DOMAIN, root, vec![page(1, event.to_any())])
    };
    let model = fold(&[owed(3, 1), second]);
    assert_eq!(model.owed().len(), 2);
}

// And a book from sequence 0 replaces what the model held of the stream.
#[test]
fn a_book_from_the_first_page_replaces_what_the_session_owed() {
    let mut model = fold(&[owed(3, 1)]);
    model.project(&owed(3, 2)).unwrap();
    let incarnations: Vec<u32> = model.owed().iter().map(|o| o.incarnation).collect();
    assert_eq!(incarnations, vec![2]);
}

// Rulings 86, 92: each run owed a runner carries its own lock, so a later
// process can judge whether its launcher still holds it.
#[test]
fn a_run_owed_a_runner_carries_its_launchers_lock() {
    let model = fold(&[
        enqueued(1, 1, RunMode::OneShot),
        enqueued(2, 1, RunMode::Interactive),
    ]);
    let locks: Vec<String> = model
        .launches()
        .into_iter()
        .map(|l| l.launcher_lock)
        .collect();
    assert_eq!(locks, [lock_of(1), lock_of(2)]);
}

/// Session `n` minted as `harp` in `/work/p{n}`, then renamed `renamed` when
/// given, as its stream's book.
fn minted(n: u8, harp: &str, renamed: Option<&str>) -> EventBook {
    let (root, _) = session_root(n);
    let mut pages = vec![page(
        0,
        SessionEvent::SessionMinted(sessions::SessionMinted {
            harp: harp.to_string(),
            project_id: "p".to_string(),
            project_dir: format!("/work/p{n}"),
            output_dir: String::new(),
            engine: "fake".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
        })
        .to_any(),
    )];
    if let Some(to) = renamed {
        pages.push(page(
            1,
            SessionEvent::RenamedTo(sessions::RenamedTo {
                harp: to.to_string(),
                output_dir: String::new(),
            })
            .to_any(),
        ));
    }
    book(SESSION_DOMAIN, root, pages)
}

// Ruling 93: a run started and not ended is listed with its session's
// project and CURRENT harp (its lock moves with a rename), until it ends.
#[test]
fn a_started_run_is_listed_with_its_sessions_lock_until_it_ends() {
    let (_, session) = session_root(1);
    let run = run_root(&session, 1);
    let mut model = fold(&[
        minted(1, "amber-swift-owl", Some("bright-keen-hawk")),
        enqueued(1, 1, RunMode::Interactive),
        later(1, 1, 1, started()),
    ]);
    assert_eq!(
        model.started(),
        [StartedRun {
            run,
            session,
            incarnation: 1,
            project_dir: "/work/p1".to_string(),
            harp: "bright-keen-hawk".to_string(),
        }]
    );
    assert!(
        model.launches().is_empty(),
        "started: no longer owed a runner"
    );
    model
        .project(&later(1, 1, 2, ended_by(EndCause::RunnerExit)))
        .unwrap();
    assert!(model.started().is_empty(), "ended: no longer started");
}

// Ruling 93: a run still owed a runner is not a started one.
#[test]
fn a_run_owed_a_runner_is_not_started() {
    let model = fold(&[
        minted(1, "amber-swift-owl", None),
        enqueued(1, 1, RunMode::OneShot),
    ]);
    assert!(model.started().is_empty());
    assert_eq!(model.launches().len(), 1);
}
