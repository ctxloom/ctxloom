//! `fragment|command distill` served (step 10b): what follows from a
//! distill's run, and how a write that was not made ends it. The ask, the
//! run and the write end to end are content_distill.feature's (the mock
//! engine as the model).

use std::sync::Weak;
use std::time::Duration;

use angzarr_diagnostic::Diagnostic;
use daemon_application::Reaction;
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use library_application::{AppError, ItemKind};
use run_application::{Answer, Done, Placement, RunError};

use super::*;
use crate::refusal::ApiRefusal;
use crate::server::tests::{now, World};

const RUN: &str = "0195f0a4-7b1e-7000-8000-000000000001";

fn placed(run: &str) -> Placement {
    Placement {
        run: run.to_string(),
        session: "s".to_string(),
        name: "projects/calm-otter/sessions/harp-1".to_string(),
        harp: "harp-1".to_string(),
        engine: "mock".to_string(),
        approval_timeout: Duration::from_secs(60),
        ..Placement::default()
    }
}

// The run's outcome as `follows` takes it, whatever the size of its error.
#[allow(clippy::result_large_err)]
fn answered(text: &str) -> Result<Answer, RunError> {
    Ok(Answer {
        harp: "harp-1".to_string(),
        session: "s".to_string(),
        engine: "mock".to_string(),
        answer: text.to_string(),
    })
}

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

// An answer is written, under its run's key; no answer is a distill that
// failed, its cause the run's end; an answer from no placed run is no
// run's.
#[test]
fn test_what_follows_from_a_distill_s_run_is_its_answer_written_else_its_failure() {
    let Follows::Write { key, answer } = follows(
        "lore#fragments/rules",
        Some(&placed(RUN)),
        &answered("DISTILLED"),
    ) else {
        panic!("a write");
    };
    assert_eq!(
        (key.as_str(), answer.as_str()),
        (write_key(RUN).as_str(), "DISTILLED")
    );

    let refused = Err(RunError::Refused(runs::RunRejection::NoPrompt));
    let Follows::Failed(error) = follows("lore#fragments/rules", Some(&placed(RUN)), &refused)
    else {
        panic!("failed");
    };
    let AppError::DistillFailed { reference, cause } = &error else {
        panic!("distill failed: {error:?}");
    };
    assert_eq!(reference, "lore#fragments/rules");
    assert_eq!(cause.code(), "runs-no-prompt");
    assert_eq!(error.exit(), Exit::FAILED);

    assert!(matches!(
        follows("lore#fragments/rules", None, &answered("DISTILLED")),
        Follows::Failed(AppError::DistillFailed { .. })
    ));
}

// A write refused is the run's end in its refusal's own words, a decline;
// a write that failed is a failure.
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
    assert_eq!(ended.code(), "daemon-api-failed");
    assert_eq!(ended.exit(), Exit::FAILED);
}

fn reaction() -> DistillReaction<World> {
    DistillReaction {
        api: Weak::new(),
        asked: Asked {
            kind: ItemKind::Fragment,
            write: ItemWrite {
                name: "projects/calm-otter/bundles/lore/fragments/rules".to_string(),
                ..ItemWrite::default()
            },
            force: true,
        },
        digest: "d".to_string(),
    }
}

// The reaction ends a run that came to no answer as the distill's failure,
// keeping what the run said; it writes nothing (no API is reached).
#[test]
fn test_a_run_with_no_answer_ends_as_the_distill_s_failure() {
    let ended = now(reaction().react(
        Some(&placed(RUN)),
        Done {
            result: Err(RunError::EmptyAnswer),
            notices: vec![library_application::Notice::text("said")],
        },
    ));
    assert_eq!(ended.notices.len(), 1, "what the run said is kept");
    let error = ended.result.expect_err("failed");
    assert_eq!(error.code(), "library-application-distill-failed");
    assert_eq!(error.exit(), Exit::FAILED);
}

// An answer whose daemon is gone is not written, and says so.
#[test]
fn test_an_answer_with_no_daemon_left_to_write_it_fails() {
    let ended = now(reaction().react(
        Some(&placed(RUN)),
        Done {
            result: answered("DISTILLED"),
            notices: Vec::new(),
        },
    ));
    let error = ended.result.expect_err("not written");
    assert_eq!(error.code(), "daemon-api-failed");
    assert_eq!(error.exit(), Exit::FAILED);
}
