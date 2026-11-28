//! The observation over the real library aggregate and its checkouts
//! projected inline, on an in-memory store: what a read records, and what
//! it refuses before anything is named or recorded.

use std::sync::{Arc, Mutex};

use angzarr_embedded::testing::{MemoryStore, Samples};
use angzarr_embedded::v1::{CommandBook, Cover, EventBook, EventPage};
use angzarr_embedded::{
    Answered, Failure, Handled, Kernel, OnRetryable, Registry, Rejected, Sampled, StoreError,
    StreamRef,
};
use library_checkout::LibraryCheckouts;
use library_tree::{LibraryTreeRejection, TreeName, LIBRARY_DOMAIN};
use run_ports::{Edge, Unserved};
use sessions::ProjectName;

use super::{CheckoutHarps, Observations, ReadError, Site};
use crate::fakes::Tree;

const BUNDLE: &str = "content/bundles/v2/demo/bundle.yaml";
const HARP: &str = "brisk-copper-moth";

/// The kernel with the library aggregate and its checkouts, over memory.
struct World {
    registry: Registry,
    store: MemoryStore,
    samples: Mutex<Samples>,
    /// How many books it was handed.
    handled: Mutex<u32>,
    /// Whether it refuses every book (a store that has failed).
    refusing: std::sync::atomic::AtomicBool,
}

impl World {
    fn new(checkouts: &LibraryCheckouts) -> Arc<World> {
        let mut registry = Registry::new();
        registry
            .aggregate::<library_tree::LibraryAggregate>(library_tree::LibraryAggregate::default)
            .unwrap();
        registry
            .projector_handler(&library_checkout::SOURCES, checkouts.clone())
            .unwrap();
        Arc::new(World {
            registry,
            store: MemoryStore::default(),
            samples: Mutex::new(Samples::starting_at(1_000)),
            handled: Mutex::new(0),
            refusing: std::sync::atomic::AtomicBool::new(false),
        })
    }
}

impl Edge for World {
    async fn serve(&self, book: CommandBook, _: run_ports::Arrival) -> Result<Answered, Unserved> {
        self.handle(book).await.map_err(Unserved::Failed)
    }

    #[allow(
        clippy::disallowed_methods,
        reason = "a test kernel or store of its own"
    )]
    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        *self.handled.lock().unwrap() += 1;
        if self.refusing.load(std::sync::atomic::Ordering::SeqCst) {
            return Err(Failure::Kernel(angzarr_embedded::KernelError::Malformed(
                angzarr_embedded::Malformed::UnknownDomain {
                    domain: "stand-in".to_string(),
                },
            )));
        }
        let sampled: Sampled = self.samples.lock().unwrap().next_sample();
        let kernel = Kernel::new(&self.registry).on_retryable(OnRetryable {
            max_retries: 0,
            ..OnRetryable::default()
        });
        let cover = book.cover.clone();
        let handled: Handled = kernel.handle_any(&self.store, book, &sampled).await?;
        let events = handled
            .books()
            .into_iter()
            .find(|book| book.cover == cover)
            .unwrap_or(EventBook {
                cover,
                ..EventBook::default()
            });
        Ok(Answered {
            events: Some(events),
            delivery: handled.delivery(),
        })
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        let stream = StreamRef::try_from(cover)?;
        let slice =
            angzarr_embedded::angzarr_ports::EventStore::read(&self.store, stream, from, None)
                .await?;
        Ok(slice.pages)
    }
}

/// A harp minted once, counted.
#[derive(Default)]
struct Harps(Mutex<u32>);

impl CheckoutHarps for Harps {
    fn harp(&self, _tree: &str) -> Result<String, ctxloom_ports::Failure> {
        *self.0.lock().unwrap() += 1;
        Ok(HARP.to_string())
    }

    fn fresh(&self) -> String {
        HARP.to_string()
    }
}

fn site() -> Site {
    Site::Checkout {
        project: ProjectName::new("calm-otter").unwrap(),
        root: "/work/demo".to_string(),
        tree: "/work/demo/.ctxloom".to_string(),
    }
}

fn tree() -> TreeName {
    TreeName::checkout(ProjectName::new("calm-otter").unwrap(), HARP).unwrap()
}

struct Under {
    world: Arc<World>,
    checkouts: LibraryCheckouts,
    harps: Arc<Harps>,
    observations: Observations<World>,
}

fn under() -> Under {
    let checkouts = LibraryCheckouts::default();
    let world = World::new(&checkouts);
    let harps = Arc::new(Harps::default());
    let observations = Observations::new(
        Arc::clone(&world),
        checkouts.clone(),
        Arc::clone(&harps) as Arc<dyn CheckoutHarps>,
    );
    Under {
        world,
        checkouts,
        harps,
        observations,
    }
}

/// The tree's events, as its stream holds them.
async fn events(world: &World) -> Vec<EventPage> {
    let cover = Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(tree().root()),
        edition: None,
    };
    world.events(&cover, 0).await.unwrap()
}

#[tokio::test(flavor = "current_thread")]
async fn a_first_read_records_the_baseline_and_a_read_that_sees_nothing_new_records_nothing() {
    let under = under();
    let git = Tree::default();
    let observed = under.observations.observe(&git, &site()).await.unwrap();
    assert_eq!(observed.tree, tree());
    assert!(observed.notices.is_empty());
    under.observations.observe(&git, &site()).await.unwrap();
    assert_eq!(events(&under.world).await.len(), 1);
    assert_eq!(*under.world.handled.lock().unwrap(), 1);
    let held = under
        .checkouts
        .checkouts()
        .tree(&tree().root())
        .cloned()
        .unwrap();
    assert!(held.baselined);
    // Seen by the scanner (step 10c U13 rework): no commit is recorded.
    assert_eq!((held.head.as_str(), held.tree.as_str()), ("", ""));
}

#[tokio::test(flavor = "current_thread")]
async fn a_change_is_recorded_once_by_the_read_that_sees_it() {
    let under = under();
    under
        .observations
        .observe(&Tree::default(), &site())
        .await
        .unwrap();
    let edited = Tree::default().file(BUNDLE, "b2");
    under.observations.observe(&edited, &site()).await.unwrap();
    under.observations.observe(&edited, &site()).await.unwrap();
    assert_eq!(events(&under.world).await.len(), 2);
    let held = under
        .checkouts
        .checkouts()
        .tree(&tree().root())
        .cloned()
        .unwrap();
    assert_eq!(held.dirty.get(BUNDLE).map(String::as_str), Some("b2"));
    assert_eq!(held.token(BUNDLE), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn two_reads_that_see_one_change_against_one_state_record_it_once() {
    // Forced: both sights are made against the state before either is
    // recorded, as two reads at once make them.
    let under = under();
    under
        .observations
        .observe(&Tree::default(), &site())
        .await
        .unwrap();
    let held = under
        .checkouts
        .checkouts()
        .tree(&tree().root())
        .cloned()
        .unwrap();
    let listing = library_tree::RecordTreeListed {
        name: tree().to_string(),
        kind: library_tree::TreeKind::Checkout,
        base_cause: held.last_cause.clone(),
        files: vec![library_tree::DirtyPath {
            path: BUNDLE.to_string(),
            oid: "b2".to_string(),
        }],
        contents: Vec::new(),
    };
    for _ in 0..2 {
        under
            .world
            .handle(library_tree::listing_book(&tree(), &listing))
            .await
            .unwrap();
    }
    assert_eq!(events(&under.world).await.len(), 2);
}

// The library is git-free (owner 2026-11-18, superseding G1): a checkout
// is named and observed wherever it is; nothing asks git.
#[tokio::test(flavor = "current_thread")]
async fn a_read_names_its_checkout_and_records_its_baseline() {
    let under = under();
    let git = Tree::default().file("config.yaml", "c0");
    under.observations.observe(&git, &site()).await.unwrap();
    assert_eq!(*under.harps.0.lock().unwrap(), 1);
    assert_eq!(events(&under.world).await.len(), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn a_scan_failing_fails_the_read() {
    struct Broken;
    impl ctxloom_ports::LibraryScanner for Broken {
        fn scan(
            &self,
            _: &str,
        ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
            Err(Box::new(LibraryTreeRejection::NoSuchResource {
                path: "stand-in".to_string(),
            }))
        }
        fn content_ids(
            &self,
            _: &str,
            _: &[&str],
        ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
            unreachable!("not asked")
        }
    }
    let under = under();
    let failed = under
        .observations
        .observe(&Broken, &site())
        .await
        .unwrap_err();
    assert!(matches!(failed, ReadError::Failed(_)));
    assert_eq!(*under.world.handled.lock().unwrap(), 0);
}

/// The trees a read sees: a fresh table each read, each scanner handed out
/// counted.
struct Callers {
    git: Box<dyn Fn() -> Tree + Send + Sync>,
    home: Option<String>,
    scans: Arc<Mutex<usize>>,
}

impl super::Observers for Callers {
    fn scanner(&self) -> Arc<dyn ctxloom_ports::LibraryScanner> {
        Arc::new(Counted {
            tree: (self.git)(),
            scans: Arc::clone(&self.scans),
        })
    }

    fn home_tree(&self) -> Option<String> {
        self.home.clone()
    }
}

/// A scanner counting each scan into its callers' count.
struct Counted {
    tree: Tree,
    scans: Arc<Mutex<usize>>,
}

impl ctxloom_ports::LibraryScanner for Counted {
    fn scan(
        &self,
        dir: &str,
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        *self.scans.lock().unwrap() += 1;
        self.tree.scan(dir)
    }

    fn content_ids(
        &self,
        dir: &str,
        paths: &[&str],
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        self.tree.content_ids(dir, paths)
    }
}

fn callers(git: impl Fn() -> Tree + Send + Sync + 'static, home: Option<&str>) -> Callers {
    Callers {
        git: Box::new(git),
        home: home.map(str::to_string),
        scans: Arc::new(Mutex::new(0)),
    }
}

fn home_events_root() -> angzarr_embedded::v1::Uuid {
    TreeName::home(HARP).unwrap().root()
}

#[tokio::test(flavor = "current_thread")]
async fn a_read_observes_its_checkout_then_the_home() {
    let under = under();
    let callers = callers(Tree::default, Some("/h/.ctxloom"));
    let project = ProjectName::new("calm-otter").unwrap();
    let notices = under
        .observations
        .observe_read(&callers, Some((project, "/work/demo")))
        .await
        .unwrap();
    assert!(notices.is_empty());
    assert_eq!(
        *callers.scans.lock().unwrap(),
        2,
        "the checkout, then the home"
    );
    assert_eq!(events(&under.world).await.len(), 1);
    let home = Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(home_events_root()),
        edition: None,
    };
    assert_eq!(under.world.events(&home, 0).await.unwrap().len(), 1);
}

// The library is git-free (owner 2026-11-18, superseding G2): a home's
// library is observed whenever it has one, never made a repository, and
// nothing is said.
#[tokio::test(flavor = "current_thread")]
async fn a_home_s_library_is_observed_as_it_is_and_nothing_said() {
    let under = under();
    let callers = callers(
        || Tree::default().file("config.yaml", "c0"),
        Some("/h/.ctxloom"),
    );
    let said = under
        .observations
        .observe_read(&callers, None)
        .await
        .unwrap();
    assert!(said.is_empty(), "{said:?}");
    let home = Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(home_events_root()),
        edition: None,
    };
    assert_eq!(under.world.events(&home, 0).await.unwrap().len(), 1);
}

// The library is git-free: a checkout in no work tree and the home beside
// it are both observed.
#[tokio::test(flavor = "current_thread")]
async fn a_checkout_outside_git_and_the_home_are_both_observed() {
    let under = under();
    let callers = callers(
        || Tree::default().file("config.yaml", "c0"),
        Some("/h/.ctxloom"),
    );
    let project = ProjectName::new("calm-otter").unwrap();
    under
        .observations
        .observe_read(&callers, Some((project, "/work/demo")))
        .await
        .unwrap();
    assert_eq!(events(&under.world).await.len(), 1);
    let home = Cover {
        domain: LIBRARY_DOMAIN.to_string(),
        root: Some(home_events_root()),
        edition: None,
    };
    assert_eq!(under.world.events(&home, 0).await.unwrap().len(), 1);
}

#[test]
fn a_checkouts_library_is_its_ctxloom() {
    let project = ProjectName::new("calm-otter").unwrap();
    assert_eq!(
        Site::checkout(project.clone(), "/work/demo"),
        Site::Checkout {
            project,
            root: "/work/demo".to_string(),
            tree: "/work/demo/.ctxloom".to_string(),
        }
    );
}

/// The operations port, counting the sweeps of each tree.
#[derive(Default)]
struct Sweeps(Mutex<Vec<String>>);

impl ctxloom_ports::Operations for Sweeps {
    fn operate(
        &self,
        _: &str,
        _: &str,
        _: &ctxloom_ports::Operation,
        _: &dyn Fn(ctxloom_ports::Point),
    ) -> Result<ctxloom_ports::Operated, ctxloom_ports::Failure> {
        unreachable!("an observation operates nothing")
    }

    fn sweep_staging(&self, tree: &str) -> Result<(), ctxloom_ports::Failure> {
        self.0.lock().unwrap().push(tree.to_string());
        Ok(())
    }

    fn purge_slot(&self, _: &str, _: &str) -> Result<(), ctxloom_ports::Failure> {
        unreachable!("an observation purges nothing")
    }
}

// S50: what a killed write left staged is swept when a daemon starts, as
// the daemon first observes the tree (a read or a write), and never again
// in that daemon's life, so a write it stages later is never swept from
// under it. The trash is not swept (the port's own test).
#[tokio::test(flavor = "current_thread")]
async fn a_tree_s_staging_is_swept_at_its_first_observation_alone() {
    let checkouts = LibraryCheckouts::default();
    let world = World::new(&checkouts);
    let sweeps = Arc::new(Sweeps::default());
    let observations = Observations::new(
        Arc::clone(&world),
        checkouts,
        Arc::new(Harps::default()) as Arc<dyn CheckoutHarps>,
    )
    .sweeping(Arc::clone(&sweeps) as Arc<dyn ctxloom_ports::Operations>);
    let git = Tree::default();
    observations.observe(&git, &site()).await.unwrap();
    observations.observe(&git, &site()).await.unwrap();
    let home = Site::Home {
        tree: "/home/alice/.ctxloom".to_string(),
    };
    observations.observe(&git, &home).await.unwrap();
    assert_eq!(
        *sweeps.0.lock().unwrap(),
        vec!["/work/demo/.ctxloom", "/home/alice/.ctxloom"]
    );
}

/// The operations port, recording each slot purged.
#[derive(Default)]
struct Purges(Mutex<Vec<(String, String)>>);

impl ctxloom_ports::Operations for Purges {
    fn operate(
        &self,
        _: &str,
        _: &str,
        _: &ctxloom_ports::Operation,
        _: &dyn Fn(ctxloom_ports::Point),
    ) -> Result<ctxloom_ports::Operated, ctxloom_ports::Failure> {
        unreachable!("an observation operates nothing")
    }

    fn sweep_staging(&self, _: &str) -> Result<(), ctxloom_ports::Failure> {
        Ok(())
    }

    fn purge_slot(&self, tree: &str, request_id: &str) -> Result<(), ctxloom_ports::Failure> {
        self.0
            .lock()
            .unwrap()
            .push((tree.to_string(), request_id.to_string()));
        Ok(())
    }
}

const REMOVAL: &str = "01a1ecdd-5f31-75fa-8782-789facb7078e";

/// The observations of a tree baselined, then holding one kept trash slot
/// of `REMOVAL` (its intent recorded and done), their clock `days` after
/// the slot's time; and what they purged.
async fn with_a_slot(
    days: i64,
) -> (
    Observations<World>,
    Arc<Purges>,
    Arc<World>,
    LibraryCheckouts,
) {
    let checkouts = LibraryCheckouts::default();
    let world = World::new(&checkouts);
    let plain = Observations::new(
        Arc::clone(&world),
        checkouts.clone(),
        Arc::new(Harps::default()) as Arc<dyn CheckoutHarps>,
    );
    let git = Tree::default();
    plain.observe(&git, &site()).await.unwrap();
    let trash = crate::writes::Intended::Trash(library_tree::TrashPath {
        name: tree().to_string(),
        verb: "bundle remove".to_string(),
        path: "content/bundles/v2/demo".to_string(),
        seen_sequence: 0,
        evidence: library_tree::Evidence {
            tree_reads: true,
            plan_rejection: None,
            builtin: false,
        },
        keep: true,
    });
    let id = uuid::Uuid::parse_str(REMOVAL).unwrap().into_bytes();
    world
        .handle(crate::writes::command_book(&tree(), &trash, id))
        .await
        .unwrap();
    let done = library_tree::RecordStepDone {
        request_id: REMOVAL.to_string(),
        step: 0,
        observed: Vec::new(),
    };
    world
        .handle(crate::writes::step_book(&tree(), &done, "done:r:0"))
        .await
        .unwrap();
    let slot = plain.held(&tree()).trashed[REMOVAL].clone();
    let purges = Arc::new(Purges::default());
    let now = slot.trashed_at + days * 86_400;
    let observations = Observations::new(
        Arc::clone(&world),
        checkouts.clone(),
        Arc::new(Harps::default()) as Arc<dyn CheckoutHarps>,
    )
    .sweeping(Arc::clone(&purges) as Arc<dyn ctxloom_ports::Operations>)
    .purging(Arc::new(move || now));
    (observations, purges, world, checkouts)
}

// G12, L12 (QUESTIONS 4): a kept slot 7 days old or more is purged when the
// daemon next observes its tree (a read's, a write's, a start's): the
// domain's PurgeTrash, the slot removed whole, its step recorded done, so
// the slot leaves the state and a later observation purges nothing more.
#[tokio::test(flavor = "current_thread")]
async fn a_kept_slot_seven_days_old_is_purged_at_its_tree_s_next_observation_once() {
    let (observations, purges, _world, _) = with_a_slot(8).await;
    let git = Tree::default();
    observations.observe(&git, &site()).await.unwrap();
    assert_eq!(
        *purges.0.lock().unwrap(),
        vec![("/work/demo/.ctxloom".to_string(), REMOVAL.to_string())]
    );
    assert!(observations.held(&tree()).trashed.is_empty());
    observations.observe(&git, &site()).await.unwrap();
    assert_eq!(purges.0.lock().unwrap().len(), 1, "purged once");
}

// A slot younger than 7 days survives every observation (and a restart:
// nothing but its age decides).
#[tokio::test(flavor = "current_thread")]
async fn a_slot_younger_than_seven_days_is_kept() {
    let (observations, purges, _world, _) = with_a_slot(6).await;
    observations
        .observe(&Tree::default(), &site())
        .await
        .unwrap();
    assert!(purges.0.lock().unwrap().is_empty());
    assert!(observations.held(&tree()).trashed.contains_key(REMOVAL));
}

// Step 10c U13 rework: a scan that finds what the record lists records
// nothing; one of a tree never listed, or one git last saw (it held no
// listing), or one that differs, is recorded.
#[test]
fn test_a_scan_is_as_held_only_against_a_listing_of_the_same_files() {
    let files = std::collections::BTreeMap::from([(BUNDLE.to_string(), "b1".to_string())]);
    let listed = library_tree::Library {
        baselined: true,
        dirty: files.clone(),
        ..library_tree::Library::default()
    };
    assert!(crate::listed_as_held(&listed, &files));
    let other = std::collections::BTreeMap::from([(BUNDLE.to_string(), "b2".to_string())]);
    assert!(!crate::listed_as_held(&listed, &other));
    let never = library_tree::Library {
        baselined: false,
        ..listed.clone()
    };
    assert!(!crate::listed_as_held(&never, &files));
    let by_git = library_tree::Library {
        head: "c1".to_string(),
        ..listed.clone()
    };
    assert!(!crate::listed_as_held(&by_git, &files));
    let by_git_tree = library_tree::Library {
        tree: "t1".to_string(),
        ..listed
    };
    assert!(!crate::listed_as_held(&by_git_tree, &files));
    let mut taken = library_tree::Library {
        baselined: true,
        dirty: files.clone(),
        ..library_tree::Library::default()
    };
    taken.dirty.insert("gone.md".to_string(), String::new());
    assert!(
        crate::listed_as_held(&taken, &files),
        "a path held gone is absent"
    );
}

// Step 10c U13 rework (owner, 2026-11-18): a hook's observation trusts a
// scan of its trees within the trust window by the daemon's clock, and
// scans nothing then; past it, or once `forget` dropped what was seen,
// it scans again. Without a clock every hook scans.
#[tokio::test(flavor = "current_thread")]
async fn test_a_hook_trusts_a_scan_within_its_window_and_forget_drops_it() {
    use std::sync::atomic::{AtomicI64, Ordering};
    let now = Arc::new(AtomicI64::new(1_000));
    let clock = Arc::clone(&now);
    let under = under();
    let observations = Observations::new(
        Arc::clone(&under.world),
        under.checkouts.clone(),
        Arc::clone(&under.harps) as Arc<dyn CheckoutHarps>,
    )
    .purging(Arc::new(move || clock.load(Ordering::SeqCst)));
    let hooked = callers(Tree::default, None);
    let project = || Some((ProjectName::new("calm-otter").unwrap(), "/work/demo"));
    let scans = |callers: &Callers| *callers.scans.lock().unwrap();
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(scans(&hooked), 1, "never scanned: scanned");
    now.store(1_299, Ordering::SeqCst);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(scans(&hooked), 1, "within the window: trusted");
    now.store(1_300, Ordering::SeqCst);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(scans(&hooked), 2, "at the window's end: scanned");
    observations.forget(&hooked);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(scans(&hooked), 3, "forgotten: scanned");
    // A read always scans.
    observations
        .observe_trees(&hooked, project())
        .await
        .unwrap();
    assert_eq!(scans(&hooked), 4);
    // No clock: every hook scans.
    let unclocked = callers(Tree::default, None);
    for _ in 0..2 {
        under
            .observations
            .observe_trees_trusting(&unclocked, project(), 300)
            .await
            .unwrap();
    }
    assert_eq!(scans(&unclocked), 2);
}

// Review 6: a tree counts as scanned only once its listing is recorded. A
// record that failed leaves it unscanned, so the next hook observes it
// again instead of trusting it for the window with nothing recorded.
#[tokio::test(flavor = "current_thread")]
async fn test_a_failed_record_leaves_the_tree_unscanned_for_the_next_hook() {
    use std::sync::atomic::Ordering;
    let under = under();
    let observations = Observations::new(
        Arc::clone(&under.world),
        under.checkouts.clone(),
        Arc::clone(&under.harps) as Arc<dyn CheckoutHarps>,
    )
    .purging(Arc::new(|| 1_000));
    let hooked = callers(|| Tree::default().file("config.yaml", "c0"), None);
    let project = || Some((ProjectName::new("calm-otter").unwrap(), "/work/demo"));
    under.world.refusing.store(true, Ordering::SeqCst);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap_err();
    under.world.refusing.store(false, Ordering::SeqCst);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(
        *hooked.scans.lock().unwrap(),
        2,
        "the failed one was not trusted"
    );
    assert_eq!(
        events(&under.world).await.len(),
        1,
        "and the retry recorded it"
    );
}

// Review 5: a clock set back makes a scan look to be from the future; that
// is never within the window, so the next hook scans (hand edits are seen).
#[tokio::test(flavor = "current_thread")]
async fn test_a_clock_set_back_does_not_keep_the_trust_window_true() {
    use std::sync::atomic::{AtomicI64, Ordering};
    let now = Arc::new(AtomicI64::new(100_000));
    let clock = Arc::clone(&now);
    let under = under();
    let observations = Observations::new(
        Arc::clone(&under.world),
        under.checkouts.clone(),
        Arc::clone(&under.harps) as Arc<dyn CheckoutHarps>,
    )
    .purging(Arc::new(move || clock.load(Ordering::SeqCst)));
    let hooked = callers(Tree::default, None);
    let project = || Some((ProjectName::new("calm-otter").unwrap(), "/work/demo"));
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(*hooked.scans.lock().unwrap(), 1);
    now.store(50_000, Ordering::SeqCst);
    observations
        .observe_trees_trusting(&hooked, project(), 300)
        .await
        .unwrap();
    assert_eq!(
        *hooked.scans.lock().unwrap(),
        2,
        "a scan from the future is not trusted"
    );
}

/// The observations of `with_a_slot`'s kind, their clock a shared cell the
/// test moves: `start` days after the slot's time.
async fn with_a_slot_on_a_moving_clock(
    start_hours: i64,
) -> (
    Observations<World>,
    Arc<Purges>,
    Arc<std::sync::atomic::AtomicI64>,
    i64,
) {
    use std::sync::atomic::{AtomicI64, Ordering};
    let (fixed, purges, world, checkouts) = with_a_slot(0).await;
    let trashed_at = fixed.held(&tree()).trashed[REMOVAL].trashed_at;
    let now = Arc::new(AtomicI64::new(trashed_at + start_hours * 3_600));
    let clock = Arc::clone(&now);
    let observations = Observations::new(
        Arc::clone(&world),
        checkouts,
        Arc::new(Harps::default()) as Arc<dyn CheckoutHarps>,
    )
    .sweeping(Arc::clone(&purges) as Arc<dyn ctxloom_ports::Operations>)
    .purging(Arc::new(move || clock.load(Ordering::SeqCst)));
    (observations, purges, now, trashed_at)
}

// Review 5 (the one that matters: a loss): a clock set FORWARD must not
// purge a kept slot early. A gap between two of the daemon's own checks
// longer than an hour is a jump (or a suspension); the whole gap is taken
// out of every later reading, so the slot is purged only after seven days
// of the daemon's own continuous time.
#[tokio::test(flavor = "current_thread")]
async fn test_a_clock_set_forward_does_not_purge_a_slot_early() {
    use std::sync::atomic::Ordering;
    let (observations, purges, now, trashed_at) = with_a_slot_on_a_moving_clock(24).await;
    let git = Tree::default();
    observations.observe(&git, &site()).await.unwrap();
    assert!(purges.0.lock().unwrap().is_empty(), "one day old");
    // Set forward eight days: by the clock the slot is nine days old.
    now.store(trashed_at + 9 * 86_400, Ordering::SeqCst);
    observations.observe(&git, &site()).await.unwrap();
    assert!(purges.0.lock().unwrap().is_empty(), "a jump purges nothing");
    // The clock then runs on hour by hour: the slot's age is the daemon's
    // own one day plus what has run since, so the seventh day is 144 hours on.
    for _ in 0..143 {
        now.fetch_add(3_600, Ordering::SeqCst);
        observations.observe(&git, &site()).await.unwrap();
    }
    assert!(purges.0.lock().unwrap().is_empty(), "six days and 23 hours");
    now.fetch_add(3_600, Ordering::SeqCst);
    observations.observe(&git, &site()).await.unwrap();
    assert_eq!(purges.0.lock().unwrap().len(), 1, "seven days: purged");
}

// A clock set back purges nothing either, and a clock then restored is no
// jump forward of its own: the slot is not early for it.
#[tokio::test(flavor = "current_thread")]
async fn test_a_clock_set_back_and_restored_does_not_purge_a_slot_early() {
    use std::sync::atomic::Ordering;
    let (observations, purges, now, trashed_at) = with_a_slot_on_a_moving_clock(24).await;
    let git = Tree::default();
    observations.observe(&git, &site()).await.unwrap();
    now.store(trashed_at, Ordering::SeqCst);
    observations.observe(&git, &site()).await.unwrap();
    assert!(purges.0.lock().unwrap().is_empty());
    now.store(trashed_at + 25 * 3_600, Ordering::SeqCst);
    observations.observe(&git, &site()).await.unwrap();
    assert!(purges.0.lock().unwrap().is_empty(), "a day and an hour old");
}

// P3 (owner 2026-11-25: the stream holds the files' bytes): what the scanner
// reads anew rides the listing and is recorded on the observation, once per
// change; an MCP server's file, which may hold a secret, is recorded with
// each secret value sealed, or by its id alone where nothing can seal.
struct Reading {
    tree: Tree,
    bytes: std::collections::BTreeMap<String, Vec<u8>>,
}

impl ctxloom_ports::LibraryScanner for Reading {
    fn scan(
        &self,
        dir: &str,
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        self.tree.scan(dir)
    }

    fn content_ids(
        &self,
        dir: &str,
        paths: &[&str],
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        self.tree.content_ids(dir, paths)
    }

    fn read_files(
        &self,
        _: &str,
        paths: &[(&str, &str)],
    ) -> Result<std::collections::BTreeMap<String, Vec<u8>>, ctxloom_ports::Failure> {
        Ok(paths
            .iter()
            .filter_map(|(path, _)| Some((path.to_string(), self.bytes.get(*path)?.clone())))
            .collect())
    }
}

/// The `(path, bytes)` each observation of the tree recorded, in order.
async fn recorded_contents(world: &World) -> Vec<Vec<(String, Vec<u8>)>> {
    use angzarr_embedded::Payload;
    events(world)
        .await
        .into_iter()
        .filter_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => Some(any),
            _ => None,
        })
        .filter_map(|any| match library_tree::LibraryEvent::from_any(&any) {
            Ok(library_tree::LibraryEvent::TreeObserved(observed)) => Some(
                observed
                    .contents
                    .into_iter()
                    .map(|c| (c.path, c.content))
                    .collect(),
            ),
            _ => None,
        })
        .collect()
}

#[tokio::test(flavor = "current_thread")]
async fn the_bytes_of_a_changed_file_are_recorded_once_and_a_servers_file_is_not() {
    const MCP: &str = "content/bundles/v2/demo/mcp/docs.yaml";
    let under = under();
    let reading = |bundle: &str, id: &str| Reading {
        tree: Tree::default()
            .file(BUNDLE, id)
            .file("config.yaml", "c1")
            .file(MCP, "m1"),
        bytes: [
            (BUNDLE.to_string(), bundle.as_bytes().to_vec()),
            ("config.yaml".to_string(), b"cfg".to_vec()),
            (
                MCP.to_string(),
                b"headers: {Authorization: SECRET}".to_vec(),
            ),
        ]
        .into_iter()
        .collect(),
    };
    under
        .observations
        .observe(&reading("one", "b1"), &site())
        .await
        .unwrap();
    under
        .observations
        .observe(&reading("two", "b2"), &site())
        .await
        .unwrap();
    let recorded = recorded_contents(&under.world).await;
    assert_eq!(
        recorded,
        [
            vec![
                ("config.yaml".to_string(), b"cfg".to_vec()),
                (BUNDLE.to_string(), b"one".to_vec()),
            ],
            vec![(BUNDLE.to_string(), b"two".to_vec())],
        ],
        "the baseline's files but the server's, then the changed file alone"
    );
}

/// A sealing that cannot seal, as a daemon with no key.
struct NoKey;

impl run_ports::Sealing for NoKey {
    fn seal(&self, _: &str, _: &str, _: &[u8]) -> Result<Vec<u8>, run_ports::SealingError> {
        Err(run_ports::SealingError::NoKey {
            project: "calm-otter".to_string(),
            file: ".env".to_string(),
        })
    }

    fn open(&self, _: &str, _: &str, _: &[u8]) -> Result<Vec<u8>, run_ports::SealingError> {
        Err(run_ports::SealingError::NoKey {
            project: "calm-otter".to_string(),
            file: ".env".to_string(),
        })
    }
}

const SERVER: &str = "content/bundles/v2/demo/mcp/docs.yaml";

fn serving() -> Reading {
    Reading {
        tree: Tree::default().file(SERVER, "m1"),
        bytes: [(
            SERVER.to_string(),
            b"url: https://docs.example/mcp\nheaders:\n  Authorization: Bearer SECRET\n".to_vec(),
        )]
        .into_iter()
        .collect(),
    }
}

/// The contents every observation recorded, with their sealed fields.
async fn recorded_files(world: &World) -> Vec<library_tree::FileContent> {
    use angzarr_embedded::Payload;
    events(world)
        .await
        .into_iter()
        .filter_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => Some(any),
            _ => None,
        })
        .filter_map(|any| match library_tree::LibraryEvent::from_any(&any) {
            Ok(library_tree::LibraryEvent::TreeObserved(observed)) => Some(observed.contents),
            _ => None,
        })
        .flatten()
        .collect()
}

#[tokio::test(flavor = "current_thread")]
async fn a_servers_secret_values_are_recorded_sealed_and_never_in_the_clear() {
    let under = under();
    let observations = under.observations.sealing(Arc::new(run_ports::TestSealing));
    observations.observe(&serving(), &site()).await.unwrap();
    let files = recorded_files(&under.world).await;
    let [file] = files.as_slice() else {
        panic!("one file: {files:?}");
    };
    assert_eq!(file.path, SERVER);
    let text = String::from_utf8(file.content.clone()).unwrap();
    assert!(!text.contains("SECRET"), "{text}");
    assert!(text.contains("https://docs.example/mcp"), "{text}");
    let [sealed] = file.sealed_fields.as_slice() else {
        panic!("one sealed field: {:?}", file.sealed_fields);
    };
    assert_eq!(sealed.document_path, "headers.Authorization");
    assert!(
        !String::from_utf8_lossy(&sealed.sealed).contains("SECRET Bearer"),
        "bound and sealed, not stored plain"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn a_server_that_cannot_be_sealed_is_recorded_by_its_id_alone() {
    let under = under();
    let observations = under.observations.sealing(Arc::new(NoKey));
    observations.observe(&serving(), &site()).await.unwrap();
    assert!(recorded_files(&under.world).await.is_empty());
}
