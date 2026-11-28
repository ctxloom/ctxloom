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

/// A purge's tombstone at `at` on the stream `root`, then `pages`.
fn closed(domain: &str, root: Uuid, at: u64, pages: Vec<EventPage>) -> EventBook {
    let mut all = vec![angzarr_embedded::angzarr_ports::tombstone::page(at)];
    all.extend(pages);
    book(domain, root, all)
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
        launcher_lock: lock_of(n),
        grant: None,
        spec: None,
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
            project_dir: String::new(),
            project_id: String::new(),
            harp: String::new(),
            spec: None,
            grant: None,
            engine: String::new(),
            workspace: None,
            agent: String::new(),
            resume: None,
        }
    );
}

// A run owed a runner is listed with its session's place once its mint is
// seen: a daemon that adopts it lets its checkout go in that project.
#[test]
fn a_run_owed_a_runner_is_listed_with_its_sessions_place() {
    let model = fold(&[
        minted(1, "amber-swift-owl"),
        enqueued(1, 1, RunMode::Interactive),
    ]);
    let launches = model.launches();
    assert_eq!(launches.len(), 1);
    assert_eq!(launches[0].project_dir, "/work/p1");
    assert_eq!(launches[0].harp, "amber-swift-owl");
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
            RunEvent::TurnStartedRecorded(TurnStartedRecorded {
                turn: 1,
                message_ids: Vec::new(),
            }),
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
fn a_stream_closed_by_a_tombstone_is_folded_afresh_after_it() {
    let mut model = fold(&[enqueued(1, 1, RunMode::OneShot), later(1, 1, 1, started())]);
    assert!(model.launches().is_empty());
    // The run's stream purged and enqueued again: its events are deleted,
    // a tombstone stands at 2, and the new enqueue follows it.
    let (_, session) = session_root(1);
    let event = RunEvent::RunEnqueued(RunEnqueued {
        session: session.clone(),
        incarnation: 1,
        mode: RunMode::OneShot,
        launcher_lock: lock_of(1),
        grant: None,
        spec: None,
    });
    model
        .project(&closed(
            RUN_DOMAIN,
            run_root(&session, 1),
            2,
            vec![page(3, event.to_any())],
        ))
        .unwrap();
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
    let gone = closed(RUN_DOMAIN, run_root(&session, 1), 1, vec![]);
    model.project(&gone).unwrap();
    assert!(model.launches().is_empty());
    let (root, _) = session_root(2);
    model
        .project(&closed(SESSION_DOMAIN, root, 1, vec![]))
        .unwrap();
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
        message_ids: Vec::new(),
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

// A tombstone replaces what the model held of the stream: what follows it is
// all the session owes.
#[test]
fn a_book_after_a_tombstone_replaces_what_the_session_owed() {
    let mut model = fold(&[owed(3, 1)]);
    let (root, _) = session_root(3);
    let event = SessionEvent::IncarnationOwed(IncarnationOwed {
        incarnation: 2,
        reason: "new".to_string(),
    });
    model
        .project(&closed(
            SESSION_DOMAIN,
            root,
            1,
            vec![page(2, event.to_any())],
        ))
        .unwrap();
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

/// Session `n` minted as `harp` in `/work/p{n}`, as its stream's book.
fn minted(n: u8, harp: &str) -> EventBook {
    let (root, _) = session_root(n);
    let pages = vec![page(
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
            workspace: None,
        })
        .to_any(),
    )];
    book(SESSION_DOMAIN, root, pages)
}

// Ruling 93: a run started and not ended is listed with its session's
// project and harp (its lock's place), until it ends;
// and with its own lock, which a host holding it for the run's life lets go
// only with the run (T3, T18).
#[test]
fn a_started_run_is_listed_with_its_sessions_lock_until_it_ends() {
    let (_, session) = session_root(1);
    let run = run_root(&session, 1);
    let mut model = fold(&[
        minted(1, "amber-swift-owl"),
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
            harp: "amber-swift-owl".to_string(),
            launcher_lock: lock_of(1),
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
        minted(1, "amber-swift-owl"),
        enqueued(1, 1, RunMode::OneShot),
    ]);
    assert!(model.started().is_empty());
    assert_eq!(model.launches().len(), 1);
}

#[path = "bus.test.rs"]
mod bus;

// P3 (owner ruling 2026-11-25): the launcher acts only once the run's session
// has its mint AND its loadout recorded, and angzarr orders events within a
// stream only (decision 14.35), so the condition is on FOLDED STATE and every
// interleaving of the run's stream and the session's is forced here, never
// waited for. RED: `launchable` lists a run on its enqueue alone.

/// The loadout of session `n`'s `incarnation`, as the daemon records it.
fn loadout(n: u8, incarnation: u32, sequence: u64) -> EventBook {
    let (root, _) = session_root(n);
    let event = SessionEvent::LoadoutRecorded(sessions::LoadoutRecorded {
        loadout: sessions::SessionLoadout {
            agent: "reviewer".to_string(),
            ..Default::default()
        },
        incarnation,
        covered: Vec::new(),
    });
    book(SESSION_DOMAIN, root, vec![page(sequence, event.to_any())])
}

fn mint_page_after(n: u8) -> EventBook {
    // The mint is the session stream's first page; a loadout follows it.
    minted(n, "kid")
}

fn launchable(model: &Pending) -> Vec<(u8, u32)> {
    model
        .launchable()
        .into_iter()
        .map(|l| (l.session.as_bytes()[1] - b'0', l.incarnation))
        .collect()
}

#[test]
fn a_run_enqueued_before_its_sessions_mint_and_loadout_waits() {
    let model = fold(&[enqueued(1, 1, RunMode::Structured)]);
    assert_eq!(pending(&model).len(), 1, "it is still owed a runner");
    assert_eq!(launchable(&model), Vec::<(u8, u32)>::new());
}

#[test]
fn a_run_whose_session_is_minted_but_has_no_loadout_waits() {
    let model = fold(&[enqueued(1, 1, RunMode::Structured), mint_page_after(1)]);
    assert_eq!(launchable(&model), Vec::<(u8, u32)>::new());
}

// Every order the three streams' books can arrive in launches the run, once
// all three are folded: the enqueue is the run stream's, the mint and the
// loadout the session stream's (its pages in order, within it).
#[test]
fn a_run_is_launchable_once_mint_and_loadout_are_folded_in_any_cross_stream_order() {
    let run = || enqueued(1, 1, RunMode::Structured);
    let session_first = || mint_page_after(1);
    let orders: Vec<Vec<EventBook>> = vec![
        vec![run(), session_first(), loadout(1, 1, 1)],
        vec![session_first(), run(), loadout(1, 1, 1)],
        vec![session_first(), loadout(1, 1, 1), run()],
    ];
    for books in orders {
        let model = fold(&books);
        assert_eq!(launchable(&model), vec![(1, 1)]);
    }
}

// A loadout is of an incarnation: the first incarnation's does not launch the
// second's run.
#[test]
fn a_loadout_of_another_incarnation_does_not_launch_the_run() {
    let model = fold(&[
        mint_page_after(1),
        loadout(1, 1, 1),
        enqueued(1, 2, RunMode::Structured),
    ]);
    assert_eq!(launchable(&model), Vec::<(u8, u32)>::new());
    let model = fold(&[
        mint_page_after(1),
        loadout(1, 1, 1),
        loadout(1, 2, 2),
        enqueued(1, 2, RunMode::Structured),
    ]);
    assert_eq!(launchable(&model), vec![(1, 2)]);
}

// The launcher acts on the record alone: the listed launch carries the
// spec its enqueue recorded.
#[test]
fn a_launchable_run_carries_the_spec_its_enqueue_recorded() {
    let (_, session) = session_root(1);
    let spec = runs::LaunchSpec {
        model: "claude-sonnet-5".to_string(),
        session_home: "session".to_string(),
        context: "FRAGMENT-BODY-testing".to_string(),
        ..Default::default()
    };
    let event = RunEvent::RunEnqueued(RunEnqueued {
        session: session.clone(),
        incarnation: 1,
        mode: RunMode::Structured,
        launcher_lock: lock_of(1),
        grant: None,
        spec: Some(spec.clone()),
    });
    let enqueue = book(
        RUN_DOMAIN,
        run_root(&session, 1),
        vec![page(0, event.to_any())],
    );
    let model = fold(&[mint_page_after(1), loadout(1, 1, 1), enqueue]);
    let launches = model.launchable();
    assert_eq!(launches.len(), 1);
    assert_eq!(launches[0].spec, Some(spec));
}

// A session purged (its stream closed by a tombstone) loses its mint and
// loadout: its run is no longer launchable.
#[test]
fn a_session_stream_closed_by_a_purge_forgets_its_mint_and_loadout() {
    let mut model = fold(&[
        mint_page_after(1),
        loadout(1, 1, 1),
        enqueued(1, 1, RunMode::Structured),
    ]);
    assert_eq!(launchable(&model), vec![(1, 1)]);
    let (root, _) = session_root(1);
    model
        .project(&closed(SESSION_DOMAIN, root, 2, vec![]))
        .unwrap();
    assert_eq!(launchable(&model), Vec::<(u8, u32)>::new());
}

// What the launcher needs besides the spec is the session's own events': the
// mint's engine and workspace axis, the loadout's agent, and (a later
// incarnation) the engine's own session bound; and the enqueue's grant.
#[test]
fn a_launchable_run_carries_what_the_sessions_events_say_of_the_launch() {
    let (root, session) = session_root(1);
    let bound = SessionEvent::EngineSessionBound(sessions::EngineSessionBound {
        native_id: "native-1".to_string(),
        transcript_path: String::new(),
        engine_version: String::new(),
        displaced: None,
    });
    let grant = runs::Grant {
        mcp_servers: vec!["ctxloom".to_string()],
        permissions: "{\"defaultMode\":\"plan\"}".to_string(),
    };
    let run = |incarnation| {
        let event = RunEvent::RunEnqueued(RunEnqueued {
            session: session.clone(),
            incarnation,
            mode: RunMode::Structured,
            launcher_lock: lock_of(1),
            grant: Some(grant.clone()),
            spec: None,
        });
        book(
            RUN_DOMAIN,
            run_root(&session, incarnation),
            vec![page(0, event.to_any())],
        )
    };
    let first = fold(&[mint_page_after(1), loadout(1, 1, 1), run(1)]);
    let launch = &first.launchable()[0];
    assert_eq!(launch.engine, "fake");
    assert_eq!(launch.agent, "reviewer");
    assert_eq!(launch.grant, Some(grant.clone()));
    assert_eq!(launch.resume, None, "the first incarnation resumes nothing");
    let later = fold(&[
        mint_page_after(1),
        book(SESSION_DOMAIN, root, vec![page(1, bound.to_any())]),
        loadout(1, 2, 2),
        run(2),
    ]);
    assert_eq!(later.launchable()[0].resume.as_deref(), Some("native-1"));
}
