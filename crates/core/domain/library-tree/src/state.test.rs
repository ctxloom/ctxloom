//! The fold: each event's paths tied to its own sequence, and the tokens a
//! path and a bundle are answered.

use angzarr_embedded::v1::event_meta::Causation;
use angzarr_embedded::v1::{CommandCause, EventMeta, FactCause, FactOrigin, Uuid};

use super::*;
use crate::{Change, DirtyPath, External, ReplaceFile, Step};

const TREE: &str = "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library";
const FILE: &str = "content/bundles/v2/demo/fragments/example.md";
const YAML: &str = "content/bundles/v2/demo/bundle.yaml";

fn change(path: &str, status: ChangeStatus, to: &str) -> Change {
    Change {
        path: path.to_string(),
        status,
        from_oid: String::new(),
        to_oid: to.to_string(),
        from_path: String::new(),
    }
}

fn observed(baseline: bool, changes: Vec<Change>, dirty: &[(&str, &str)]) -> LibraryEvent {
    LibraryEvent::TreeObserved(TreeObserved {
        name: TREE.to_string(),
        external: Some(External {
            head_before: "h-0".to_string(),
            head_after: "h-1".to_string(),
        }),
        intended: None,
        changes,
        dirty_after: dirty
            .iter()
            .map(|(path, oid)| DirtyPath {
                path: path.to_string(),
                oid: oid.to_string(),
            })
            .collect(),
        tree_after: "t-1".to_string(),
        baseline,
        ..Default::default()
    })
}

fn intent(request: &str, steps: usize) -> LibraryEvent {
    let step = Step {
        replace: Some(ReplaceFile {
            foreign: false,
            name: TREE.to_string(),
            verb: "fragment edit".to_string(),
            path: FILE.to_string(),
            seen_sequence: 0,
            new_oid: "n".to_string(),
            evidence: crate::Evidence {
                tree_reads: true,
                plan_rejection: None,
                builtin: false,
            },
        }),
        create: None,
        trash: None,
        r#move: None,
        purge: None,
    };
    LibraryEvent::ChangeIntended(ChangeIntended {
        intent: crate::Intent {
            foreign: Vec::new(),
            request_id: request.to_string(),
            verb: "fragment edit".to_string(),
            steps: vec![step; steps],
            base_cause: String::new(),
            done: 0,
        },
    })
}

fn applied(request: &str, step: u32, observed: &[(&str, &str)]) -> LibraryEvent {
    LibraryEvent::StepApplied(StepApplied {
        request_id: request.to_string(),
        step,
        observed: observed
            .iter()
            .map(|(path, oid)| DirtyPath {
                path: path.to_string(),
                oid: oid.to_string(),
            })
            .collect(),
    })
}

fn resource(sequence: u64, oid: &str) -> Resource {
    Resource {
        sequence,
        oid: oid.to_string(),
    }
}

#[test]
fn a_baseline_names_the_tree_sets_its_sequence_and_ties_each_change_to_it() {
    let mut library = Library::default();
    library.apply(
        &observed(
            true,
            vec![change(YAML, ChangeStatus::Added, "y1")],
            &[(YAML, "y1")],
        ),
        4,
        "obs:1".to_string(),
    );
    assert_eq!(library.name, TREE);
    assert_eq!(library.kind, Some(TreeKind::Checkout));
    assert_eq!(library.head, "h-1");
    assert_eq!(library.tree, "t-1");
    assert_eq!(library.last_cause, "obs:1");
    assert!(library.baselined);
    assert_eq!(library.baseline_sequence, 4);
    assert_eq!(library.resources.get(YAML), Some(&resource(4, "y1")));
    assert_eq!(library.dirty.get(YAML).map(String::as_str), Some("y1"));
}

#[test]
fn a_later_observation_moves_only_the_paths_it_names() {
    let mut library = Library::default();
    library.apply(&observed(true, vec![], &[]), 0, "b".to_string());
    library.apply(
        &observed(
            false,
            vec![change(FILE, ChangeStatus::Modified, "f2")],
            &[(FILE, "f2")],
        ),
        3,
        "o".to_string(),
    );
    assert_eq!(library.baseline_sequence, 0);
    assert_eq!(library.token(FILE), 3);
    assert_eq!(
        library.token(YAML),
        0,
        "a path no event named is the baseline's"
    );
    assert_eq!(library.token("content/bundles/v2/demo"), 3);
    assert_eq!(library.token("content/bundles/v2/other"), 0);
}

#[test]
fn a_rebaseline_forgets_every_path_and_moves_every_token() {
    let mut library = Library::default();
    library.apply(&observed(true, vec![], &[]), 0, "b".to_string());
    library.apply(
        &observed(false, vec![change(FILE, ChangeStatus::Modified, "f2")], &[]),
        1,
        "o".to_string(),
    );
    library.apply(&observed(true, vec![], &[]), 7, "b2".to_string());
    assert!(library.resources.is_empty());
    assert_eq!(library.token(FILE), 7);
    assert_eq!(library.token(YAML), 7);
}

#[test]
fn a_rename_empties_where_it_was_and_names_where_it_is() {
    let mut library = Library::default();
    library.apply(&observed(true, vec![], &[]), 0, "b".to_string());
    let renamed = Change {
        path: "content/bundles/v2/demo/fragments/renamed.md".to_string(),
        status: ChangeStatus::Renamed,
        from_oid: "f1".to_string(),
        to_oid: "f1".to_string(),
        from_path: FILE.to_string(),
    };
    library.apply(&observed(false, vec![renamed], &[]), 2, "o".to_string());
    assert_eq!(library.resources.get(FILE), Some(&resource(2, "")));
    assert_eq!(
        library
            .resources
            .get("content/bundles/v2/demo/fragments/renamed.md"),
        Some(&resource(2, "f1"))
    );
    assert!(library.known_absent(FILE));
    assert!(library.known_present("content/bundles/v2/demo/fragments/renamed.md"));
}

#[test]
fn a_change_of_another_status_than_a_rename_empties_no_from_path() {
    let mut library = Library::default();
    let mut modified = change(FILE, ChangeStatus::Modified, "f2");
    modified.from_path = YAML.to_string();
    library.apply(&observed(true, vec![modified], &[]), 0, "b".to_string());
    assert_eq!(library.resources.get(YAML), None);
}

#[test]
fn the_dirty_set_is_the_observations_after_whole() {
    let mut library = Library::default();
    library.apply(
        &observed(true, vec![], &[(FILE, "f1"), (YAML, "y1")]),
        0,
        "b".to_string(),
    );
    library.apply(
        &observed(false, vec![], &[(YAML, "y2")]),
        1,
        "o".to_string(),
    );
    assert_eq!(library.dirty.len(), 1);
    assert_eq!(library.dirty.get(YAML).map(String::as_str), Some("y2"));
}

#[test]
fn the_name_and_kind_are_the_first_observations() {
    let mut library = Library::default();
    library.apply(&observed(true, vec![], &[]), 0, "b".to_string());
    let LibraryEvent::TreeObserved(mut other) = observed(false, vec![], &[]) else {
        unreachable!()
    };
    other.name = "users/me/library".to_string();
    library.apply(&LibraryEvent::TreeObserved(other), 1, "o".to_string());
    assert_eq!(library.name, TREE);
    assert_eq!(library.kind, Some(TreeKind::Checkout));
}

#[test]
fn an_observation_of_an_intent_keeps_the_head_it_had() {
    let mut library = Library::default();
    library.apply(&observed(true, vec![], &[]), 0, "b".to_string());
    let LibraryEvent::TreeObserved(mut ours) = observed(false, vec![], &[]) else {
        unreachable!()
    };
    ours.external = None;
    ours.intended = Some("r-1".to_string());
    library.head = "h-kept".to_string();
    library.apply(&LibraryEvent::TreeObserved(ours), 1, "o".to_string());
    assert_eq!(library.head, "h-kept");
}

#[test]
fn an_intent_is_pending_until_its_last_step_and_its_steps_tie_their_paths() {
    let mut library = Library::default();
    library.apply(&intent("r-1", 2), 1, "r-1".to_string());
    assert!(library.pending.contains_key("r-1"));
    assert_eq!(library.token(FILE), 0, "an intent moves no token");
    library.apply(&applied("r-1", 0, &[(FILE, "n")]), 2, "s0".to_string());
    assert_eq!(library.pending.get("r-1").map(|i| i.done), Some(1));
    assert_eq!(library.resources.get(FILE), Some(&resource(2, "n")));
    library.apply(&applied("r-1", 1, &[(YAML, "")]), 3, "s1".to_string());
    assert!(!library.pending.contains_key("r-1"));
    assert_eq!(library.resources.get(YAML), Some(&resource(3, "")));
}

#[test]
fn a_step_applied_again_never_moves_an_intent_back() {
    let mut library = Library::default();
    library.apply(&intent("r-1", 3), 1, "r-1".to_string());
    library.apply(&applied("r-1", 1, &[]), 2, "s1".to_string());
    library.apply(&applied("r-1", 0, &[]), 3, "s0".to_string());
    assert_eq!(library.pending.get("r-1").map(|i| i.done), Some(2));
}

#[test]
fn a_step_of_no_pending_intent_still_ties_what_it_observed() {
    let mut library = Library::default();
    library.apply(&applied("r-9", 0, &[(FILE, "n")]), 5, "s".to_string());
    assert_eq!(library.resources.get(FILE), Some(&resource(5, "n")));
    assert!(library.pending.is_empty());
}

#[test]
fn a_failed_or_superseded_intent_is_no_longer_pending() {
    let mut library = Library::default();
    library.apply(&intent("r-1", 1), 1, "r-1".to_string());
    library.apply(&intent("r-2", 1), 2, "r-2".to_string());
    library.apply(
        &LibraryEvent::ChangeFailed(ChangeFailed {
            request_id: "r-1".to_string(),
            step: 0,
            ext_err_txt: "denied".to_string(),
        }),
        3,
        "f".to_string(),
    );
    library.apply(
        &LibraryEvent::IntentSuperseded(IntentSuperseded {
            request_id: "r-2".to_string(),
            by_cause: "obs".to_string(),
        }),
        4,
        "sup".to_string(),
    );
    assert!(library.pending.is_empty());
    assert_eq!(library.last_cause, "sup");
}

#[test]
fn a_bundles_token_is_its_latest_files_and_a_sibling_prefix_is_not_under_it() {
    let mut library = Library {
        baseline_sequence: 2,
        ..Library::default()
    };
    library.resources.insert(YAML.to_string(), resource(5, "y"));
    library.resources.insert(
        "content/bundles/v2/demo/fragments/a.md".to_string(),
        resource(9, "a"),
    );
    library.resources.insert(
        "content/bundles/v2/demox/bundle.yaml".to_string(),
        resource(11, "x"),
    );
    assert_eq!(library.token("content/bundles/v2/demo"), 9);
    assert_eq!(library.token("content/bundles/v2/demo/"), 9);
    assert_eq!(library.token(YAML), 5);
    assert_eq!(library.token("config.yaml"), 2);
}

#[test]
fn presence_is_known_only_of_what_an_event_named() {
    let mut library = Library::default();
    library.resources.insert(YAML.to_string(), resource(1, "y"));
    library.resources.insert(FILE.to_string(), resource(1, ""));
    assert!(library.known_present("content/bundles/v2/demo"));
    assert!(library.known_present(YAML));
    assert!(!library.known_present(FILE));
    assert!(library.known_absent(FILE));
    assert!(
        !library.known_absent("content/bundles/v2/demo"),
        "a file is left"
    );
    assert!(
        !library.known_absent("config.yaml"),
        "never named: not known"
    );
    assert!(!library.known_present("config.yaml"));
}

#[test]
fn a_cause_is_a_facts_external_id_or_a_commands_id() {
    let fact = EventMeta {
        causation: Some(Causation::Fact(FactCause {
            command_id: None,
            origin: Some(FactOrigin {
                external_id: "obs:x".to_string(),
                description: String::new(),
            }),
        })),
        ..EventMeta::default()
    };
    assert_eq!(cause_of(Some(&fact)), "obs:x");
    let id = uuid::Uuid::from_u128(0x0102_0304_0506_0708_090a_0b0c_0d0e_0f10);
    let command = EventMeta {
        causation: Some(Causation::Command(CommandCause {
            command_id: Some(Uuid::from_bytes(*id.as_bytes())),
        })),
        ..EventMeta::default()
    };
    assert_eq!(cause_of(Some(&command)), id.to_string());
    assert_eq!(cause_of(Some(&EventMeta::default())), "");
    assert_eq!(cause_of(None), "");
}

#[test]
fn the_wire_state_carries_every_part_of_the_fold() {
    let mut library = Library::default();
    library.apply(
        &observed(
            true,
            vec![change(YAML, ChangeStatus::Added, "y1")],
            &[(YAML, "y1")],
        ),
        4,
        "obs:1".to_string(),
    );
    library.apply(&intent("r-1", 1), 5, "r-1".to_string());
    library.trashed.insert(
        "r-0".to_string(),
        crate::Slot {
            paths: vec!["content/bundles/v2/old".to_string()],
            trashed_at: 1_700_000_000,
            kept: true,
        },
    );
    let wire = library.wire();
    assert_eq!(wire.name, TREE);
    assert_eq!(wire.kind, 1);
    assert_eq!(wire.head, "h-1");
    assert_eq!(wire.tree, "t-1");
    assert_eq!(wire.dirty.get(YAML).map(String::as_str), Some("y1"));
    assert_eq!(wire.last_cause, "r-1");
    assert_eq!(wire.pending.get("r-1").map(|i| i.steps.len()), Some(1));
    assert_eq!(
        wire.trashed
            .get("r-0")
            .map(|t| (t.request_id.as_str(), t.paths.len())),
        Some(("r-0", 1))
    );
    assert_eq!(
        wire.resources
            .get(YAML)
            .map(|r| (r.sequence, r.oid.as_str())),
        Some((4, "y1"))
    );
    assert_eq!(wire.baseline_sequence, 4);
    assert_eq!(Library::default().wire().kind, 0);
}

// The library is git-free (owner 2026-11-18): `HomeRepositoryMade` is never
// emitted again, but a stream a 10b daemon wrote holds it, and replays: it
// folds as nothing but its cause, which the next fact's key chains on.
#[test]
fn test_a_home_repository_made_a_10b_stream_holds_folds_as_nothing_but_its_cause() {
    let mut library = Library::default();
    library.apply(
        &observed(true, vec![change(FILE, ChangeStatus::Added, "f1")], &[]),
        1,
        "obs:one".to_string(),
    );
    let mut expected = library.clone();
    library.apply(
        &LibraryEvent::HomeRepositoryMade(crate::HomeRepositoryMade {
            name: TREE.to_string(),
        }),
        2,
        "home-repository-made:x".to_string(),
    );
    expected.last_cause = "home-repository-made:x".to_string();
    assert_eq!(library, expected);
}
