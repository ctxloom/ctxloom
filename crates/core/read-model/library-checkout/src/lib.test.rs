//! The checkouts: each tree folded from its stream with the domain's own
//! fold, each path's token its last event's sequence, answered as the
//! domain's state message.

use angzarr_embedded::angzarr_proto::v1::{
    event_meta::Causation, EventMeta, FactCause, FactOrigin,
};
use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage};
use angzarr_embedded::{Payload, ProjectorHandler};
use library_tree::{
    tree_root, Change, ChangeStatus, DirtyPath, External, LibraryEvent, StepApplied, TreeKind,
    TreeObserved, LIBRARY_DOMAIN,
};

use crate::{caught_up, LibraryCheckout, LibraryCheckouts};

const TREE: &str = "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library";
const FILE: &str = "content/bundles/v2/demo/fragments/example.md";

fn observed(baseline: bool, changes: &[(&str, &str)]) -> LibraryEvent {
    LibraryEvent::TreeObserved(TreeObserved {
        name: TREE.to_string(),
        external: Some(External {
            head_before: "h-0".to_string(),
            head_after: "h-1".to_string(),
        }),
        intended: None,
        changes: changes
            .iter()
            .map(|(path, oid)| Change {
                path: path.to_string(),
                status: ChangeStatus::Modified,
                from_oid: String::new(),
                to_oid: oid.to_string(),
                from_path: String::new(),
            })
            .collect(),
        dirty_after: changes
            .iter()
            .map(|(path, oid)| DirtyPath {
                path: path.to_string(),
                oid: oid.to_string(),
            })
            .collect(),
        tree_after: "t-1".to_string(),
        baseline,
    })
}

/// The tree's stream: `events` from `first` on, each caused by the fact
/// `obs:<sequence>`.
/// A book a purge's tombstone at `at` opens, then `events` after it: what a
/// stream purged and written again hands a reader.
fn after_tombstone(domain: &str, at: u64, events: Vec<LibraryEvent>) -> EventBook {
    let mut after = on(domain, at + 1, events);
    after
        .pages
        .insert(0, angzarr_embedded::angzarr_ports::tombstone::page(at));
    after.next_sequence = after.pages.last().map_or(at + 1, |p| p.sequence + 1);
    after
}

fn on(domain: &str, first: u64, events: Vec<LibraryEvent>) -> EventBook {
    let pages: Vec<EventPage> = (first..)
        .zip(events)
        .map(|(sequence, event)| EventPage {
            sequence,
            body: Some(event_page::Body::Event(event.to_any())),
            meta: Some(EventMeta {
                causation: Some(Causation::Fact(FactCause {
                    command_id: None,
                    origin: Some(FactOrigin {
                        external_id: format!("obs:{sequence}"),
                        description: String::new(),
                    }),
                })),
                ..EventMeta::default()
            }),
        })
        .collect();
    let next = pages.last().map_or(0, |p| p.sequence + 1);
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(tree_root(TREE)),
            edition: None,
        }),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

#[test]
fn a_tree_is_folded_from_its_stream_each_path_at_its_last_events_sequence() {
    let mut model = LibraryCheckout::new();
    model
        .project(&on(LIBRARY_DOMAIN, 0, vec![observed(true, &[])]))
        .unwrap();
    model
        .project(&on(
            LIBRARY_DOMAIN,
            1,
            vec![observed(false, &[(FILE, "f1")])],
        ))
        .unwrap();
    let root = tree_root(TREE);
    let tree = model.tree(&root).expect("held");
    assert_eq!(tree.name, TREE);
    assert_eq!(tree.kind, Some(TreeKind::Checkout));
    assert_eq!(tree.head, "h-1");
    assert_eq!(tree.last_cause, "obs:1");
    assert_eq!(model.token(&root, FILE), Some(1));
    assert_eq!(model.token(&root, "config.yaml"), Some(0));
    assert_eq!(model.token(&tree_root("users/me/library"), FILE), None);
}

#[test]
fn a_step_applied_moves_its_paths_tokens_as_the_aggregates_fold_does() {
    let mut model = LibraryCheckout::new();
    model
        .project(&on(
            LIBRARY_DOMAIN,
            0,
            vec![
                observed(true, &[]),
                LibraryEvent::StepApplied(StepApplied {
                    request_id: "r-1".to_string(),
                    step: 0,
                    observed: vec![DirtyPath {
                        path: FILE.to_string(),
                        oid: "n".to_string(),
                    }],
                }),
            ],
        ))
        .unwrap();
    assert_eq!(model.token(&tree_root(TREE), FILE), Some(1));
}

#[test]
fn a_tombstone_closes_the_stream_and_what_follows_starts_afresh() {
    let mut model = LibraryCheckout::new();
    model
        .project(&on(
            LIBRARY_DOMAIN,
            0,
            vec![observed(true, &[(FILE, "f1")])],
        ))
        .unwrap();
    model
        .project(&after_tombstone(LIBRARY_DOMAIN, 9, vec![]))
        .unwrap();
    assert!(model.tree(&tree_root(TREE)).is_none());
    model
        .project(&on(LIBRARY_DOMAIN, 10, vec![observed(true, &[])]))
        .unwrap();
    assert!(model.tree(&tree_root(TREE)).unwrap().resources.is_empty());
}

#[test]
fn another_domains_book_is_none_of_its() {
    let mut model = LibraryCheckout::new();
    model
        .project(&on("trust", 0, vec![observed(true, &[])]))
        .unwrap();
    assert!(model.tree(&tree_root(TREE)).is_none());
}

#[test]
fn the_projection_is_the_trees_state_message() {
    use prost::Message as _;
    let model = LibraryCheckouts::default();
    let view = ready(model.dispatch(&ProjectRequest {
        events: Some(on(LIBRARY_DOMAIN, 0, vec![observed(true, &[(FILE, "f1")])])),
    }))
    .unwrap()
    .projection
    .expect("a view");
    assert_eq!(
        view.type_url,
        <library_domain_proto::v1::LibraryState as prost::Name>::type_url()
    );
    let state = library_domain_proto::v1::LibraryState::decode(view.value.as_slice()).unwrap();
    assert_eq!(state.name, TREE);
    assert_eq!(state.baseline_sequence, 0);
    assert_eq!(
        state
            .resources
            .get(FILE)
            .map(|r| (r.sequence, r.oid.as_str())),
        Some((0, "f1"))
    );
    assert_eq!(model.name(), "library-checkout");
    assert!(model.checkouts().tree(&tree_root(TREE)).is_some());
    let _ = caught_up(model);
}

#[test]
fn a_stream_it_holds_nothing_of_has_no_view() {
    use held_projection::Model as _;
    let model = LibraryCheckout::new();
    let cover = Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(tree_root(TREE)),
        edition: None,
    };
    assert!(model.view(&cover).is_none());
}

fn intended(request: &str, steps: usize) -> LibraryEvent {
    let step = library_tree::Step {
        replace: Some(library_tree::ReplaceFile {
            foreign: false,
            name: TREE.to_string(),
            verb: "fragment edit".to_string(),
            path: FILE.to_string(),
            seen_sequence: 0,
            new_oid: "n".to_string(),
            evidence: library_tree::Evidence {
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
    LibraryEvent::ChangeIntended(library_tree::ChangeIntended {
        intent: library_tree::Intent {
            foreign: Vec::new(),
            request_id: request.to_string(),
            verb: "fragment edit".to_string(),
            steps: vec![step; steps],
            base_cause: String::new(),
            done: 0,
        },
    })
}

fn applied(request: &str, step: u32) -> LibraryEvent {
    LibraryEvent::StepApplied(StepApplied {
        request_id: request.to_string(),
        step,
        observed: Vec::new(),
    })
}

#[test]
fn an_intent_is_known_by_its_request_id_pending_then_done() {
    use crate::End;
    let mut model = LibraryCheckout::new();
    model
        .project(&on(
            LIBRARY_DOMAIN,
            0,
            vec![observed(true, &[]), intended("r-1", 2), applied("r-1", 0)],
        ))
        .unwrap();
    let known = model.intent("r-1").expect("known");
    assert_eq!(known.end, End::Pending { done: 1 });
    assert_eq!(known.root, tree_root(TREE).value.to_vec());
    assert_eq!(known.intent.verb, "fragment edit");
    model
        .project(&on(LIBRARY_DOMAIN, 3, vec![applied("r-1", 1)]))
        .unwrap();
    assert_eq!(model.intent("r-1").map(|k| k.end), Some(End::Done));
    assert!(model.intent("r-2").is_none());
}

#[test]
fn an_intent_failed_is_known_failed_and_a_stream_closed_by_a_tombstone_forgets_it() {
    use crate::End;
    let mut model = LibraryCheckout::new();
    let failed = LibraryEvent::ChangeFailed(library_tree::ChangeFailed {
        request_id: "r-1".to_string(),
        step: 0,
        ext_err_txt: "denied".to_string(),
    });
    model
        .project(&on(
            LIBRARY_DOMAIN,
            0,
            vec![observed(true, &[]), intended("r-1", 1), failed],
        ))
        .unwrap();
    assert_eq!(model.intent("r-1").map(|k| k.end), Some(End::Failed));
    model
        .project(&after_tombstone(
            LIBRARY_DOMAIN,
            9,
            vec![observed(true, &[])],
        ))
        .unwrap();
    assert!(model.intent("r-1").is_none());
}

// AIP-155 (owner 2026-11-18, night): an export is known by its request id,
// what it answered kept; a stream a tombstone closes forgets it.
#[test]
fn an_export_is_known_by_its_request_id_with_what_it_answered() {
    let mut model = LibraryCheckout::new();
    let exported = LibraryEvent::BundleExported(library_tree::BundleExported {
        request_id: "r-9".to_string(),
        bundle: "demo".to_string(),
        source: "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
        destination: "/out/demo".to_string(),
    });
    model
        .project(&on(LIBRARY_DOMAIN, 0, vec![observed(true, &[]), exported]))
        .unwrap();
    let known = model.export("r-9").expect("known");
    assert_eq!(known.destination, "/out/demo");
    assert_eq!(
        known.source,
        "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    assert_eq!(known.bundle, "demo");
    assert!(model.export("r-8").is_none());
    model
        .project(&after_tombstone(
            LIBRARY_DOMAIN,
            9,
            vec![observed(true, &[])],
        ))
        .unwrap();
    assert!(model.export("r-9").is_none());
}

// Step 10c U13 (G7, protos.md §4.1): every recorded change is logged once,
// in order, as a thin notice: its tree, its sequence, its cause, its paths
// and how each changed, and for ctxloom's own step its request and verb (a
// path it wrote over one there was modified, a new one added). A book that
// folds the stream afresh logs nothing it logged before; other events are
// no change.
#[test]
fn each_recorded_change_is_told_to_each_watcher_once_in_order() {
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;
    let mut model = LibraryCheckout::new();
    let woken = Arc::new(AtomicU32::new(0));
    let wake = Arc::clone(&woken);
    let heard = model.watch(Arc::new(move || {
        wake.fetch_add(1, Ordering::SeqCst);
    }));
    model
        .project(&on(
            LIBRARY_DOMAIN,
            0,
            vec![observed(true, &[(FILE, "a")]), intended("r-1", 1)],
        ))
        .unwrap();
    let applied = LibraryEvent::StepApplied(StepApplied {
        request_id: "r-1".to_string(),
        step: 0,
        observed: vec![
            DirtyPath {
                path: FILE.to_string(),
                oid: "b".to_string(),
            },
            DirtyPath {
                path: "content/bundles/v2/demo/fragments/new.md".to_string(),
                oid: "c".to_string(),
            },
        ],
    });
    model
        .project(&on(LIBRARY_DOMAIN, 2, vec![applied]))
        .unwrap();
    let told: Vec<super::Recorded> = heard.try_iter().collect();
    assert_eq!(told.len(), 2, "the intent is no change: {told:?}");
    assert_eq!(woken.load(Ordering::SeqCst), 2, "woken at each change");
    let (first, second) = (&told[0], &told[1]);
    assert_eq!(first.tree, TREE);
    assert_eq!((first.at, first.sequence), (1, 0));
    assert_eq!(first.cause, "obs:0");
    assert_eq!(first.intended, None);
    assert_eq!(first.changes[0].path, FILE);
    assert_eq!(first.changes[0].status, "modified");
    assert_eq!((second.at, second.sequence), (2, 2));
    assert_eq!(
        second.intended,
        Some(("r-1".to_string(), "fragment edit".to_string()))
    );
    let statuses: Vec<(&str, &str)> = second
        .changes
        .iter()
        .map(|c| (c.path.as_str(), c.status.as_str()))
        .collect();
    assert_eq!(
        statuses,
        [
            (FILE, "modified"),
            ("content/bundles/v2/demo/fragments/new.md", "added")
        ]
    );
    // A watcher from now on is told only what is recorded from now on.
    let later = model.watch(Arc::new(|| {}));
    assert!(later.try_iter().next().is_none());
    // Folded afresh from sequence 0 (a catch-up): nothing is told again.
    model
        .project(&on(LIBRARY_DOMAIN, 0, vec![observed(true, &[(FILE, "a")])]))
        .unwrap();
    assert!(heard.try_iter().next().is_none());
    assert!(later.try_iter().next().is_none());
    // A watcher gone is let go at the next change: nothing is kept for it.
    drop(heard);
    drop(later);
    model
        .project(&on(
            LIBRARY_DOMAIN,
            3,
            vec![observed(false, &[(FILE, "d")])],
        ))
        .unwrap();
    assert_eq!(model.watching(), 0);
    assert_eq!(woken.load(Ordering::SeqCst), 2, "no watcher, nobody woken");
}
