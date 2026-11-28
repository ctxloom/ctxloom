//! Step 10c U13: a change reacted to: a watcher's notice, and what a
//! session is owed, collected.

#[test]
fn a_recorded_change_is_a_thin_notice_to_a_watcher_of_its_tree() {
    use super::{notice_of, watches};
    let recorded = library_checkout::Recorded {
        at: 3,
        tree: "projects/p/checkouts/c/library".to_string(),
        sequence: 7,
        cause: "cmd".to_string(),
        intended: Some(("r-1".to_string(), "fragment create".to_string())),
        changes: vec![library_checkout::Changed {
            path: "content/bundles/v2/demo/fragments/rules.md".to_string(),
            status: "added".to_string(),
            from_path: String::new(),
        }],
        recorded_at: (100, 5),
    };
    let notice = notice_of(&recorded);
    assert_eq!(notice.tree, recorded.tree);
    assert_eq!(
        notice.cause,
        daemon_proto::v1::library_change::Cause::Intended as i32
    );
    assert_eq!(
        (notice.request_id.as_str(), notice.verb.as_str()),
        ("r-1", "fragment create")
    );
    assert_eq!(notice.sequence, 7);
    assert_eq!(notice.changes[0].path, recorded.changes[0].path);
    assert_eq!(notice.changes[0].status, "added");
    assert_eq!(
        notice.observed_at.map(|at| (at.seconds, at.nanos)),
        Some((100, 5))
    );
    let outside = notice_of(&library_checkout::Recorded {
        intended: None,
        ..recorded.clone()
    });
    assert_eq!(
        outside.cause,
        daemon_proto::v1::library_change::Cause::External as i32
    );
    assert!(outside.request_id.is_empty() && outside.verb.is_empty());
    assert!(watches("projects/p", &recorded.tree));
    assert!(!watches("projects/q", &recorded.tree));
    assert!(!watches("projects/p", "projects/pq/checkouts/c/library"));
    assert!(watches("users/me", "users/me/library"));
    assert!(!watches("users/me", &recorded.tree));
}

/// An engine that carries a context at every hook, as claude's
/// additionalContext does; it drives nothing.
struct Carries;

impl run_ports::Engine for Carries {
    fn name(&self) -> &str {
        "claude-code"
    }
    fn prepare(
        &self,
        _: &run_ports::PrepareRequest,
    ) -> Result<run_ports::Prepared, run_ports::EngineError> {
        unreachable!("a delivery prepares nothing")
    }
    fn drive_turn(
        &self,
        _: &run_ports::TurnRequest,
    ) -> Result<run_ports::TurnCommand, run_ports::EngineError> {
        unreachable!("a delivery drives nothing")
    }
    fn finish_turn(&self, _: &run_ports::TurnOutput) -> run_ports::TurnOutcome {
        unreachable!("a delivery drives nothing")
    }
    fn drive_interactive(
        &self,
        _: &run_ports::TurnRequest,
    ) -> Result<run_ports::TurnCommand, run_ports::EngineError> {
        unreachable!("a delivery drives nothing")
    }
    fn deliver_context(&self, _: &str, context: &str) -> run_ports::ContextDelivery {
        run_ports::ContextDelivery::HookContext(context.to_string())
    }
}

const TREE: &str = "projects/calm-quiet-heron/checkouts/c/library";

/// `fact` recorded on the session `harp`'s stream, keyed `key`.
async fn on_session<F: angzarr_embedded::markup::DomainMessage>(
    f: &crate::server::tests::Fixture,
    harp: &str,
    fact: &F,
    key: &str,
) {
    use run_ports::Edge as _;
    let name = format!("projects/calm-quiet-heron/sessions/{harp}");
    let cover = angzarr_embedded::v1::Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(sessions::SessionName::parse(&name).unwrap().root()),
        edition: None,
    };
    f.world
        .handle(sessions::fact_book(cover, fact, key))
        .await
        .unwrap();
}

fn owed_at(sequence: u64) -> sessions::RecordContextOwed {
    sessions::RecordContextOwed {
        tree: TREE.to_string(),
        sequence,
        bundles: vec!["demo".to_string()],
        paths: vec!["content/bundles/v2/demo/fragments/a.md".to_string()],
    }
}

// Step 10c U13 rework, the race forced: a change recorded while a context
// is being re-assembled and handed is past the positions the delivery
// covers (read before it was assembled), so it stays owed and the next
// hook collects it. The old wall-clock mark (`context_since`, the
// delivery's own instant) lost it. A batch handed again is a DUPLICATE,
// and with nothing owed nothing is collected.
#[tokio::test]
async fn test_a_change_recorded_while_a_context_is_handed_stays_owed() {
    let f = crate::server::tests::fixture(vec![]);
    f.mint("amber-swift-owl").await;
    let loadout = sessions::RecordLoadout {
        loadout: sessions::SessionLoadout {
            agent: "dev".to_string(),
            profiles: Vec::new(),
            bundles: vec!["demo".to_string()],
        },
        incarnation: 1,
        covered: vec![sessions::TreePosition {
            tree: TREE.to_string(),
            sequence: 3,
        }],
    };
    on_session(&f, "amber-swift-owl", &loadout, "loadout:1").await;
    on_session(&f, "amber-swift-owl", &owed_at(4), "owed:4").await;
    f.catch_up().await;
    let record = || f.index.index().find("amber-swift-owl").unwrap();
    let before = record();
    let collected = f
        .api
        .collect(
            &before,
            (&Carries, "PostToolUse", run_launcher::HOOK_CONTEXT_BUDGET),
            || async {
                // Mid-delivery: the next change lands, owed to the session.
                on_session(&f, "amber-swift-owl", &owed_at(5), "owed:5").await;
                f.catch_up().await;
                Ok("the context".to_string())
            },
            |_| async { Vec::new() },
        )
        .await
        .unwrap();
    let (text, changed) = collected.expect("delivered");
    assert!(text.contains("the context"), "{text}");
    assert_eq!(changed, ["demo"]);
    f.catch_up().await;
    let handed = record().context.unwrap();
    assert_eq!(handed.covered.get(TREE), Some(&4));
    assert_eq!(
        handed.owed_positions(),
        [sessions::TreePosition {
            tree: TREE.to_string(),
            sequence: 5,
        }],
        "the change recorded mid-delivery stays owed"
    );
    // The same batch handed again (a second hook that read it first): a
    // DUPLICATE, nothing handed.
    let again = f
        .api
        .collect(
            &before,
            (&Carries, "PostToolUse", run_launcher::HOOK_CONTEXT_BUDGET),
            || async { Ok("the context".to_string()) },
            |_| async { Vec::new() },
        )
        .await
        .unwrap();
    assert_eq!(again, None);
    // The next hook collects the change left owed, and then nothing is.
    let next = f
        .api
        .collect(
            &record(),
            (
                &Carries,
                "UserPromptSubmit",
                run_launcher::HOOK_CONTEXT_BUDGET,
            ),
            || async { Ok("the context, again".to_string()) },
            |_| async { Vec::new() },
        )
        .await
        .unwrap();
    assert!(next.is_some());
    f.catch_up().await;
    assert!(record().context.unwrap().owed.is_empty());
    let none = f
        .api
        .collect(
            &record(),
            (
                &Carries,
                "UserPromptSubmit",
                run_launcher::HOOK_CONTEXT_BUDGET,
            ),
            || async { panic!("nothing owed is never assembled") },
            |_| async { Vec::new() },
        )
        .await
        .unwrap();
    assert_eq!(none, None);
}

// Step 10c U13 rework (owner, 2026-11-18): over the hook's budget a
// delivery is only the fragments that changed, each whole, asked of the
// library only then (by the owed paths), and recorded as what was handed.
#[tokio::test]
async fn test_a_delivery_over_its_budget_gives_the_changed_fragments_alone() {
    let f = crate::server::tests::fixture(vec![]);
    f.mint("amber-swift-owl").await;
    let loadout = sessions::RecordLoadout {
        loadout: sessions::SessionLoadout {
            agent: "dev".to_string(),
            profiles: Vec::new(),
            bundles: vec!["demo".to_string()],
        },
        incarnation: 1,
        covered: Vec::new(),
    };
    on_session(&f, "amber-swift-owl", &loadout, "loadout:1").await;
    on_session(&f, "amber-swift-owl", &owed_at(4), "owed:4").await;
    f.catch_up().await;
    let record = f.index.index().find("amber-swift-owl").unwrap();
    let asked = std::sync::Mutex::new(Vec::new());
    let unchanged: String = std::iter::repeat_n('#', 2_000).collect();
    let (text, _) = f
        .api
        .collect(
            &record,
            (&Carries, "PostToolUse", 1_000),
            || async { Ok(unchanged.clone()) },
            |owed| {
                asked.lock().unwrap().extend(owed);
                async {
                    vec![run_launcher::ChangedFragment {
                        name: "projects/calm-quiet-heron/bundles/demo/fragments/a".to_string(),
                        body: "the new a".to_string(),
                    }]
                }
            },
        )
        .await
        .unwrap()
        .expect("delivered");
    assert!(text.chars().count() <= 1_000, "{text}");
    assert!(!text.contains('#'), "{text}");
    assert!(text.contains("the new a"), "{text}");
    assert_eq!(
        *asked.lock().unwrap(),
        [(
            TREE.to_string(),
            "content/bundles/v2/demo/fragments/a.md".to_string()
        )]
    );
    // Within the budget, whole, and the library is never asked.
    on_session(&f, "amber-swift-owl", &owed_at(5), "owed:5").await;
    f.catch_up().await;
    let record = f.index.index().find("amber-swift-owl").unwrap();
    let whole = f
        .api
        .collect(
            &record,
            (&Carries, "PostToolUse", 10_000),
            || async { Ok("small".to_string()) },
            |_| async { panic!("the library is not asked") },
        )
        .await
        .unwrap()
        .expect("delivered");
    assert!(whole.0.ends_with("\n\nsmall"), "{}", whole.0);
    // No room even for a pointer: nothing handed, still owed.
    on_session(&f, "amber-swift-owl", &owed_at(6), "owed:6").await;
    f.catch_up().await;
    let record = f.index.index().find("amber-swift-owl").unwrap();
    let none = f
        .api
        .collect(
            &record,
            (&Carries, "PostToolUse", 5),
            || async { Ok(unchanged.clone()) },
            |_| async { Vec::new() },
        )
        .await
        .unwrap();
    assert_eq!(none, None);
    f.catch_up().await;
    assert!(!f
        .index
        .index()
        .find("amber-swift-owl")
        .unwrap()
        .context
        .unwrap()
        .owed
        .is_empty());
}

// A request's budget: 0 is the whole hook budget; less is what it says;
// more is the hook budget still.
#[test]
fn test_a_requests_budget_is_at_most_the_hooks() {
    use super::budget_of;
    assert_eq!(budget_of(0), 10_000);
    assert_eq!(budget_of(2_500), 2_500);
    assert_eq!(budget_of(10_001), 10_000);
}

/// A listing of `tree` recorded through the fixture's kernel, which folds
/// the checkouts inline.
async fn listed(world: &crate::server::tests::World, tree: &library_tree::TreeName, oid: &str) {
    use run_ports::Edge as _;
    let fact = library_tree::RecordTreeListed {
        name: tree.to_string(),
        kind: tree.kind(),
        base_cause: world
            .checkouts
            .checkouts()
            .tree(&tree.root())
            .map(|held| held.last_cause.clone())
            .unwrap_or_default(),
        files: vec![library_tree::DirtyPath {
            path: "config.yaml".to_string(),
            oid: oid.to_string(),
        }],
        contents: Vec::new(),
    };
    world
        .handle(library_tree::listing_book(tree, &fact))
        .await
        .unwrap();
}

// Step 10c U13 rework: WatchLibrary is signalled by the catch-up, never
// polled: a change recorded wakes the watcher, which drains it; a change
// of a tree it does not watch wakes it and tells it nothing; nothing is
// kept for a watcher once it is gone.
#[tokio::test]
async fn test_a_watcher_is_woken_and_told_each_change_its_trees_record() {
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;
    let f = crate::server::tests::fixture(vec![]);
    let world = Arc::clone(&f.world);
    let gits = Arc::new(crate::server::tests::Gits::default());
    let api = f
        .api
        .observing(crate::server::tests::observing(&world, &gits));
    let woken = Arc::new(AtomicU32::new(0));
    let wake = Arc::clone(&woken);
    let mut poll = api
        .watch_library(
            &daemon_proto::v1::WatchLibraryRequest {
                parent: "projects/calm-quiet-heron".to_string(),
                ..daemon_proto::v1::WatchLibraryRequest::default()
            },
            Arc::new(move || {
                wake.fetch_add(1, Ordering::SeqCst);
            }),
        )
        .unwrap();
    assert!(poll().is_empty());
    let checkout = library_tree::TreeName::checkout(
        sessions::ProjectName::new("calm-quiet-heron").unwrap(),
        "brisk-copper-moth",
    )
    .unwrap();
    listed(&world, &checkout, "a").await;
    assert_eq!(woken.load(Ordering::SeqCst), 1);
    let told = poll();
    assert_eq!(told.len(), 1, "{told:?}");
    assert_eq!(told[0].tree, checkout.to_string());
    assert!(poll().is_empty(), "drained once");
    let home = library_tree::TreeName::home("brisk-copper-moth").unwrap();
    listed(&world, &home, "h").await;
    assert_eq!(woken.load(Ordering::SeqCst), 2);
    assert!(poll().is_empty(), "the home's change is not this watcher's");
    drop(poll);
    listed(&world, &checkout, "b").await;
    assert_eq!(world.checkouts.checkouts().watching(), 0);
}
