//! A run on angzarr-embedded, driven through its kernel as the binary drives
//! it (`Harness`, over an in-memory store). RED (S5, design-by-test): the
//! aggregate is a stub. Each test says PORTED-FROM-GO (naming the Go test it
//! ports) or NEW-DESIGN (needs human review; the plan section it follows).

use std::collections::BTreeMap;

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin};
use angzarr_embedded::{Any, Failure, Handled, Payload, Registry};
use coded_diagnostic::{Arg, Diagnostic};

use crate::{
    run_root, CredentialEvidence, EndCause, EnqueueRun, Grant, QueueTurn, RecordFinalReported,
    RecordLaunchFailed, RecordLaunchOrphaned, RecordRunExited, RecordRunnerAttached,
    RecordRunnerLost, RecordRunnerStarted, RecordSummaryReported, RecordTurnCompleted,
    RecordTurnFailed, RecordTurnStarted, RequestStop, RunAggregate, RunCommand, RunEnded, RunEvent,
    RunMode, RunRejection, RunState, TurnFailureClass, RUN_DOMAIN,
};

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";
// Two principals per run (owner ruling 2026-11-04, item 16): a hash each.

pub(crate) type Said = (String, BTreeMap<String, String>);

/// The domain's rejection a typed refusal is, by its type.
fn refused(rejected: &angzarr_embedded::Rejected) -> RunRejection {
    let typed = rejected
        .rejection::<angzarr_embedded::markup::TypedRefusal>()
        .expect("a typed refusal");
    RunRejection::try_from(typed).expect("a refusal of this domain")
}

fn said(diagnostic: &dyn Diagnostic) -> Said {
    let args = diagnostic
        .args()
        .into_iter()
        .map(|(name, arg)| {
            let text = match arg {
                Arg::Int(n) => n.to_string(),
                Arg::Uint(n) => n.to_string(),
                Arg::Str(s) => s,
                Arg::Nested(cause) => cause.code().to_string(),
            };
            (name.to_string(), text)
        })
        .collect();
    (diagnostic.code().to_string(), args)
}

fn cover() -> Cover {
    Cover {
        domain: RUN_DOMAIN.to_string(),
        root: Some(run_root(SESSION, 1)),
        edition: None,
    }
}

pub(crate) struct Run {
    harness: Harness<RunAggregate>,
}

impl Run {
    pub(crate) fn new() -> Run {
        let mut registry = Registry::new();
        registry
            .aggregate::<RunAggregate>(RunAggregate::default)
            .unwrap();
        Run {
            harness: Harness::new(registry, cover()).with_samples(Samples::starting_at(100)),
        }
    }

    pub(crate) fn enqueued() -> Run {
        let mut run = Run::new();
        run.command(enqueue()).unwrap();
        run
    }

    /// The rejection a command is refused with, as the domain reads it back.
    fn rejection(&mut self, command: impl Into<RunCommand>) -> RunRejection {
        match self.harness.when(command).result() {
            Err(Failure::Rejected(rejected)) => refused(&rejected),
            other => panic!("not a rejection: {other:?}"),
        }
    }

    pub(crate) fn command(
        &mut self,
        command: impl Into<RunCommand>,
    ) -> Result<Vec<RunEvent>, Said> {
        match self.harness.when(command).result() {
            Ok(events) => Ok(events),
            Err(Failure::Rejected(rejected)) => Err(said(&refused(&rejected))),
            Err(other) => panic!("not a rejection: {other:?}"),
        }
    }

    /// A fact, delivered as the listener delivers one: a Fact body with its
    /// derived `external_id`.
    pub(crate) fn fact(&mut self, fact: impl DomainMessage, external_id: &str) -> Vec<RunEvent> {
        self.harness
            .when_book(fact_book(fact, external_id))
            .events()
    }

    pub(crate) fn state(&self) -> RunState {
        self.harness.state().state().clone()
    }

    fn event_count(&self) -> usize {
        self.harness.pages().len()
    }
}

/// A fact as the listener delivers one: a Fact body with its derived
/// `external_id`.
fn fact_book(fact: impl DomainMessage, external_id: &str) -> CommandBook {
    let any = Any::from_msg(&fact.to_wire()).unwrap();
    CommandBook {
        cover: Some(cover()),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(any),
                origin: Some(FactOrigin {
                    external_id: external_id.to_string(),
                    description: String::new(),
                }),
            })),

            ..CommandPage::default()
        }),
    }
}

fn enqueue() -> EnqueueRun {
    EnqueueRun {
        session: SESSION.to_string(),
        incarnation: 1,
        mode: RunMode::Interactive,
        prompt_given: true,
        engine: "claude-code".to_string(),
        drivable_engines: vec!["claude-code".to_string(), "opencode".to_string()],
        credential: not_asked(),
        fatal_findings: 0,
        fatal: Vec::new(),
        launcher_lock: launcher_lock(),
        untrusted_settings: Vec::new(),
        grant: None,
    }
}

/// What a child was given (step 9): two servers and its claude document.
fn grant() -> Grant {
    Grant {
        mcp_servers: vec!["ctxloom".to_string(), "fix-tools".to_string()],
        permissions: r#"{"defaultMode":"acceptEdits"}"#.to_string(),
    }
}

/// The run's lock file, which its launcher holds (rulings 86, 92).
fn launcher_lock() -> String {
    "/home/u/.ctxloom/runs/run-1.lock".to_string()
}

/// No credential to judge: a session's, or an engine's that asks a child for
/// none.
fn not_asked() -> CredentialEvidence {
    CredentialEvidence {
        file_only: false,
        builtin_variables: Vec::new(),
        accepted_variables: Vec::new(),
        accepted_set: false,
    }
}

/// A child's credential: the variables it would be given one by, and whether
/// one of them is set.
fn accepting(set: bool) -> CredentialEvidence {
    CredentialEvidence {
        accepted_variables: vec![
            "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
            "ANTHROPIC_API_KEY".to_string(),
        ],
        accepted_set: set,
        ..not_asked()
    }
}

/// A child of an engine whose source is a file alone.
fn file_only() -> CredentialEvidence {
    CredentialEvidence {
        file_only: true,
        builtin_variables: vec![
            "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
            "ANTHROPIC_API_KEY".to_string(),
        ],
        ..accepting(true)
    }
}

/// A one-shot's enqueue, its checks passing.
fn one_shot() -> EnqueueRun {
    EnqueueRun {
        mode: RunMode::OneShot,
        credential: accepting(true),
        ..enqueue()
    }
}

fn queue_turn() -> QueueTurn {
    QueueTurn {
        prompt_ref: "prompt:1".to_string(),
        resume: false,
        message_ids: Vec::new(),
        urgency: crate::TurnUrgency::Wake,
    }
}

fn exited(code: i32) -> RecordRunExited {
    RecordRunExited { code, signal: 0 }
}

// NEW-DESIGN (plan 3.3), owner ruling s4-Q3: the event holds the session,
// its incarnation and the mode, and no credential (the hook bearer's digest
// went with the per-run listener).
#[test]
fn enqueue_records_the_session_incarnation_and_mode() {
    let mut run = Run::new();
    let events = run.command(enqueue()).unwrap();
    let [RunEvent::RunEnqueued(enqueued)] = events.as_slice() else {
        panic!("one RunEnqueued, got {events:?}");
    };
    assert_eq!(enqueued.session, SESSION);
    assert_eq!(enqueued.incarnation, 1);
    assert_eq!(enqueued.mode, RunMode::Interactive);
    let state = run.state();
    assert_eq!(
        (state.session.as_str(), state.incarnation, state.mode),
        (SESSION, 1, Some(RunMode::Interactive))
    );
    assert_eq!(state.ended, None);
}

// Rulings 86, 92: the run journals its own lock, which its launcher holds
// until the runner is started, so a later process can tell a launcher that
// died (an orphan) from one still launching.
#[test]
fn enqueue_journals_the_lock_its_launcher_holds() {
    let mut run = Run::new();
    let events = run.command(enqueue()).unwrap();
    let [RunEvent::RunEnqueued(enqueued)] = events.as_slice() else {
        panic!("one RunEnqueued, got {events:?}");
    };
    assert_eq!(enqueued.launcher_lock, launcher_lock());
}

// Step 9 (owner ruling 2026-11-17): the grant record is journaled. What the
// command says a run was given is its RunEnqueued's, and its state keeps it,
// so a later edit of its agent leaves this run's record as it was.
#[test]
fn enqueue_journals_what_the_run_was_given() {
    let mut run = Run::new();
    let events = run
        .command(EnqueueRun {
            grant: Some(grant()),
            ..enqueue()
        })
        .unwrap();
    let [RunEvent::RunEnqueued(enqueued)] = events.as_slice() else {
        panic!("one RunEnqueued, got {events:?}");
    };
    assert_eq!(enqueued.grant, Some(grant()));
    assert_eq!(run.state().grant, Some(grant()));
}

// NEW-DESIGN (plan 3.3: a run is enqueued once).
#[test]
fn enqueue_on_a_stream_with_history_is_refused() {
    let mut run = Run::enqueued();
    let (code, _) = run.command(enqueue()).unwrap_err();
    assert_eq!(code, "runs-already-enqueued");
    assert_eq!(run.event_count(), 1);
}

// NEW-DESIGN (plan 3.3: root = UUIDv5(session, incarnation)): an enqueue
// whose session and incarnation derive another root is refused, so a stream
// can never hold a run that is not its own.
#[test]
fn enqueue_on_a_stream_that_is_not_the_runs_own_root_is_refused() {
    let mut run = Run::new();
    let wrong = EnqueueRun {
        incarnation: 2,
        ..enqueue()
    };
    let (code, _) = run.command(wrong).unwrap_err();
    assert_eq!(code, "runs-not-its-stream");
}

// NEW-DESIGN (plan 3.3 / 4.4: "a run with a turn in flight refuses
// QueueTurn"). Go's turn gate; the Go test is not traced, so it needs review.
#[test]
fn a_turn_is_refused_while_another_is_in_flight() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    let (code, args) = run.command(queue_turn()).unwrap_err();
    assert_eq!(code, "runs-turn-in-flight");
    // The turn is the sequence of its TurnQueued event (plan 4.3).
    assert_eq!(args["turn"], "1");
}

// NEW-DESIGN (plan 4.3: `<turn>` is the sequence of the TurnQueued event in
// the run's stream: a per-stream position, the cause).
#[test]
fn the_turn_in_flight_is_the_sequence_of_its_turn_queued_event() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    assert_eq!(run.state().turn_in_flight, Some(1));
}

// NEW-DESIGN (D5 Q7, settled by the owner: one run per incarnation, not per
// turn; plan 3.3 over Go's `sessions.MaxRunEnds` comment "a run per turn"):
// two turns of one runner are one RunEnqueued and one RunEnded.
#[test]
fn two_turns_of_one_runner_are_one_run_enqueued_and_one_run_ended() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    run.fact(
        RecordTurnStarted {
            turn: 1,
            message_ids: Vec::new(),
        },
        "turn:r:1:started",
    );
    run.fact(
        RecordTurnCompleted {
            turn: 1,
            native_key: "k".to_string(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: Vec::new(),
        },
        "turn:r:1:completed",
    );
    run.command(queue_turn()).unwrap();
    run.fact(
        RecordTurnStarted {
            turn: 3,
            message_ids: Vec::new(),
        },
        "turn:r:3:started",
    );
    run.fact(
        RecordTurnCompleted {
            turn: 3,
            native_key: "k".to_string(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: Vec::new(),
        },
        "turn:r:3:completed",
    );
    run.fact(exited(0), "exit:r");
    let all: Vec<RunEvent> = run
        .harness
        .pages()
        .iter()
        .map(|page| RunEvent::from_any(event_any(page)).unwrap())
        .collect();
    let enqueued = all
        .iter()
        .filter(|e| matches!(e, RunEvent::RunEnqueued(_)))
        .count();
    let ended = all
        .iter()
        .filter(|e| matches!(e, RunEvent::RunEnded(_)))
        .count();
    assert_eq!((enqueued, ended), (1, 1));
}

// NEW-DESIGN (plan 3.3: RecordTurnCompleted, then RunIdle): a completed turn frees
// the gate, so the next one queues.
#[test]
fn a_completed_turn_frees_the_gate() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    run.fact(
        RecordTurnCompleted {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: Vec::new(),
        },
        "turn:r:1:completed",
    );
    assert_eq!(run.state().turn_in_flight, None);
    run.command(queue_turn()).unwrap();
}

// NEW-DESIGN (plan 3.3: RecordTurnFailed records the class; the policy run->hold
// reads it).
#[test]
fn a_failed_turn_records_its_class_and_frees_the_gate() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    let events = run.fact(
        RecordTurnFailed {
            turn: 1,
            class: TurnFailureClass::RateLimited,
            detail: "429".to_string(),
            message_ids: Vec::new(),
        },
        "turn:r:1:failed",
    );
    assert_eq!(events.len(), 1);
    assert_eq!(run.state().turn_in_flight, None);
}

// PORTED-FROM-GO: internal/core/coord/endfence_test.go
// TestEndFence_MailForAnEndedRunWaitsForTheResumedRun. After the run's end
// there is no courier, and the run refuses a turn: the end fence (plan 3.4).
#[test]
fn a_turn_queued_after_the_runs_end_is_refused() {
    let mut run = Run::enqueued();
    run.fact(exited(0), "exit:r");
    let (code, _) = run.command(queue_turn()).unwrap_err();
    assert_eq!(code, "runs-ended");
}

// NEW-DESIGN (plan 3.3: "RunEnded, exactly once: a second terminal input
// records nothing").
#[test]
fn the_first_terminal_input_ends_the_run_and_a_second_records_nothing() {
    let mut run = Run::enqueued();
    let first = run.fact(exited(0), "exit:r");
    assert!(matches!(
        first.as_slice(),
        [RunEvent::RunEnded(RunEnded {
            cause: EndCause::RunnerExit,
            ..
        })]
    ));
    let before = run.event_count();
    let second = run.fact(
        RecordLaunchFailed {
            deterministic: true,
            detail: String::new(),
        },
        "launch:r:1",
    );
    assert!(second.is_empty());
    assert_eq!(run.event_count(), before);
}

// Rulings 95, 100: an orphan's end says what it lost. A STARTED run whose
// owner is gone ends for runner loss; a run still owed a runner whose
// launcher died ends orphaned, never as a launch failure. Each is the run's
// one end (a second terminal input records nothing), it revokes the
// credentials, and a redelivery is a DUPLICATE.
#[test]
fn runner_lost_ends_a_started_run_for_runner_loss_once() {
    let mut run = Run::enqueued();
    run.fact(
        RecordRunnerStarted {
            runtime: "fake".to_string(),
            workspace: "/w".to_string(),
            pid: 7,
        },
        "started:r",
    );
    let ended = run.fact(RecordRunnerLost {}, "runner-lost:r");
    assert_eq!(
        ended,
        [RunEvent::RunEnded(RunEnded {
            cause: EndCause::RunnerLoss,
            exit_code: None,
            detail: String::new(),
        })]
    );
    assert_eq!(run.state().ended, Some(EndCause::RunnerLoss));
    let handled = run
        .harness
        .when_book(fact_book(RecordRunnerLost {}, "runner-lost:r"))
        .handled()
        .unwrap();
    assert!(matches!(handled, Handled::Duplicate(..)));
    assert!(run.fact(exited(0), "exit:r").is_empty(), "ended once");
}

#[test]
fn launch_orphaned_ends_a_waiting_run_orphaned_and_claims_no_launch_failure() {
    let mut run = Run::enqueued();
    let ended = run.fact(RecordLaunchOrphaned {}, "launch-orphaned:r");
    assert_eq!(
        ended,
        [RunEvent::RunEnded(RunEnded {
            cause: EndCause::LaunchOrphaned,
            exit_code: None,
            detail: String::new(),
        })]
    );
    assert_eq!(run.state().ended, Some(EndCause::LaunchOrphaned));
    let handled = run
        .harness
        .when_book(fact_book(RecordLaunchOrphaned {}, "launch-orphaned:r"))
        .handled()
        .unwrap();
    assert!(matches!(handled, Handled::Duplicate(..)));
}

#[test]
fn an_orphan_fact_on_an_ended_or_unenqueued_run_records_nothing() {
    let mut ended = Run::enqueued();
    ended.fact(exited(0), "exit:r");
    assert!(ended.fact(RecordRunnerLost {}, "runner-lost:r").is_empty());
    assert!(ended
        .fact(RecordLaunchOrphaned {}, "launch-orphaned:r")
        .is_empty());
    let mut none = Run::new();
    assert!(none.fact(RecordRunnerLost {}, "runner-lost:r").is_empty());
    assert!(none
        .fact(RecordLaunchOrphaned {}, "launch-orphaned:r")
        .is_empty());
}

// NEW-DESIGN (plan 4.3: a re-sent fact with the same external_id is
// DUPLICATE; the kernel dedupes by `fact_command_id(external_id)`).
#[test]
fn the_same_fact_again_is_a_duplicate() {
    let mut run = Run::enqueued();
    run.fact(exited(0), "exit:r");
    let book = fact_book(exited(0), "exit:r");
    let handled = run.harness.when_book(book).handled().unwrap();
    assert!(matches!(handled, Handled::Duplicate(..)));
}

// PORTED-FROM-GO: internal/core/coord/creds_test.go
// TestCredentialRevocation_RevokesTheChildsCredential, extended per D29: the
// end revokes in the fold, whatever the cause.
#[test]
fn every_end_revokes_the_credential_whatever_the_cause() {
    let exit = |run: &mut Run| {
        run.fact(exited(1), "exit:r");
    };
    let launch_failed = |run: &mut Run| {
        run.fact(
            RecordLaunchFailed {
                deterministic: false,
                detail: "boom".to_string(),
            },
            "launch:r:1",
        );
    };
    for end in [&exit as &dyn Fn(&mut Run), &launch_failed] {
        let mut run = Run::enqueued();
        assert_eq!(run.state().ended, None);
        end(&mut run);
        assert!(run.state().ended.is_some());
    }
}

// PORTED-FROM-GO: internal/core/coord/session_owner_revoke_test.go
// TestRevokeSessionOwner_RevocationSurvivesAdoption: the end is a fact in the
// stream, so a fresh process replaying it still sees the run ended
// (`Harness::state` replays from the store, as the next process does).
#[test]
fn an_end_survives_a_restart() {
    let mut run = Run::enqueued();
    run.fact(exited(0), "exit:r");
    let replayed = run.harness.state();
    assert!(replayed.state().ended.is_some());
}

// NEW-DESIGN (plan 3.3: RequestStop from the policy session->run; the run
// ends later by RecordRunExited or StopGraceElapsed, not by the request).
#[test]
fn a_stop_request_is_recorded_once_and_does_not_end_the_run() {
    let mut run = Run::enqueued();
    let stop = RequestStop {
        reason: "user".to_string(),
    };
    assert_eq!(run.command(stop.clone()).unwrap().len(), 1);
    assert!(run.command(stop).unwrap().is_empty());
    let state = run.state();
    assert!(state.stop_requested);
    assert_eq!(state.ended, None);
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16): a summary is a CLAIM, a fact
// the model records. It is evidence and changes no transition: the run stays
// live, its turn gate and credentials untouched.
#[test]
fn a_summary_claim_is_recorded_and_transitions_nothing() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    let before = run.state();
    let events = run.fact(summary(1, 0), "report:r:1:0");
    assert!(matches!(
        events.as_slice(),
        [RunEvent::SummaryReportedRecorded(_)]
    ));
    let after = run.state();
    assert_eq!(after.summaries, 1);
    assert_eq!(
        RunState {
            summaries: 0,
            ..after
        },
        before,
        "nothing else of the run changed"
    );
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16): "even task done" is a claim.
// A final report is recorded as evidence for the parent and does NOT end the
// run, free its turn, or revoke a credential (there is no `final_reported` end
// cause: the session stays resumable and the decider accepts or re-delegates).
#[test]
fn a_final_claim_is_recorded_and_ends_nothing() {
    let mut run = Run::enqueued();
    run.command(queue_turn()).unwrap();
    let events = run.fact(final_claim(1), "final:r:1");
    assert!(matches!(
        events.as_slice(),
        [RunEvent::FinalReportedRecorded(_)]
    ));
    let state = run.state();
    assert_eq!(state.final_reported, Some(1));
    assert_eq!(state.ended, None, "the run did not end");
    assert_eq!(
        state.turn_in_flight,
        Some(1),
        "the runner's turn is the runner's"
    );
    assert!(!run
        .harness
        .pages()
        .iter()
        .any(|page| RunEvent::from_any(event_any(page))
            .is_ok_and(|e| matches!(e, RunEvent::RunEnded(_)))));
}

// NEW-DESIGN: a claim is idempotent by its external id, like any fact; a
// second summary under another ordinal is a second claim.
#[test]
fn a_claim_again_is_a_duplicate_and_a_new_ordinal_is_a_new_claim() {
    let mut run = Run::enqueued();
    run.fact(summary(1, 0), "report:r:1:0");
    let again = run
        .harness
        .when_book(fact_book(summary(1, 0), "report:r:1:0"))
        .handled()
        .unwrap();
    assert!(matches!(again, Handled::Duplicate(..)));
    run.fact(summary(1, 1), "report:r:1:1");
    assert_eq!(run.state().summaries, 2);
}

type State = (&'static str, fn() -> Run);

fn summary(turn: u64, ordinal: u32) -> RecordSummaryReported {
    RecordSummaryReported {
        turn,
        ordinal,
        scope: "progress".to_string(),
        text_ref: "text:1".to_string(),
    }
}

fn final_claim(turn: u64) -> RecordFinalReported {
    RecordFinalReported {
        turn,
        text_ref: "text:final".to_string(),
    }
}

/// One book of every fact the run takes, and its name.
fn facts() -> Vec<(CommandBook, &'static str)> {
    vec![
        (
            fact_book(
                RecordRunnerAttached {
                    runner_id: "r".to_string(),
                    protocol: "v1".to_string(),
                },
                "attach",
            ),
            "attach",
        ),
        (
            fact_book(
                RecordRunnerStarted {
                    runtime: "claude-code".to_string(),
                    workspace: "/w".to_string(),
                    pid: 7,
                },
                "runner-started",
            ),
            "runner-started",
        ),
        (
            fact_book(
                RecordTurnStarted {
                    turn: 1,
                    message_ids: Vec::new(),
                },
                "started",
            ),
            "started",
        ),
        (
            fact_book(
                RecordTurnCompleted {
                    turn: 1,
                    native_key: String::new(),
                    answer_ref: String::new(),
                    exit_code: 0,
                    message_ids: Vec::new(),
                },
                "completed",
            ),
            "completed",
        ),
        (
            fact_book(
                RecordTurnFailed {
                    turn: 1,
                    class: TurnFailureClass::EngineError,
                    detail: String::new(),
                    message_ids: Vec::new(),
                },
                "failed",
            ),
            "failed",
        ),
        (fact_book(exited(0), "exit"), "exit"),
        (
            fact_book(
                RecordLaunchFailed {
                    deterministic: false,
                    detail: String::new(),
                },
                "launch",
            ),
            "launch",
        ),
        (fact_book(summary(1, 0), "summary"), "summary"),
        (fact_book(final_claim(1), "final"), "final"),
    ]
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16, totality): a fact is what
// already happened, so no fact handler may reject, in ANY state of the run:
// never enqueued, enqueued, a turn in flight, a stop requested, ended. A fact
// the run cannot use records nothing; it is never an error. The `match` stops
// compiling when a fact is added without being listed here.
#[test]
fn every_fact_applies_in_every_state() {
    // The domain's facts are not an enum the compiler can match on; the count
    // is the guard: a new fact in runs.proto fails here until it is added above.
    assert_eq!(facts().len(), 9, "one case per fact of the runs proto");
    let states: [State; 5] = [
        ("never enqueued", Run::new),
        ("enqueued", Run::enqueued),
        ("turn in flight", || {
            let mut run = Run::enqueued();
            run.command(queue_turn()).unwrap();
            run
        }),
        ("stop requested", || {
            let mut run = Run::enqueued();
            run.command(RequestStop {
                reason: "x".to_string(),
            })
            .unwrap();
            run
        }),
        ("ended", || {
            let mut run = Run::enqueued();
            run.fact(exited(0), "exit:first");
            run
        }),
    ];
    for (state, make) in states {
        for (book, name) in facts() {
            let mut run = make();
            let handled = run.harness.when_book(book).handled();
            assert!(
                handled.is_ok(),
                "{name} in state {state}: {:?}",
                handled.err()
            );
        }
    }
}

// NEW-DESIGN: a runner attaches once per id; the attach is a fact.
#[test]
fn a_runner_attaching_is_recorded_on_the_run() {
    let mut run = Run::enqueued();
    let events = run.fact(
        RecordRunnerAttached {
            runner_id: "r1".to_string(),
            protocol: "v1".to_string(),
        },
        "attach:r:r1",
    );
    assert_eq!(events.len(), 1);
    assert!(run.state().runner_attached);
}

// Owner ruling 2026-11-08: the launcher's start of the runner is a fact, and
// the fold keeps what it said.
#[test]
fn a_runner_starting_is_recorded_on_the_run_and_only_while_it_is_live() {
    let started = || RecordRunnerStarted {
        runtime: "claude-code".to_string(),
        workspace: "/w".to_string(),
        pid: 7,
    };
    let mut run = Run::enqueued();
    assert!(run.state().runner_started.is_none());
    let events = run.fact(started(), "started:r");
    assert_eq!(events.len(), 1);
    assert_eq!(
        run.state().runner_started,
        Some(crate::RunnerStartedRecorded {
            runtime: "claude-code".to_string(),
            workspace: "/w".to_string(),
            pid: 7,
        })
    );
    // Never enqueued: nothing to record it on.
    let mut none = Run::new();
    assert!(none.fact(started(), "started:r").is_empty());
}

// NEW-DESIGN: a command to a stream holding no run is refused, not decided on
// an empty state.
#[test]
fn commands_to_a_stream_with_no_run_are_refused() {
    let mut run = Run::new();
    let (code, _) = run.command(queue_turn()).unwrap_err();
    assert_eq!(code, "runs-no-run");
    let stop = RequestStop {
        reason: "x".to_string(),
    };
    let (code, _) = run.command(stop).unwrap_err();
    assert_eq!(code, "runs-no-run");
}

/// The domain event a page carries.
fn event_any(page: &angzarr_embedded::v1::EventPage) -> &angzarr_embedded::Any {
    match &page.body {
        Some(angzarr_embedded::v1::event_page::Body::Event(event)) => event,
        other => panic!("not a domain event: {other:?}"),
    }
}

// ---- the pre-launch checks, decided by the enqueue (owner rulings 73, 73c-f) ----

const BOTH: &str = "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY";

fn no_credential() -> RunRejection {
    RunRejection::NoCredential {
        engine: "claude-code".to_string(),
        variables: BOTH.to_string(),
    }
}

fn unknown(name: &str) -> RunRejection {
    RunRejection::UnknownEngine {
        name: name.to_string(),
        known: "claude-code, opencode".to_string(),
    }
}

// Ruling 73c: a one-shot with no prompt is the typed NoPrompt, and journals
// nothing.
#[test]
fn a_one_shot_with_no_prompt_is_refused_and_journals_nothing() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        ..one_shot()
    };
    assert_eq!(run.rejection(cmd), RunRejection::NoPrompt);
    assert_eq!(run.event_count(), 0);
}

// A session's prompt is optional: an interactive run without one enqueues.
#[test]
fn an_interactive_run_needs_no_prompt() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        ..enqueue()
    };
    assert!(run.command(cmd).is_ok());
}

// Ruling 73c: an engine this build does not drive is the typed UnknownEngine,
// naming it and the ones it drives.
#[test]
fn an_engine_that_is_not_driven_is_refused_naming_it_and_the_ones_that_are() {
    for mode in [RunMode::OneShot, RunMode::Interactive] {
        let mut run = Run::new();
        let cmd = EnqueueRun {
            mode,
            engine: "bogus-engine".to_string(),
            credential: not_asked(),
            ..enqueue()
        };
        assert_eq!(run.rejection(cmd), unknown("bogus-engine"), "{mode:?}");
        assert_eq!(run.event_count(), 0);
    }
}

// Ruling 53, now decided here: a child of a file-only source is refused naming
// the built-in variables, even with one of them set (the source names none).
#[test]
fn a_child_of_a_file_only_source_is_refused_naming_the_variables_to_export() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        credential: file_only(),
        ..one_shot()
    };
    assert_eq!(
        run.rejection(cmd),
        RunRejection::FileOnlyCredential {
            engine: "claude-code".to_string(),
            variables: BOTH.to_string(),
        }
    );
}

// A child with none of its variables set is refused; with one set, or on an
// engine that asks a child for none, it enqueues.
#[test]
fn a_child_needs_one_of_its_variables_set_unless_its_engine_asks_for_none() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        credential: accepting(false),
        ..one_shot()
    };
    assert_eq!(run.rejection(cmd), no_credential());
    for credential in [accepting(true), not_asked()] {
        let mut run = Run::new();
        let cmd = EnqueueRun {
            credential,
            ..one_shot()
        };
        assert!(run.command(cmd).is_ok());
    }
}

// Rulings 73, 73b: two failing checks are ONE refusal, LaunchRefusals, holding
// both in check order; its count is how many failed.
#[test]
fn two_failing_checks_are_one_launch_refused_holding_both_in_check_order() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        credential: accepting(false),
        ..one_shot()
    };
    let rejection = run.rejection(cmd);
    assert_eq!(
        rejection,
        RunRejection::LaunchRefused {
            refusals: vec![RunRejection::NoPrompt, no_credential()],
            fatal_findings: 0,
            findings: Vec::new(),
        }
    );
    let (code, args) = said(&rejection);
    assert_eq!(code, "runs-launch-refused");
    assert_eq!(args["count"], "2");
    assert_eq!(run.event_count(), 0);
}

// Step 8 U2 (ruling 142, D3): the untrusted project-only settings this
// launch reads are the typed UntrustedProjectSettings, naming each path,
// and nothing is journaled.
#[test]
fn untrusted_project_settings_refuse_the_launch_naming_each_path() {
    for mode in [RunMode::OneShot, RunMode::Interactive, RunMode::Structured] {
        let mut run = Run::new();
        let cmd = EnqueueRun {
            mode,
            untrusted_settings: vec![
                "agents.default.session_home".to_string(),
                "engines.claude-code.command".to_string(),
            ],
            ..one_shot()
        };
        let rejection = run.rejection(cmd);
        assert_eq!(
            rejection,
            RunRejection::UntrustedProjectSettings {
                paths: "agents.default.session_home, engines.claude-code.command".to_string(),
            },
            "{mode:?}"
        );
        let (code, args) = said(&rejection);
        assert_eq!(code, "runs-untrusted-project-settings");
        assert_eq!(
            args["paths"],
            "agents.default.session_home, engines.claude-code.command"
        );
        assert_eq!(run.event_count(), 0);
    }
}

// Owner Q9: the trust check is the LAST, so beside another refusal it is
// one LaunchRefusals holding both, the untrusted settings after the rest.
#[test]
fn untrusted_settings_are_the_last_refusal_of_a_launch_refused() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        credential: accepting(false),
        untrusted_settings: vec!["engines.claude-code.command".to_string()],
        ..one_shot()
    };
    assert_eq!(
        run.rejection(cmd),
        RunRejection::LaunchRefused {
            refusals: vec![
                RunRejection::NoPrompt,
                no_credential(),
                RunRejection::UntrustedProjectSettings {
                    paths: "engines.claude-code.command".to_string(),
                },
            ],
            fatal_findings: 0,
            findings: Vec::new(),
        }
    );
}

// Ruling 73d: an engine nobody drives has no credential policy, so its
// credential evidence is not judged: the most a run can fail is the plan, the
// prompt and the engine.
#[test]
fn an_undriven_engines_credential_is_not_judged() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        engine: "bogus-engine".to_string(),
        credential: accepting(false),
        fatal_findings: 1,
        ..one_shot()
    };
    assert_eq!(
        run.rejection(cmd),
        RunRejection::LaunchRefused {
            refusals: vec![RunRejection::NoPrompt, unknown("bogus-engine")],
            fatal_findings: 1,
            findings: Vec::new(),
        }
    );
}

// Rulings 73a, 73c, 73h: the plan's fatal findings (a count) are not a refusal of the list,
// but they refuse the launch: alone, as an empty aggregate the application
// says as the plan's own error; beside one refusal, as the aggregate of it,
// counting the findings as one failed check.
#[test]
fn fatal_findings_refuse_the_launch_beside_or_without_the_typed_refusals() {
    let mut run = Run::new();
    let alone = EnqueueRun {
        fatal_findings: 1,
        ..one_shot()
    };
    assert_eq!(
        run.rejection(alone),
        RunRejection::LaunchRefused {
            refusals: Vec::new(),
            fatal_findings: 1,
            findings: Vec::new(),
        }
    );
    assert_eq!(run.event_count(), 0);
    let mut run = Run::new();
    let beside = EnqueueRun {
        fatal_findings: 3,
        credential: accepting(false),
        ..one_shot()
    };
    let rejection = run.rejection(beside);
    assert_eq!(
        rejection,
        RunRejection::LaunchRefused {
            refusals: vec![no_credential()],
            fatal_findings: 3,
            findings: Vec::new(),
        }
    );
    // Ruling 73h: the findings are counted, not said; as checks they are one.
    assert_eq!(said(&rejection).1["count"], "2");
}

// The evidence is the command's alone: an enqueue that passes its checks
// journals the run as before, and the event has no field of the evidence (its
// wire form is the six fields it always had).
#[test]
fn the_evidence_never_reaches_the_event() {
    let mut run = Run::new();
    let events = run.command(one_shot()).unwrap();
    let [RunEvent::RunEnqueued(enqueued)] = events.as_slice() else {
        panic!("one RunEnqueued, got {events:?}");
    };
    let wire = runs_proto::v1::RunEnqueued::from(enqueued.clone());
    let bytes = prost::Message::encode_to_vec(&wire);
    let back: runs_proto::v1::EnqueueRun = prost::Message::decode(bytes.as_slice()).unwrap();
    assert!(!back.prompt_given && back.engine.is_empty() && back.drivable_engines.is_empty());
    assert!(back.credential.is_none() && back.fatal_findings == 0);
}

// ---- no posture of ctxloom's own (owner ruling 134) ----

// The enqueue judges no posture: whatever an agent's engine permissions say,
// they are the engine's (rulings 133-139), and the command carries no posture
// evidence at all (field 13 is reserved).
#[test]
fn the_enqueue_judges_no_posture() {
    for mode in [RunMode::OneShot, RunMode::Interactive] {
        let mut run = Run::new();
        let cmd = EnqueueRun { mode, ..one_shot() };
        assert!(run.command(cmd).is_ok(), "{mode:?}");
    }
    let mut run = Run::new();
    let cmd = EnqueueRun {
        prompt_given: false,
        credential: accepting(false),
        ..one_shot()
    };
    let rejection = run.rejection(cmd);
    assert_eq!(
        rejection,
        RunRejection::LaunchRefused {
            refusals: vec![RunRejection::NoPrompt, no_credential()],
            fatal_findings: 0,
            findings: Vec::new(),
        }
    );
    assert_eq!(said(&rejection).1["count"], "2");
}

// ---- the pre-launch checks judged before anything is recorded (S10, U3)

/// NEW-DESIGN (daemon slice 4 §3.4): a launch every check passes is judged
/// so, and nothing is recorded.
#[test]
fn a_launch_every_check_passes_has_no_pre_launch_refusal() {
    assert!(crate::pre_launch_refusal(&one_shot()).is_none());
}

/// NEW-DESIGN (§3.4): a child with no credential set is refused before its
/// hand-off is written, with the typed refusal the enqueue would answer and
/// the status its message declares.
#[test]
fn a_child_with_no_credential_is_refused_as_its_enqueue_would_be() {
    let child = EnqueueRun {
        mode: RunMode::Structured,
        credential: accepting(false),
        ..enqueue()
    };
    let (refusal, status) = crate::pre_launch_refusal(&child).expect("refused");
    assert_eq!(
        RunRejection::try_from(&refusal),
        Ok(RunRejection::NoCredential {
            engine: "claude-code".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY".to_string(),
        })
    );
    assert_eq!(
        status,
        <crate::NoCredential as crate::Refusal>::REFUSAL_STATUS
    );
}

/// NEW-DESIGN (§3.4): a file-only source refuses a child as its enqueue
/// does.
#[test]
fn a_child_on_a_file_only_source_is_refused_as_its_enqueue_would_be() {
    let child = EnqueueRun {
        mode: RunMode::Structured,
        credential: file_only(),
        ..enqueue()
    };
    let (refusal, _) = crate::pre_launch_refusal(&child).expect("refused");
    assert!(matches!(
        RunRejection::try_from(&refusal),
        Ok(RunRejection::FileOnlyCredential { .. })
    ));
}

// Owner ruling 2026-11-21: the findings the command carries are the refusal's,
// beside their count, so a launch that has nobody to say them still tells.
#[test]
fn the_fatal_findings_a_command_carries_are_the_refusals() {
    let mut run = Run::new();
    let cmd = EnqueueRun {
        fatal_findings: 2,
        fatal: vec![
            crate::FatalFinding {
                text: "failed to load bundle \"hub/demo\"".to_string(),
                remedy: "run `ctxloom deps pull`".to_string(),
            },
            crate::FatalFinding {
                text: "second".to_string(),
                remedy: String::new(),
            },
        ],
        ..one_shot()
    };
    let RunRejection::LaunchRefused { findings, .. } = run.rejection(cmd) else {
        panic!("not an aggregate");
    };
    assert_eq!(
        findings,
        [
            crate::LaunchFinding {
                text: "failed to load bundle \"hub/demo\"".to_string(),
                remedy: "run `ctxloom deps pull`".to_string(),
            },
            crate::LaunchFinding {
                text: "second".to_string(),
                remedy: String::new(),
            },
        ]
    );
}

// The pre-check of a child's hand-off asks the same checks of the same
// evidence, so its typed refusal holds the findings too.
#[test]
fn the_pre_launch_refusal_holds_the_findings() {
    let cmd = EnqueueRun {
        fatal_findings: 1,
        fatal: vec![crate::FatalFinding {
            text: "t".to_string(),
            remedy: "r".to_string(),
        }],
        ..one_shot()
    };
    let (typed, _) = crate::pre_launch_refusal(&cmd).expect("refused");
    let RunRejection::LaunchRefused { findings, .. } =
        RunRejection::try_from(&typed).expect("ours")
    else {
        panic!("not an aggregate");
    };
    assert_eq!(findings.len(), 1);
    assert_eq!(findings[0].text, "t");
}
