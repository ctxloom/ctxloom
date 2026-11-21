//! A library tree through the kernel (`Harness`, over an in-memory store):
//! observations recorded once and only when they add something, each event
//! tying its paths to its own sequence; commands decided on their evidence
//! and on the token the caller saw (G4 superseded).

use std::collections::BTreeMap;

use angzarr_embedded::markup::{DomainMessage, TypedRefusal};
use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin};
use angzarr_embedded::{Any, Failure, Handled, Registry};

use crate::{
    observation_key, tree_root, Change, ChangeStatus, CreateDirectory, DirtyPath, Evidence,
    LibraryAggregate, LibraryCommand, LibraryEvent, LibraryTreeRejection, MovePath, PlanRejected,
    PurgeTrash, RecordBundleExported, RecordStepDone, RecordStepFailed, RecordTreeObserved,
    ReplaceFile, Resource, RunSequence, Step, TrashPath, TreeKind, KEEP_DAYS, LIBRARY_DOMAIN,
    TREE_UNREADABLE,
};

const TREE: &str = "projects/amber-quiet-heron/checkouts/brisk-copper-moth/library";
const FILE: &str = "content/bundles/v2/demo/fragments/example.md";
const YAML: &str = "content/bundles/v2/demo/bundle.yaml";
const FRAGMENT: &str = "content/bundles/v2/demo/fragments/a.md";
const BUNDLE: &str = "content/bundles/v2/demo";

fn cover() -> Cover {
    Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(tree_root(TREE)),
        edition: None,
    }
}

struct Tree {
    harness: Harness<LibraryAggregate>,
}

impl Tree {
    fn new() -> Tree {
        let mut registry = Registry::new();
        registry
            .aggregate::<LibraryAggregate>(LibraryAggregate::default)
            .unwrap();
        Tree {
            harness: Harness::new(registry, cover()).with_samples(Samples::starting_at(100)),
        }
    }

    /// A tree baselined clean at `h-0`, its first event at sequence 0.
    fn baselined() -> Tree {
        let mut tree = Tree::new();
        let events = tree.observe(observation("h-0", "h-0", vec![], &[], true));
        assert_eq!(events.len(), 1);
        tree
    }

    fn library(&self) -> crate::Library {
        self.harness.state().state().clone()
    }

    /// `fact` as an observer makes it: against the state it read, keyed by
    /// the chain over its last cause.
    fn observe(&mut self, mut fact: RecordTreeObserved) -> Vec<LibraryEvent> {
        let last = self.library().last_cause;
        fact.base_cause = last.clone();
        let key = observation_key(TREE, &last, &fact.head_after, &fact.dirty_after);
        self.fact(fact, &key)
    }

    fn fact(&mut self, fact: impl DomainMessage, external_id: &str) -> Vec<LibraryEvent> {
        self.harness
            .when_book(fact_book(fact, external_id))
            .events()
    }

    fn handled(&mut self, fact: impl DomainMessage, external_id: &str) -> Handled {
        self.harness
            .when_book(fact_book(fact, external_id))
            .handled()
            .expect("handled")
    }

    fn command(
        &mut self,
        command: impl Into<LibraryCommand>,
    ) -> Result<Vec<LibraryEvent>, LibraryTreeRejection> {
        match self.harness.when(command).result() {
            Ok(events) => Ok(events),
            Err(Failure::Rejected(rejected)) => {
                let typed = rejected
                    .rejection::<TypedRefusal>()
                    .expect("a typed refusal");
                Err(LibraryTreeRejection::try_from(typed).expect("a library refusal"))
            }
            Err(other) => panic!("not a rejection: {other:?}"),
        }
    }

    fn event_count(&self) -> usize {
        self.harness.pages().len()
    }
}

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

fn dirty(entries: &[(&str, &str)]) -> Vec<DirtyPath> {
    entries
        .iter()
        .map(|(path, oid)| DirtyPath {
            path: path.to_string(),
            oid: oid.to_string(),
        })
        .collect()
}

fn change(path: &str, status: ChangeStatus, from: &str, to: &str) -> Change {
    Change {
        path: path.to_string(),
        status,
        from_oid: from.to_string(),
        to_oid: to.to_string(),
        from_path: String::new(),
    }
}

fn observation(
    before: &str,
    after: &str,
    changes: Vec<Change>,
    dirty_after: &[(&str, &str)],
    baseline: bool,
) -> RecordTreeObserved {
    RecordTreeObserved {
        name: TREE.to_string(),
        kind: TreeKind::Checkout,
        base_cause: String::new(),
        head_before: before.to_string(),
        head_after: after.to_string(),
        tree_after: format!("tree-of-{after}"),
        changes,
        dirty_after: dirty(dirty_after),
        baseline,
    }
}

/// An edit of `path` to `oid`, seen against HEAD `h-0`.
fn edit(path: &str, oid: &str) -> RecordTreeObserved {
    observation(
        "h-0",
        "h-0",
        vec![change(path, ChangeStatus::Modified, "base", oid)],
        &[(path, oid)],
        false,
    )
}

fn evidence() -> Evidence {
    Evidence {
        tree_reads: true,
        plan_rejection: None,
        builtin: false,
    }
}

fn replace(path: &str, seen: u64) -> ReplaceFile {
    ReplaceFile {
        foreign: false,
        name: TREE.to_string(),
        verb: "fragment edit".to_string(),
        path: path.to_string(),
        seen_sequence: seen,
        new_oid: "new".to_string(),
        evidence: evidence(),
    }
}

fn create(path: &str) -> CreateDirectory {
    CreateDirectory {
        name: TREE.to_string(),
        verb: "bundle create".to_string(),
        path: path.to_string(),
        entries: dirty(&[("bundle.yaml", "y")]),
        evidence: evidence(),
    }
}

fn trash(path: &str, seen: u64) -> TrashPath {
    TrashPath {
        name: TREE.to_string(),
        verb: "bundle remove".to_string(),
        path: path.to_string(),
        seen_sequence: seen,
        evidence: evidence(),
        keep: true,
    }
}

fn moved(from: &str, to: &str, seen: u64) -> MovePath {
    MovePath {
        name: TREE.to_string(),
        verb: "bundle move".to_string(),
        from: from.to_string(),
        to: to.to_string(),
        seen_sequence: seen,
        evidence: evidence(),
        to_tree: String::new(),
    }
}

fn step_of(
    replace: Option<ReplaceFile>,
    trash: Option<TrashPath>,
    r#move: Option<MovePath>,
) -> Step {
    Step {
        replace,
        create: None,
        trash,
        r#move,
        purge: None,
    }
}

fn sequence(steps: Vec<Step>) -> RunSequence {
    RunSequence {
        foreign: Vec::new(),
        name: TREE.to_string(),
        verb: "deps pull".to_string(),
        steps,
        evidence: evidence(),
    }
}

fn stale(path: &str, seen: u64, now: u64) -> LibraryTreeRejection {
    LibraryTreeRejection::PlanStale {
        path: path.to_string(),
        seen,
        now,
    }
}

fn resource(sequence: u64, oid: &str) -> Resource {
    Resource {
        sequence,
        oid: oid.to_string(),
    }
}

// region observations

/// A listing of `files` (path, content id) against the tree's last cause.
fn listing(tree: &Tree, files: &[(&str, &str)]) -> crate::RecordTreeListed {
    crate::RecordTreeListed {
        name: TREE.to_string(),
        kind: TreeKind::Checkout,
        base_cause: tree.library().last_cause,
        files: dirty(files),
    }
}

/// `fact` recorded keyed as a scanner keys it.
fn listed(tree: &mut Tree, fact: crate::RecordTreeListed) -> Vec<LibraryEvent> {
    let key = crate::listing_key(TREE, &fact.base_cause, &fact.files);
    tree.fact(fact, &key)
}

// Step 10c U13 rework (the scanner, owner 2026-11-18): a listing of every
// file with its content id is compared with the listing the state holds:
// the first is a baseline of every file; then each path added, changed or
// gone is one change of one event, each path's token its sequence; a
// listing that matches records nothing; A to B to A is three, never a
// duplicate. No git is asked: no head, no tree id.
#[test]
fn test_a_listing_records_each_path_added_changed_or_gone_against_the_last() {
    let mut tree = Tree::new();
    let first = listing(&tree, &[(YAML, "y1"), (FRAGMENT, "f1")]);
    let events = listed(&mut tree, first);
    let [LibraryEvent::TreeObserved(baseline)] = &events[..] else {
        panic!("one baseline: {events:?}")
    };
    assert!(baseline.baseline);
    assert_eq!(baseline.changes.len(), 2, "every file: {baseline:?}");
    assert!(baseline
        .changes
        .iter()
        .all(|c| c.status == ChangeStatus::Added));
    let same = listing(&tree, &[(YAML, "y1"), (FRAGMENT, "f1")]);
    assert!(
        listed(&mut tree, same).is_empty(),
        "a match records nothing"
    );
    let next = listing(&tree, &[(FRAGMENT, "f2"), ("remotes.yaml", "r1")]);
    let events = listed(&mut tree, next);
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("one observation: {events:?}")
    };
    assert!(!observed.baseline);
    let said: Vec<(&str, ChangeStatus, &str, &str)> = observed
        .changes
        .iter()
        .map(|c| {
            (
                c.path.as_str(),
                c.status,
                c.from_oid.as_str(),
                c.to_oid.as_str(),
            )
        })
        .collect();
    assert_eq!(
        said,
        [
            (YAML, ChangeStatus::Deleted, "y1", ""),
            (FRAGMENT, ChangeStatus::Modified, "f1", "f2"),
            ("remotes.yaml", ChangeStatus::Added, "", "r1"),
        ],
        "sorted by path"
    );
    let library = tree.library();
    assert_eq!(library.resources.get(FRAGMENT), Some(&resource(1, "f2")));
    assert_eq!(library.resources.get(YAML), Some(&resource(1, "")));
    assert_eq!((library.head.as_str(), library.tree.as_str()), ("", ""));
    // Back to what it was, then on again: each is recorded.
    let back = listing(&tree, &[(FRAGMENT, "f1"), ("remotes.yaml", "r1")]);
    assert_eq!(listed(&mut tree, back).len(), 1);
    let again = listing(&tree, &[(FRAGMENT, "f2"), ("remotes.yaml", "r1")]);
    assert_eq!(listed(&mut tree, again).len(), 1);
    assert_eq!(
        tree.library().resources.get(FRAGMENT),
        Some(&resource(3, "f2"))
    );
}

// A path a step took away is held with an empty id: absent. A listing
// without it matches, and records nothing.
#[test]
fn test_a_path_held_gone_is_absent_from_the_listing_compared() {
    let mut tree = Tree::new();
    let first = listing(&tree, &[(YAML, "y1"), (FRAGMENT, "f1")]);
    listed(&mut tree, first);
    let mut held = tree.library();
    held.dirty.insert(FRAGMENT.to_string(), String::new());
    assert_eq!(held.listed().get(FRAGMENT), None, "gone");
    assert_eq!(
        super::listing_changes(&held.listed(), &dirty(&[(YAML, "y1")])),
        Vec::new(),
        "nothing changed"
    );
}

// A listing's key chains on the cause the scanner read and hashes the
// listing in path order: the same listing against another cause is another
// key; one listed in any order is the same.
#[test]
fn test_a_listing_key_chains_on_its_cause_and_is_its_listing_s_whatever_the_order() {
    let a = dirty(&[(YAML, "y"), (FRAGMENT, "f")]);
    let b = dirty(&[(FRAGMENT, "f"), (YAML, "y")]);
    let key = crate::listing_key(TREE, "c1", &a);
    assert!(key.starts_with(&format!("listed:{TREE}:")), "{key}");
    assert_eq!(key, crate::listing_key(TREE, "c1", &b));
    assert_ne!(key, crate::listing_key(TREE, "c2", &a));
    assert_ne!(key, crate::listing_key(TREE, "c1", &dirty(&[(YAML, "y")])));
}

#[test]
fn a_trees_first_observation_is_its_baseline_listing_what_is_dirty() {
    let mut tree = Tree::new();
    let events = tree.observe(observation(
        "",
        "h-0",
        vec![change(YAML, ChangeStatus::Added, "", "y1")],
        &[(YAML, "y1")],
        true,
    ));
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("one observation: {events:?}")
    };
    assert!(observed.baseline);
    assert_eq!(observed.name, TREE);
    assert_eq!(observed.changes.len(), 1);
    let external = observed.external.as_ref().expect("an outside cause");
    assert_eq!(
        (external.head_before.as_str(), external.head_after.as_str()),
        ("", "h-0")
    );
    assert_eq!(observed.intended, None);
    let library = tree.library();
    assert_eq!(library.kind, Some(TreeKind::Checkout));
    assert_eq!(library.baseline_sequence, 0);
    assert_eq!(library.resources.get(YAML), Some(&resource(0, "y1")));
    assert_eq!(library.head, "h-0");
}

#[test]
fn a_first_observation_not_marked_a_baseline_is_recorded_as_one() {
    let mut tree = Tree::new();
    let events = tree.observe(edit(FILE, "f1"));
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("{events:?}")
    };
    assert!(observed.baseline);
    assert!(tree.library().baselined);
}

#[test]
fn a_clean_baseline_is_recorded_though_it_lists_no_change() {
    let tree = Tree::baselined();
    assert_eq!(tree.event_count(), 1);
    assert!(tree.library().resources.is_empty());
}

#[test]
fn a_read_that_sees_what_the_record_holds_records_nothing() {
    let mut tree = Tree::baselined();
    assert!(tree
        .observe(observation("h-0", "h-0", vec![], &[], false))
        .is_empty());
    assert_eq!(tree.event_count(), 1);
}

#[test]
fn a_commit_that_moves_head_alone_records_nothing() {
    let mut tree = Tree::baselined();
    let mut moved = observation("h-0", "h-1", vec![], &[], false);
    moved.tree_after = "tree-of-h-0".to_string();
    assert!(tree.observe(moved).is_empty());
    assert_eq!(
        tree.library().head,
        "h-0",
        "the head goes stale (QUESTIONS A)"
    );
}

#[test]
fn a_new_library_tree_alone_is_recorded() {
    let mut tree = Tree::baselined();
    let events = tree.observe(observation("h-0", "h-1", vec![], &[], false));
    assert_eq!(events.len(), 1);
    assert_eq!(tree.library().tree, "tree-of-h-1");
    assert_eq!(tree.library().head, "h-1");
}

#[test]
fn a_new_dirty_set_alone_is_recorded() {
    let mut tree = Tree::baselined();
    let events = tree.observe(observation("h-0", "h-0", vec![], &[(FILE, "f1")], false));
    assert_eq!(events.len(), 1);
}

#[test]
fn a_hand_edit_is_recorded_once_its_path_tied_to_the_events_sequence() {
    let mut tree = Tree::baselined();
    assert_eq!(tree.observe(edit(FILE, "f1")).len(), 1);
    let library = tree.library();
    assert_eq!(library.resources.get(FILE), Some(&resource(1, "f1")));
    assert_eq!(library.token(FILE), 1);
    assert_eq!(library.token(YAML), 0);
    assert_eq!(library.token(BUNDLE), 1);
    assert!(
        tree.observe(edit(FILE, "f1")).is_empty(),
        "seen again: nothing"
    );
}

#[test]
fn a_second_edit_of_a_dirty_file_is_a_second_event() {
    let mut tree = Tree::baselined();
    tree.observe(edit(FILE, "f1"));
    assert_eq!(tree.observe(edit(FILE, "f2")).len(), 1);
    assert_eq!(tree.library().resources.get(FILE), Some(&resource(2, "f2")));
}

#[test]
fn edited_put_back_and_edited_again_is_three_events_each_keyed_apart() {
    let mut tree = Tree::baselined();
    let b = edit(YAML, "b");
    let mut back = observation(
        "h-0",
        "h-0",
        vec![change(YAML, ChangeStatus::Modified, "b", "a")],
        &[],
        false,
    );
    back.tree_after = "tree-of-h-0".to_string();
    let mut keys = Vec::new();
    for fact in [b.clone(), back.clone(), b, back] {
        let last = tree.library().last_cause;
        keys.push(observation_key(
            TREE,
            &last,
            &fact.head_after,
            &fact.dirty_after,
        ));
        assert_eq!(
            tree.observe(fact).len(),
            1,
            "A to B to A to B is never a duplicate"
        );
    }
    assert_eq!(tree.event_count(), 5);
    let mut unique = keys.clone();
    unique.sort();
    unique.dedup();
    assert_eq!(unique.len(), 4, "{keys:?}");
    assert_eq!(tree.library().resources.get(YAML), Some(&resource(4, "a")));
}

#[test]
fn the_same_observation_delivered_twice_is_a_duplicate() {
    let mut tree = Tree::baselined();
    let last = tree.library().last_cause;
    let mut fact = edit(FILE, "f1");
    fact.base_cause = last.clone();
    let key = observation_key(TREE, &last, "h-0", &fact.dirty_after);
    assert!(matches!(
        tree.handled(fact.clone(), &key),
        Handled::Applied(_)
    ));
    assert!(matches!(tree.handled(fact, &key), Handled::Duplicate(..)));
    assert_eq!(tree.event_count(), 2);
}

#[test]
fn an_observation_records_its_cause_as_the_next_chains_last() {
    let mut tree = Tree::baselined();
    let last = tree.library().last_cause;
    tree.observe(edit(FILE, "f1"));
    let expected = observation_key(TREE, &last, "h-0", &dirty(&[(FILE, "f1")]));
    assert_eq!(tree.library().last_cause, expected);
}

#[test]
fn an_observation_made_against_an_older_state_records_only_what_it_adds() {
    let mut tree = Tree::baselined();
    let base = tree.library().last_cause;
    tree.observe(edit(FILE, "f1"));
    // A racing read made against the baseline saw the same edit and one
    // more: only the one more is recorded.
    let mut racing = observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Modified, "base", "f1"),
            change(YAML, ChangeStatus::Modified, "base", "y1"),
        ],
        &[(FILE, "f1"), (YAML, "y1")],
        false,
    );
    racing.base_cause = base.clone();
    let key = observation_key(TREE, &base, "h-0", &racing.dirty_after);
    let events = tree.fact(racing.clone(), &format!("{key}-racing"));
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(observed.changes.len(), 1);
    assert_eq!(observed.changes[0].path, YAML);
    assert_eq!(tree.library().resources.get(FILE), Some(&resource(1, "f1")));
    assert_eq!(tree.library().resources.get(YAML), Some(&resource(2, "y1")));
}

#[test]
fn an_older_observation_of_what_is_recorded_records_nothing() {
    let mut tree = Tree::baselined();
    let base = tree.library().last_cause;
    tree.observe(edit(FILE, "f1"));
    let mut racing = edit(FILE, "f1");
    racing.base_cause = base;
    assert!(tree.fact(racing, "obs:racing").is_empty());
}

#[test]
fn an_older_rename_whose_old_path_the_record_still_holds_is_kept() {
    let mut tree = Tree::baselined();
    let base = tree.library().last_cause;
    tree.observe(edit(FILE, "f1"));
    tree.observe(observation(
        "h-0",
        "h-0",
        vec![change(
            "content/bundles/v2/demo/fragments/new.md",
            ChangeStatus::Added,
            "",
            "f1",
        )],
        &[
            (FILE, "f1"),
            ("content/bundles/v2/demo/fragments/new.md", "f1"),
        ],
        false,
    ));
    let mut rename = change(
        "content/bundles/v2/demo/fragments/new.md",
        ChangeStatus::Renamed,
        "f1",
        "f1",
    );
    rename.from_path = FILE.to_string();
    let mut racing = observation("h-0", "h-0", vec![rename], &[], false);
    racing.base_cause = base;
    let events = tree.fact(racing, "obs:racing");
    assert_eq!(events.len(), 1, "the old path is still alive in the record");
    assert!(tree.library().known_absent(FILE));
}

#[test]
fn an_older_baseline_after_one_was_recorded_records_nothing() {
    let mut tree = Tree::baselined();
    let mut again = observation("h-0", "h-0", vec![], &[], true);
    again.base_cause = String::new();
    assert!(tree.fact(again, "obs:another-baseline").is_empty());
}

#[test]
fn a_baseline_made_against_the_state_rebaselines_the_tree() {
    let mut tree = Tree::baselined();
    tree.observe(edit(FILE, "f1"));
    let events = tree.observe(observation("h-0", "h-0", vec![], &[(FILE, "f1")], true));
    assert_eq!(events.len(), 1, "a gc'd HEAD re-baselines");
    let library = tree.library();
    assert_eq!(library.baseline_sequence, 2);
    assert_eq!(library.token(FILE), 2);
}

#[test]
fn another_trees_observation_records_nothing() {
    let mut tree = Tree::baselined();
    let mut other = edit(FILE, "f1");
    other.name = "projects/amber-quiet-heron/checkouts/calm-amber-fox/library".to_string();
    assert!(tree.observe(other).is_empty());
}

// endregion

// region commands

#[test]
fn a_replace_on_the_token_it_saw_records_its_intent_and_moves_no_token() {
    let mut tree = Tree::baselined();
    tree.observe(edit(FILE, "f1"));
    let events = tree.command(replace(FILE, 1)).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    let intent = &intended.intent;
    assert_eq!(intent.verb, "fragment edit");
    assert_eq!(intent.done, 0);
    assert_eq!(intent.steps.len(), 1);
    assert_eq!(
        intent.steps[0].replace.as_ref().map(|r| r.new_oid.as_str()),
        Some("new")
    );
    assert!(
        uuid::Uuid::parse_str(&intent.request_id).is_ok(),
        "{}",
        intent.request_id
    );
    let before = observation_key(TREE, "", "h-0", &[]);
    assert_ne!(intent.base_cause, "");
    assert_ne!(intent.base_cause, before);
    let library = tree.library();
    assert_eq!(library.token(FILE), 1);
    assert!(library.pending.contains_key(&intent.request_id));
    assert_eq!(library.last_cause, intent.request_id);
}

#[test]
fn a_replace_against_a_moved_token_is_refused_stale() {
    let mut tree = Tree::baselined();
    tree.observe(edit(FILE, "f1"));
    tree.observe(edit(FILE, "f2"));
    assert_eq!(tree.command(replace(FILE, 1)), Err(stale(FILE, 1, 2)));
    assert_eq!(tree.command(replace(YAML, 1)), Err(stale(YAML, 1, 0)));
    assert_eq!(tree.event_count(), 3, "a refusal records nothing");
}

#[test]
fn a_change_undone_since_still_moves_the_token() {
    let mut tree = Tree::baselined();
    tree.observe(edit(FILE, "f1"));
    tree.observe(edit(FILE, "base"));
    assert_eq!(tree.command(replace(FILE, 0)), Err(stale(FILE, 0, 2)));
}

#[test]
fn the_built_in_bundle_is_refused_naming_it() {
    let mut tree = Tree::baselined();
    let mut cmd = replace("content/bundles/v2/ctxloom/prompts/setup-interview.md", 0);
    cmd.evidence.builtin = true;
    assert_eq!(
        tree.command(cmd),
        Err(LibraryTreeRejection::BuiltinReadOnly {
            bundle: "ctxloom".to_string()
        })
    );
    let mut cmd = trash("config.yaml", 0);
    cmd.evidence.builtin = true;
    assert_eq!(
        tree.command(cmd),
        Err(LibraryTreeRejection::BuiltinReadOnly {
            bundle: "config.yaml".to_string()
        })
    );
    let mut cmd = create("content/bundles/v2/");
    cmd.evidence.builtin = true;
    assert_eq!(
        tree.command(cmd),
        Err(LibraryTreeRejection::BuiltinReadOnly {
            bundle: "content/bundles/v2/".to_string()
        })
    );
}

#[test]
fn a_plans_rejection_is_refused_as_the_plan_said_it() {
    let mut tree = Tree::baselined();
    let mut cmd = create(BUNDLE);
    let args = BTreeMap::from([("bundle".to_string(), "demo".to_string())]);
    cmd.evidence.plan_rejection = Some(PlanRejected {
        code: "library-application-write-x".to_string(),
        args: args.clone(),
    });
    assert_eq!(
        tree.command(cmd),
        Err(LibraryTreeRejection::PlanRejected {
            code: "library-application-write-x".to_string(),
            args,
        })
    );
}

#[test]
fn a_tree_that_would_not_read_is_a_plans_rejection_of_its_own() {
    let mut tree = Tree::baselined();
    let mut cmd = replace(FILE, 0);
    cmd.evidence.tree_reads = false;
    assert_eq!(
        tree.command(cmd),
        Err(LibraryTreeRejection::PlanRejected {
            code: TREE_UNREADABLE.to_string(),
            args: BTreeMap::new(),
        })
    );
}

#[test]
fn the_built_in_bundle_is_judged_before_the_plan() {
    let mut tree = Tree::baselined();
    let mut cmd = replace(FILE, 0);
    cmd.evidence.builtin = true;
    cmd.evidence.tree_reads = false;
    assert!(matches!(
        tree.command(cmd),
        Err(LibraryTreeRejection::BuiltinReadOnly { .. })
    ));
}

#[test]
fn a_directory_the_record_knows_is_there_is_not_created_again() {
    let mut tree = Tree::baselined();
    tree.observe(edit(YAML, "y1"));
    assert_eq!(
        tree.command(create(BUNDLE)),
        Err(LibraryTreeRejection::ResourceExists {
            path: BUNDLE.to_string()
        })
    );
    let events = tree.command(create("content/bundles/v2/fresh")).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    assert!(intended.intent.steps[0].create.is_some());
}

#[test]
fn a_path_the_record_knows_is_gone_is_not_trashed() {
    let mut tree = Tree::baselined();
    tree.observe(observation(
        "h-0",
        "h-0",
        vec![change(FILE, ChangeStatus::Deleted, "f0", "")],
        &[(FILE, "")],
        false,
    ));
    assert_eq!(
        tree.command(trash(FILE, 1)),
        Err(LibraryTreeRejection::NoSuchResource {
            path: FILE.to_string()
        })
    );
    let events = tree.command(trash(YAML, 0)).unwrap();
    assert_eq!(events.len(), 1, "a path no event named is not known gone");
}

#[test]
fn a_trash_judges_neither_the_plan_nor_a_stale_token_before_existence() {
    let mut tree = Tree::baselined();
    let mut cmd = trash(YAML, 0);
    cmd.evidence.tree_reads = false;
    assert_eq!(tree.command(cmd).map(|e| e.len()), Ok(1));
    assert_eq!(tree.command(trash(YAML, 4)), Err(stale(YAML, 4, 0)));
}

#[test]
fn a_move_judges_where_it_comes_from_where_it_goes_and_the_token_it_saw() {
    let mut tree = Tree::baselined();
    tree.observe(observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Deleted, "f0", ""),
            change(YAML, ChangeStatus::Modified, "y0", "y1"),
        ],
        &[(FILE, ""), (YAML, "y1")],
        false,
    ));
    assert_eq!(
        tree.command(moved(FILE, "content/bundles/v2/other", 1)),
        Err(LibraryTreeRejection::NoSuchResource {
            path: FILE.to_string()
        })
    );
    assert_eq!(
        tree.command(moved("content/bundles/v2/other", BUNDLE, 0)),
        Err(LibraryTreeRejection::ResourceExists {
            path: BUNDLE.to_string()
        })
    );
    assert_eq!(
        tree.command(moved(BUNDLE, "content/bundles/v2/renamed", 0)),
        Err(stale(BUNDLE, 0, 1))
    );
    let mut ours = moved(BUNDLE, "content/bundles/v2/renamed", 1);
    ours.evidence.builtin = true;
    ours.evidence.tree_reads = false;
    let events = tree.command(ours).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(
        intended.intent.steps[0]
            .r#move
            .as_ref()
            .map(|m| m.to.as_str()),
        Some("content/bundles/v2/renamed")
    );
}

#[test]
fn a_sequence_judges_each_step_as_its_own_command_and_keeps_them_all() {
    let mut tree = Tree::baselined();
    tree.observe(edit(YAML, "y1"));
    let steps = vec![
        step_of(Some(replace("lock.yaml", 0)), None, None),
        step_of(Some(replace(YAML, 0)), None, None),
    ];
    assert_eq!(tree.command(sequence(steps)), Err(stale(YAML, 0, 1)));
    let steps = vec![
        Step {
            replace: None,
            create: Some(create("cache/x")),
            trash: None,
            r#move: None,
            purge: None,
        },
        step_of(None, Some(trash(YAML, 1)), None),
        step_of(None, None, Some(moved(BUNDLE, "content/bundles/v2/z", 1))),
        step_of(Some(replace("lock.yaml", 0)), None, None),
    ];
    let mut cmd = sequence(steps);
    cmd.evidence.builtin = true;
    let events = tree.command(cmd).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(intended.intent.steps.len(), 4);
    assert_eq!(intended.intent.verb, "deps pull");
}

#[test]
fn a_sequences_steps_are_not_judged_on_existence_so_a_retry_can_resume() {
    let mut tree = Tree::baselined();
    tree.observe(observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Deleted, "f0", ""),
            change(YAML, ChangeStatus::Modified, "y0", "y1"),
        ],
        &[(FILE, ""), (YAML, "y1")],
        false,
    ));
    let steps = vec![
        step_of(None, Some(trash(FILE, 1)), None),
        step_of(None, None, Some(moved(FILE, BUNDLE, 1))),
        Step {
            replace: None,
            create: Some(create(BUNDLE)),
            trash: None,
            r#move: None,
            purge: None,
        },
    ];
    assert_eq!(tree.command(sequence(steps)).map(|e| e.len()), Ok(1));
    let stale_trash = sequence(vec![step_of(None, Some(trash(FILE, 0)), None)]);
    assert_eq!(tree.command(stale_trash), Err(stale(FILE, 0, 1)));
    let stale_move = sequence(vec![step_of(None, None, Some(moved(YAML, "x", 0)))]);
    assert_eq!(tree.command(stale_move), Err(stale(YAML, 0, 1)));
}

#[test]
fn a_sequences_evidence_is_judged_once_for_all_its_steps() {
    let mut tree = Tree::baselined();
    let mut cmd = sequence(vec![step_of(Some(replace(FILE, 0)), None, None)]);
    cmd.evidence.plan_rejection = Some(PlanRejected {
        code: "deps-application-x".to_string(),
        args: BTreeMap::new(),
    });
    assert!(matches!(
        tree.command(cmd),
        Err(LibraryTreeRejection::PlanRejected { .. })
    ));
}

// endregion

// region steps

/// A tree with one intent of `steps` replaces pending: its request id.
fn with_intent(tree: &mut Tree, steps: usize) -> String {
    let all = (0..steps)
        .map(|_| step_of(Some(replace(FILE, 0)), None, None))
        .collect();
    let events = tree.command(sequence(all)).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    intended.intent.request_id.clone()
}

fn done(request: &str, step: u32, observed: &[(&str, &str)]) -> RecordStepDone {
    RecordStepDone {
        request_id: request.to_string(),
        step,
        observed: dirty(observed),
    }
}

#[test]
fn a_step_done_ties_what_it_left_to_its_events_sequence() {
    let mut tree = Tree::baselined();
    let request = with_intent(&mut tree, 2);
    let events = tree.fact(
        done(&request, 0, &[(FILE, "new")]),
        &format!("done:{request}:0"),
    );
    let [LibraryEvent::StepApplied(applied)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(applied.step, 0);
    let library = tree.library();
    assert_eq!(library.resources.get(FILE), Some(&resource(2, "new")));
    assert_eq!(
        library.token(FILE),
        2,
        "the new etag is the step's sequence"
    );
    assert_eq!(library.pending.get(&request).map(|i| i.done), Some(1));
    tree.fact(
        done(&request, 1, &[(FILE, "newer")]),
        &format!("done:{request}:1"),
    );
    assert!(tree.library().pending.is_empty());
}

#[test]
fn a_step_not_the_next_or_of_no_intent_records_nothing() {
    let mut tree = Tree::baselined();
    let request = with_intent(&mut tree, 2);
    assert!(tree.fact(done(&request, 1, &[]), "done:skip").is_empty());
    assert!(tree
        .fact(done("r-unknown", 0, &[]), "done:unknown")
        .is_empty());
    tree.fact(done(&request, 0, &[]), "done:0");
    assert!(tree.fact(done(&request, 0, &[]), "done:0-again").is_empty());
}

#[test]
fn a_failed_step_ends_its_intent_once() {
    let mut tree = Tree::baselined();
    let request = with_intent(&mut tree, 1);
    let failed = RecordStepFailed {
        request_id: request.clone(),
        step: 0,
        ext_err_txt: "permission denied".to_string(),
    };
    let events = tree.fact(failed.clone(), "failed:0");
    let [LibraryEvent::ChangeFailed(change)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(change.ext_err_txt, "permission denied");
    assert!(tree.library().pending.is_empty());
    assert!(tree.fact(failed, "failed:0-again").is_empty());
}

// endregion

// region attribution (step 10b U3)

/// A tree with one intent: `step`, alone, pending; its request id.
fn intending(tree: &mut Tree, step: Step) -> String {
    let events = tree.command(sequence(vec![step])).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    intended.intent.request_id.clone()
}

fn creating(path: &str, entries: &[(&str, &str)]) -> Step {
    Step {
        replace: None,
        create: Some(CreateDirectory {
            entries: dirty(entries),
            ..create(path)
        }),
        trash: None,
        r#move: None,
        purge: None,
    }
}

#[test]
fn a_step_done_makes_what_it_left_the_records_dirty_set() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(Some(replace(FILE, 0)), None, None));
    tree.fact(done(&request, 0, &[(FILE, "new")]), "done:0");
    assert_eq!(
        tree.library().dirty,
        BTreeMap::from([(FILE.to_string(), "new".to_string())])
    );
}

#[test]
fn an_observation_of_exactly_a_pending_steps_ids_is_that_step_done() {
    let mut tree = Tree::baselined();
    let made = "content/bundles/v2/notes";
    let yaml = "content/bundles/v2/notes/bundle.yaml";
    let request = intending(&mut tree, creating(made, &[("bundle.yaml", "y1")]));
    let events = tree.observe(observation(
        "h-0",
        "h-0",
        vec![change(yaml, ChangeStatus::Added, "", "y1")],
        &[(yaml, "y1")],
        false,
    ));
    let [LibraryEvent::StepApplied(applied)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(applied.request_id, request);
    assert_eq!(applied.step, 0);
    assert_eq!(applied.observed, dirty(&[(yaml, "y1")]));
    let library = tree.library();
    assert!(library.pending.is_empty());
    assert_eq!(library.resources.get(yaml), Some(&resource(2, "y1")));
}

#[test]
fn an_observation_of_a_steps_ids_and_more_is_recorded_as_the_intents() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(Some(replace(FILE, 0)), None, None));
    let events = tree.observe(observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Modified, "f0", "new"),
            change(YAML, ChangeStatus::Modified, "y0", "y1"),
        ],
        &[(FILE, "new"), (YAML, "y1")],
        false,
    ));
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(observed.intended.as_deref(), Some(request.as_str()));
    assert!(observed.external.is_none());
    assert!(
        tree.library().pending.is_empty(),
        "the intent's step is done"
    );
}

// Changed by step 10c U13 (protos.md §1.3): an outside change at a pending
// intent's path, whose ids are not the intent's, supersedes the intent.
#[test]
fn an_observation_of_other_ids_at_a_pending_path_supersedes_the_intent() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(Some(replace(FILE, 0)), None, None));
    let last = tree.library().last_cause;
    let key = observation_key(TREE, &last, "h-0", &dirty(&[(FILE, "by-hand")]));
    let events = tree.observe(edit(FILE, "by-hand"));
    let [LibraryEvent::TreeObserved(observed), LibraryEvent::IntentSuperseded(superseded)] =
        &events[..]
    else {
        panic!("{events:?}")
    };
    assert_eq!(observed.intended, None);
    assert_eq!(superseded.request_id, request);
    assert_eq!(superseded.by_cause, key);
    assert!(tree.library().pending.is_empty(), "no longer pending");
}

#[test]
fn an_outside_change_of_another_path_leaves_a_pending_intent_pending() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(Some(replace(FILE, 0)), None, None));
    let events = tree.observe(edit(YAML, "by-hand"));
    let [LibraryEvent::TreeObserved(_)] = &events[..] else {
        panic!("{events:?}")
    };
    assert!(tree.library().pending.contains_key(&request));
}

#[test]
fn an_outside_edit_under_a_path_a_pending_trash_takes_supersedes_it() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(None, Some(trash(BUNDLE, 0)), None));
    let events = tree.observe(edit(FILE, "by-hand"));
    let [LibraryEvent::TreeObserved(_), LibraryEvent::IntentSuperseded(superseded)] = &events[..]
    else {
        panic!("{events:?}")
    };
    assert_eq!(superseded.request_id, request);
}

#[test]
fn a_file_made_by_hand_where_a_pending_creation_creates_supersedes_it() {
    let mut tree = Tree::baselined();
    let made = "content/bundles/v2/notes";
    let other = "content/bundles/v2/notes/fragments/x.md";
    let request = intending(&mut tree, creating(made, &[("bundle.yaml", "y1")]));
    let events = tree.observe(observation(
        "h-0",
        "h-0",
        vec![change(other, ChangeStatus::Added, "", "x1")],
        &[(other, "x1")],
        false,
    ));
    let [LibraryEvent::TreeObserved(_), LibraryEvent::IntentSuperseded(superseded)] = &events[..]
    else {
        panic!("{events:?}")
    };
    assert_eq!(superseded.request_id, request);
}

#[test]
fn a_trash_step_is_seen_done_when_everything_under_its_path_is_gone() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(None, Some(trash(BUNDLE, 0)), None));
    let events = tree.observe(observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Deleted, "f0", ""),
            change(YAML, ChangeStatus::Deleted, "y0", ""),
        ],
        &[(FILE, ""), (YAML, "")],
        false,
    ));
    let [LibraryEvent::StepApplied(applied)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(applied.request_id, request);
    assert_eq!(applied.observed, dirty(&[(FILE, ""), (YAML, "")]));
}

// endregion

#[test]
fn a_pending_steps_ids_seen_past_a_moved_head_are_the_outsides() {
    let mut tree = Tree::baselined();
    let request = intending(&mut tree, step_of(Some(replace(FILE, 0)), None, None));
    let events = tree.observe(observation(
        "h-0",
        "h-1",
        vec![change(FILE, ChangeStatus::Modified, "f0", "new")],
        &[],
        false,
    ));
    let [LibraryEvent::TreeObserved(observed)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(observed.intended, None);
    assert!(tree.library().pending.contains_key(&request));
}

#[test]
fn a_replace_of_a_path_holding_several_files_is_one_change_per_call() {
    let mut tree = Tree::baselined();
    tree.observe(observation(
        "h-0",
        "h-0",
        vec![
            change(FILE, ChangeStatus::Modified, "f0", "f1"),
            change(YAML, ChangeStatus::Modified, "y0", "y1"),
        ],
        &[(FILE, "f1"), (YAML, "y1")],
        false,
    ));
    assert_eq!(
        tree.command(replace(BUNDLE, 1)),
        Err(LibraryTreeRejection::OneChangePerCall {
            paths: vec![
                "bundle.yaml".to_string(),
                "fragments/example.md".to_string()
            ]
        })
    );
    assert!(tree.command(replace(FILE, 1)).is_ok());
}

// region the trash (step 10b U8, G12)

/// The one trash of `path`, recorded and done: its request id and the
/// seconds its intent was recorded at.
fn trashed(tree: &mut Tree, path: &str, keep: bool) -> (String, i64) {
    let events = tree
        .command(TrashPath {
            keep,
            ..trash(path, 0)
        })
        .unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    let at = tree
        .harness
        .pages()
        .last()
        .and_then(|page| page.meta.clone())
        .and_then(|meta| meta.recorded_at)
        .map(|at| at.seconds)
        .unwrap();
    let request = intended.intent.request_id.clone();
    tree.fact(
        done(&request, 0, &[(path, "")]),
        &format!("done:{request}:0"),
    );
    (request, at)
}

fn purge(request: &str, age_days: u32) -> PurgeTrash {
    PurgeTrash {
        name: TREE.to_string(),
        request_id: request.to_string(),
        age_days,
    }
}

// L12, G12: a removal's slot is recorded with the time its intent was
// recorded (decide reads no clock) and whether it is kept; it outlives its
// intent's end, so the daemon can purge it once it is old enough.
#[test]
fn a_removal_s_slot_is_recorded_with_its_time_and_outlives_its_intent() {
    let mut tree = Tree::baselined();
    let (request, at) = trashed(&mut tree, BUNDLE, true);
    let library = tree.library();
    assert!(library.pending.is_empty());
    let slot = library.trashed.get(&request).expect("the slot");
    assert_eq!(slot.paths, vec![BUNDLE.to_string()]);
    assert_eq!(slot.trashed_at, at);
    assert!(slot.kept);
    let wire = library.wire();
    let slot = &wire.trashed[&request];
    assert_eq!(slot.trashed_at.as_ref().map(|t| t.seconds), Some(at));
    assert!(slot.kept);
}

// G12: a slot 7 days old or more is purged by one PurgeTrash, whose one
// step removes the slot, dropped from the state once it is done.
#[test]
fn a_slot_seven_days_old_is_purged_by_its_one_step() {
    let mut tree = Tree::baselined();
    let (request, _) = trashed(&mut tree, BUNDLE, true);
    let events = tree.command(purge(&request, KEEP_DAYS)).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    let step = &intended.intent.steps[0];
    assert_eq!(
        step.purge.as_ref().map(|p| p.request_id.as_str()),
        Some(request.as_str())
    );
    assert!(tree.library().trashed.contains_key(&request), "until done");
    let purger = intended.intent.request_id.clone();
    tree.fact(done(&purger, 0, &[]), &format!("done:{purger}:0"));
    let library = tree.library();
    assert!(library.trashed.is_empty(), "{:?}", library.trashed);
    assert!(library.pending.is_empty());
}

// Nothing is purged early, and a slot not there (purged already) is not
// purged again.
#[test]
fn a_slot_not_due_or_not_there_is_refused() {
    let mut tree = Tree::baselined();
    let (request, _) = trashed(&mut tree, BUNDLE, true);
    assert_eq!(
        tree.command(purge(&request, KEEP_DAYS - 1)),
        Err(LibraryTreeRejection::SlotNotDue {
            request_id: request.clone(),
            age_days: KEEP_DAYS - 1,
        })
    );
    assert_eq!(
        tree.command(purge("0199aaaa-0000-7000-8000-000000000000", 30)),
        Err(LibraryTreeRejection::NoSuchSlot {
            request_id: "0199aaaa-0000-7000-8000-000000000000".to_string(),
        })
    );
    assert!(tree.library().trashed.contains_key(&request));
}

// A trash step that failed moved nothing, so it leaves no slot.
#[test]
fn a_failed_trash_leaves_no_slot() {
    let mut tree = Tree::baselined();
    let events = tree.command(trash(BUNDLE, 0)).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    let request = intended.intent.request_id.clone();
    assert!(tree.library().trashed.contains_key(&request));
    tree.fact(
        RecordStepFailed {
            request_id: request.clone(),
            step: 0,
            ext_err_txt: "busy".to_string(),
        },
        "failed:trash",
    );
    assert!(tree.library().trashed.is_empty());
}

// endregion

// 10b (QUESTIONS 1(a)): a move into ANOTHER tree is judged here on where
// it comes from and the token it saw; where it goes is the other tree's,
// never this record's (its NOREPLACE rename judges it).
#[test]
fn a_move_into_another_tree_does_not_judge_its_target_on_this_record() {
    let mut tree = Tree::baselined();
    tree.observe(edit(YAML, "y1"));
    let mut out = moved(BUNDLE, BUNDLE, 1);
    out.to_tree = "/work/other/.ctxloom".to_string();
    let events = tree.command(out).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    let step = intended.intent.steps[0].r#move.as_ref().unwrap();
    assert_eq!(step.to_tree, "/work/other/.ctxloom");
    // Within this tree the same target is there, and refused.
    assert_eq!(
        tree.command(moved(BUNDLE, BUNDLE, 1)),
        Err(LibraryTreeRejection::ResourceExists {
            path: BUNDLE.to_string()
        })
    );
}

// AIP-155 (owner 2026-11-18, night): an export is recorded on its source
// tree as it was made, so its retry is answered with it; the tree's state
// is otherwise unchanged.
#[test]
fn test_a_bundle_exported_is_recorded_as_made_and_changes_no_resource() {
    let mut tree = Tree::baselined();
    let before = tree.library().resources.clone();
    let exported = RecordBundleExported {
        request_id: "r-1".to_string(),
        bundle: "demo".to_string(),
        source: "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
        destination: "/out/demo".to_string(),
    };
    let events = tree.fact(exported, "export:r-1");
    let [LibraryEvent::BundleExported(event)] = events.as_slice() else {
        panic!("{events:?}")
    };
    assert_eq!(event.request_id, "r-1");
    assert_eq!(event.destination, "/out/demo");
    assert_eq!(
        event.source,
        "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    assert_eq!(tree.library().resources, before);
}

// region foreign writers (step 10c U10, L15, T26)

/// A foreign `ReplaceFile`: another tool's file, project-relative.
fn foreign(path: &str, seen: u64) -> ReplaceFile {
    ReplaceFile {
        foreign: true,
        verb: "materialize".to_string(),
        ..replace(path, seen)
    }
}

fn foreign_file(path: &str, oid: &str) -> crate::ForeignFile {
    crate::ForeignFile {
        path: path.to_string(),
        new_oid: oid.to_string(),
    }
}

#[test]
fn a_foreign_writers_sequence_lists_its_files_in_its_intent_and_judges_no_token_of_theirs() {
    let mut tree = Tree::baselined();
    // A library path of the same name moved on: a foreign step is never
    // judged on a library token (no client is given one).
    tree.observe(edit("CLAUDE.md", "c1"));
    let mut cmd = sequence(vec![
        step_of(
            Some(foreign("/home/a/.ctxloom/records/x.claims.yaml", 0)),
            None,
            None,
        ),
        step_of(Some(foreign("CLAUDE.md", 0)), None, None),
    ]);
    cmd.verb = "materialize".to_string();
    cmd.foreign = vec![
        foreign_file("/home/a/.ctxloom/records/x.claims.yaml", "r1"),
        foreign_file("CLAUDE.md", "c2"),
    ];
    let events = tree.command(cmd.clone()).unwrap();
    let [LibraryEvent::ChangeIntended(intended)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(intended.intent.foreign, cmd.foreign);
    assert_eq!(intended.intent.verb, "materialize");
    assert_eq!(intended.intent.steps.len(), 2);
}

#[test]
fn a_foreign_file_outside_a_foreign_writers_sequence_is_a_plan_rejection() {
    let mut tree = Tree::baselined();
    let rejected = |code: &str| {
        Err(LibraryTreeRejection::PlanRejected {
            code: code.to_string(),
            args: BTreeMap::new(),
        })
    };
    // Alone, as a command of its own.
    assert_eq!(
        tree.command(foreign("CLAUDE.md", 0)),
        rejected(crate::FOREIGN_OUTSIDE_WRITER)
    );
    // In the sequence of a verb that writes no other tool's files.
    let mut cmd = sequence(vec![step_of(Some(foreign("CLAUDE.md", 0)), None, None)]);
    cmd.verb = "bundle import".to_string();
    assert_eq!(tree.command(cmd), rejected(crate::FOREIGN_OUTSIDE_WRITER));
    // Each foreign writer's verb takes one.
    for verb in crate::FOREIGN_WRITERS {
        let mut cmd = sequence(vec![step_of(Some(foreign("CLAUDE.md", 0)), None, None)]);
        cmd.verb = (*verb).to_string();
        assert_eq!(tree.command(cmd).map(|e| e.len()), Ok(1), "{verb}");
    }
    assert_eq!(tree.event_count(), 1 + crate::FOREIGN_WRITERS.len());
}

#[test]
fn a_foreign_steps_done_is_folded_apart_from_the_library_and_moves_no_token() {
    let mut tree = Tree::baselined();
    let mut cmd = sequence(vec![
        step_of(Some(foreign("CLAUDE.md", 0)), None, None),
        step_of(Some(replace("lock.yaml", 0)), None, None),
    ]);
    cmd.verb = "materialize".to_string();
    cmd.foreign = vec![foreign_file("CLAUDE.md", "c2")];
    let [LibraryEvent::ChangeIntended(intended)] = &tree.command(cmd).unwrap()[..] else {
        panic!("no intent")
    };
    let request = intended.intent.request_id.clone();
    let events = tree.fact(done(&request, 0, &[("CLAUDE.md", "c2")]), "done:0");
    assert_eq!(events.len(), 1, "{events:?}");
    let library = tree.library();
    assert_eq!(library.foreign.get("CLAUDE.md"), Some(&resource(2, "c2")));
    assert_eq!(library.resources.get("CLAUDE.md"), None);
    assert_eq!(library.dirty.get("CLAUDE.md"), None);
    assert_eq!(library.token("CLAUDE.md"), 0);
    // The library step after it is the library's as ever.
    tree.fact(done(&request, 1, &[("lock.yaml", "l1")]), "done:1");
    let library = tree.library();
    assert_eq!(library.resources.get("lock.yaml"), Some(&resource(3, "l1")));
    assert_eq!(library.foreign.get("lock.yaml"), None);
    assert!(library.pending.is_empty());
    assert_eq!(
        library
            .wire()
            .foreign
            .get("CLAUDE.md")
            .map(|r| (r.sequence, r.oid.as_str())),
        Some((2, "c2"))
    );
}

// region install facts (step 10c U4, L9, protos.md §1.2)

const CACHE_TREE: &str = "cache/bundles/example.com/r/demo/abc123";

fn installed(request: &str, step: u32) -> crate::RecordTreeInstalled {
    crate::RecordTreeInstalled {
        request_id: request.to_string(),
        step,
        identity: "ctxloom+file:///r//bundles/demo".to_string(),
        sha: "abc123".to_string(),
        tree: "t-demo".to_string(),
    }
}

/// A pull's intent: the tree's new directory, then the lock.
fn pulled(tree: &mut Tree) -> String {
    let steps = vec![
        Step {
            replace: None,
            create: Some(create(CACHE_TREE)),
            trash: None,
            r#move: None,
            purge: None,
        },
        step_of(Some(replace("lock.yaml", 0)), None, None),
    ];
    let [LibraryEvent::ChangeIntended(intended)] = &tree.command(sequence(steps)).unwrap()[..]
    else {
        panic!("no intent")
    };
    intended.intent.request_id.clone()
}

#[test]
fn a_tree_installed_completes_its_create_and_ties_its_cache_path_to_its_sequence() {
    let mut tree = Tree::baselined();
    let request = pulled(&mut tree);
    let events = tree.fact(installed(&request, 0), "installed:0");
    let [LibraryEvent::TreeInstalled(got)] = &events[..] else {
        panic!("{events:?}")
    };
    assert_eq!(
        (got.step, got.sha.as_str(), got.tree.as_str()),
        (0, "abc123", "t-demo")
    );
    assert_eq!(got.identity, "ctxloom+file:///r//bundles/demo");
    let library = tree.library();
    assert_eq!(
        library.resources.get(CACHE_TREE),
        Some(&resource(2, "t-demo"))
    );
    assert_eq!(library.pending.get(&request).map(|i| i.done), Some(1));
    // The same fact again records nothing; a step not next records nothing.
    assert!(tree
        .fact(installed(&request, 0), "installed:0-again")
        .is_empty());
    assert!(tree.fact(installed(&request, 1), "installed:1").is_empty());
    assert!(tree
        .fact(installed("nobody", 0), "installed:nobody")
        .is_empty());
    // The lock, the sequence's last step, completes it.
    tree.fact(done(&request, 1, &[("lock.yaml", "l1")]), "done:1");
    assert!(tree.library().pending.is_empty());
}
