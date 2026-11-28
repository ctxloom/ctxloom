//! A read's first step (step 10a U2): its tree observed, and what changed
//! recorded as the library domain's one fact, before the read opens the
//! library. A tree is observed in any directory, in git or not (the library
//! is git-free, owner 2026-11-18). The checkout is named at its first read
//! (L2), and what the record holds of it is read from the warm
//! `library-checkout` (U0), never folded per read.
//!
//! No lock is held across the recording: two reads that see the same thing
//! against the same state make one fact key, so the second is a DUPLICATE
//! (L4), and one made against a state another read has moved since records
//! only what it adds (the aggregate's rule). The kernel projects the
//! checkouts inline, so a read after another's recording reconciles against
//! it.

use std::collections::BTreeSet;
use std::sync::{Arc, Mutex};

use ctxloom_ports::{Failure, LibraryScanner, Operations};
use library_checkout::LibraryCheckouts;
use library_tree::{listing_book, DirtyPath, RecordTreeListed, TreeName};
use run_ports::Edge;
use sessions::ProjectName;

use crate::LibraryApiNotice;

/// Whether a scan that found `files` is what `held` already lists: a
/// tree listed before (never a git-era one, which held no listing), the
/// same paths at the same content ids (a path a step took away, held with
/// an empty id, absent). Such a scan records nothing.
pub fn listed_as_held(
    held: &library_tree::Library,
    files: &std::collections::BTreeMap<String, String>,
) -> bool {
    held.baselined && held.head.is_empty() && held.tree.is_empty() && held.listed() == *files
}

/// Where a checkout's harp is kept (`<tree>/state/checkout`, out of git),
/// minted at its tree's first read and read back after.
pub trait CheckoutHarps: Send + Sync {
    /// The harp of the checkout whose library is the directory `tree`.
    fn harp(&self, tree: &str) -> Result<String, Failure>;

    /// A fresh harp, minted and written nowhere (step 10c U6: a first
    /// init's checkout and project, staged with its scaffold).
    fn fresh(&self) -> String;
}

/// Whether `path` is an MCP server's file, whose header and environment
/// values may be secrets: its bytes are recorded with each of them sealed.
fn holds_secrets(path: &str) -> bool {
    path.contains("/mcp/") && path.ends_with(".yaml")
}

/// The keys of an MCP server's document whose values are secrets.
const SECRET_MAPS: [&str; 2] = ["env", "headers"];

/// What stands in the content for a value that is sealed.
const SEALED_IN_CONTENT: &str = "[sealed]";

/// `bytes`, an MCP server's file, with each value of its `env` and `headers`
/// replaced by [`SEALED_IN_CONTENT`] and sealed to the daemon's key, bound to
/// its document path. None when the file is no mapping or any value cannot be
/// sealed: it is then recorded by its content id alone, never in the clear.
fn sealed_server(
    bytes: &[u8],
    (sealing, project): (&dyn run_ports::Sealing, &str),
) -> Option<(Vec<u8>, Vec<library_tree::SealedField>)> {
    use angzarr_yaml::Value;
    let Value::Map(entries) = angzarr_yaml::parse(std::str::from_utf8(bytes).ok()?).ok()? else {
        return None;
    };
    let mut sealed = Vec::new();
    let mut out = Vec::new();
    for (key, value) in entries {
        let value = match (SECRET_MAPS.contains(&key.as_str()), value) {
            (true, Value::Map(inner)) => {
                let mut replaced = Vec::new();
                for (name, secret) in inner {
                    let Value::Scalar(plain) = secret else {
                        return None;
                    };
                    let path = format!("{key}.{name}");
                    let bound = sealing.seal(project, &path, plain.as_bytes()).ok()?;
                    sealed.push(library_tree::SealedField {
                        document_path: path,
                        sealed: bound,
                    });
                    replaced.push((name, Value::Scalar(SEALED_IN_CONTENT.to_string())));
                }
                Value::Map(replaced)
            }
            (_, other) => other,
        };
        out.push((key, value));
    }
    Some((
        angzarr_yaml::write(&Value::Map(out), usize::MAX).into_bytes(),
        sealed,
    ))
}

/// The bytes of the files `found` lists anew against `held` (path to content
/// id as the record lists them): each file whose id the record does not hold,
/// read by the scanner, a secret-bearing one left out.
fn contents_of(
    (scanner, sealing, project): (&dyn LibraryScanner, Option<&dyn run_ports::Sealing>, &str),
    dir: &str,
    held: &std::collections::BTreeMap<String, String>,
    found: &std::collections::BTreeMap<String, String>,
) -> Result<Vec<library_tree::FileContent>, ReadError> {
    let anew: Vec<(&str, &str)> = found
        .iter()
        .filter(|(path, id)| {
            held.get(*path) != Some(*id) && (!holds_secrets(path) || sealing.is_some())
        })
        .map(|(path, id)| (path.as_str(), id.as_str()))
        .collect();
    let mut bytes = scanner.read_files(dir, &anew).map_err(ReadError::Failed)?;
    Ok(anew
        .into_iter()
        .filter_map(|(path, id)| {
            let content = bytes.remove(path)?;
            let (content, sealed_fields) = match (holds_secrets(path), sealing) {
                (true, Some(sealing)) => sealed_server(&content, (sealing, project))?,
                _ => (content, Vec::new()),
            };
            Some(library_tree::FileContent {
                path: path.to_string(),
                content_id: id.to_string(),
                content,
                sealed_fields,
            })
        })
        .collect())
}

/// What a read's trees are seen by: the scanner, and the home whose library
/// every read reads beside its project's.
pub trait Observers: Send + Sync {
    /// The scanner every tree is seen through (step 10c U13 rework): no
    /// git, so no environment; its memo shared by every read.
    fn scanner(&self) -> Arc<dyn LibraryScanner>;

    /// The home's library directory (its `.ctxloom`), when it has one.
    fn home_tree(&self) -> Option<String>;
}

/// The tree a read is of.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Site {
    /// A project checkout's library: the project, the checkout's root and
    /// its `.ctxloom` there.
    Checkout {
        project: ProjectName,
        root: String,
        tree: String,
    },
    /// The home's library, its `.ctxloom`.
    Home { tree: String },
}

impl Site {
    /// The library of the checkout of `project` at `root`: its `.ctxloom`.
    pub fn checkout(project: ProjectName, root: &str) -> Site {
        let tree = format!("{}/.ctxloom", root.trim_end_matches(['/', '\\']));
        Site::Checkout {
            project,
            root: root.to_string(),
            tree,
        }
    }

    /// The project whose key seals the tree's secrets: the checkout's, none
    /// for the home's library (which has no `.env`, so it seals nothing).
    pub fn project(&self) -> &str {
        match self {
            Site::Checkout { project, .. } => project.harp(),
            Site::Home { .. } => "",
        }
    }

    /// The tree's directory, its `.ctxloom`.
    pub fn tree(&self) -> &str {
        match self {
            Site::Checkout { tree, .. } | Site::Home { tree } => tree,
        }
    }
}

/// What the observation came to: the tree's name, and what the read is to
/// say beside its answer.
#[derive(Clone, Debug, PartialEq)]
pub struct Observed {
    pub tree: TreeName,
    pub notices: Vec<LibraryApiNotice>,
}

/// What a write's observations came to: each tree observed, with what it
/// came to, and everything they are to say, in order.
#[derive(Clone, Debug, PartialEq)]
pub struct Trees {
    pub seen: Vec<(Site, Observed)>,
    pub notices: Vec<LibraryApiNotice>,
}

/// Why a read was not answered.
#[derive(Debug)]
pub enum ReadError {
    /// The scan or the record failed (exit 32).
    Failed(Failure),
}

/// The observations of the daemon's reads, recorded through its edge.
pub struct Observations<E> {
    edge: Arc<E>,
    checkouts: LibraryCheckouts,
    harps: Arc<dyn CheckoutHarps>,
    /// The atomic operations, whose staging of a tree is swept at the
    /// tree's first observation (S50); none, nothing is swept.
    operations: Option<Arc<dyn Operations>>,
    /// The trees whose staging this daemon has swept.
    swept: Mutex<BTreeSet<String>>,
    /// The daemon's clock, in seconds since the epoch, by which a kept
    /// trash slot's age is told (G12); none, nothing is purged.
    clock: Option<Clock>,
    /// When each tree (its directory) was last scanned, by the clock (step
    /// 10c U13 rework): what a hook's observation trusts.
    scanned: Mutex<std::collections::BTreeMap<String, i64>>,
    /// What the purge has seen of the clock's own progress (review 5).
    ticks: Mutex<Ticks>,
    /// What an MCP server's secret values are sealed with; none, its file is
    /// recorded by its content id alone.
    sealing: Option<Arc<dyn run_ports::Sealing>>,
}

/// The longest stretch between two of the daemon's own purge checks that
/// still counts as the clock running on. The daemon checks at every
/// observation, so a longer one is the clock set forward (or the machine
/// suspended): either way it is not seven days of the slot's keeping.
const MAX_GAP: i64 = 3_600;

/// The clock as the purge has watched it: its last reading, and the
/// stretches between readings too long to be time passing, summed. A slot's
/// age is told by the clock less that sum, so a clock set forward cannot
/// purge a slot early (a loss), while a quiet daemon only purges later (the
/// slot is kept longer, never lost). A clock set
/// back adds nothing and is simply followed. One wall clock cannot tell a
/// jump it was not watching, so a daemon (re)started on a clock already set
/// forward is not protected.
#[derive(Default)]
struct Ticks {
    last: Option<i64>,
    skipped: i64,
}

impl Ticks {
    /// The reading `now` taken into account: `now` less the jumps seen.
    fn steady(&mut self, now: i64) -> i64 {
        if let Some(last) = self.last {
            let gap = now.saturating_sub(last);
            if gap > MAX_GAP {
                self.skipped = self.skipped.saturating_add(gap);
            }
        }
        self.last = Some(now);
        now.saturating_sub(self.skipped)
    }
}

/// The daemon's clock: now, in seconds since the epoch.
pub type Clock = Arc<dyn Fn() -> i64 + Send + Sync>;

impl<E: Edge> Observations<E> {
    /// Recorded through `edge`, reconciled against `checkouts` (the
    /// kernel's, projected inline), each checkout named by `harps`.
    pub fn new(
        edge: Arc<E>,
        checkouts: LibraryCheckouts,
        harps: Arc<dyn CheckoutHarps>,
    ) -> Observations<E> {
        Observations {
            edge,
            checkouts,
            harps,
            operations: None,
            swept: Mutex::new(BTreeSet::new()),
            clock: None,
            scanned: Mutex::new(std::collections::BTreeMap::new()),
            ticks: Mutex::new(Ticks::default()),
            sealing: None,
        }
    }

    /// These observations, an MCP server's secrets sealed by `sealing`.
    #[must_use]
    pub fn sealing(self, sealing: Arc<dyn run_ports::Sealing>) -> Observations<E> {
        Observations {
            sealing: Some(sealing),
            ..self
        }
    }

    /// These observations, each tree's staging swept through `operations`
    /// the first time this daemon observes the tree (step 10b U8, S50).
    #[must_use]
    pub fn sweeping(self, operations: Arc<dyn Operations>) -> Observations<E> {
        Observations {
            operations: Some(operations),
            ..self
        }
    }

    /// These observations, each tree's kept trash slots
    /// [`library_tree::KEEP_DAYS`] old or more by `clock` purged when the
    /// tree is observed (G12, L12).
    #[must_use]
    pub fn purging(self, clock: Clock) -> Observations<E> {
        Observations {
            clock: Some(clock),
            ..self
        }
    }

    /// The operations a write makes its change through, if any.
    pub fn operations(&self) -> Option<&Arc<dyn Operations>> {
        self.operations.as_ref()
    }

    /// `tree`'s staging swept, the first time this daemon observes it: what
    /// a write killed before its rename left there (S50). The trash is
    /// never swept.
    fn sweep_once(&self, tree: &str) -> Result<(), ReadError> {
        let Some(operations) = &self.operations else {
            return Ok(());
        };
        let mut swept = self
            .swept
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if swept.contains(tree) {
            return Ok(());
        }
        operations.sweep_staging(tree).map_err(ReadError::Failed)?;
        swept.insert(tree.to_string());
        Ok(())
    }

    /// The kept trash slots of `tree` (its directory `dir`) at least
    /// [`library_tree::KEEP_DAYS`] old by the daemon's clock, purged (G12,
    /// L12): for each, the domain's `PurgeTrash` (its command id derived
    /// from the tree and the slot, so a second purge of one slot is a
    /// DUPLICATE), the slot removed whole, and its one step recorded done,
    /// which drops it from the state. A younger slot, or a sequence's own,
    /// is left; nothing is purged without a clock.
    async fn purge_due(&self, dir: &str, tree: &TreeName) -> Result<(), ReadError> {
        let (Some(clock), Some(operations)) = (&self.clock, &self.operations) else {
            return Ok(());
        };
        let now = self
            .ticks
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .steady(clock());
        let due: Vec<(String, u32)> = self
            .held(tree)
            .trashed
            .iter()
            .filter(|(_, slot)| slot.kept)
            .map(|(request, slot)| {
                let days = (now - slot.trashed_at).max(0) / 86_400;
                (request.clone(), u32::try_from(days).unwrap_or(u32::MAX))
            })
            .filter(|(_, days)| *days >= library_tree::KEEP_DAYS)
            .collect();
        for (request, age_days) in due {
            let key = format!("purge:{tree}:{request}");
            let id = uuid::Uuid::new_v5(&uuid::Uuid::NAMESPACE_URL, key.as_bytes());
            let purge = library_tree::PurgeTrash {
                name: tree.to_string(),
                request_id: request.clone(),
                age_days,
            };
            self.edge
                .handle(crate::writes::purge_book(tree, purge, id.into_bytes()))
                .await
                .map_err(|failure| ReadError::Failed(Box::new(failure)))?;
            operations
                .purge_slot(dir, &request)
                .map_err(ReadError::Failed)?;
            let purger = id.to_string();
            let done = library_tree::RecordStepDone {
                request_id: purger.clone(),
                step: 0,
                observed: Vec::new(),
            };
            self.edge
                .handle(crate::writes::step_book(
                    tree,
                    &done,
                    &format!("done:{purger}:0"),
                ))
                .await
                .map_err(|failure| ReadError::Failed(Box::new(failure)))?;
        }
        Ok(())
    }

    /// A fresh harp ([`CheckoutHarps::fresh`]), written nowhere.
    pub fn fresh_harp(&self) -> String {
        self.harps.fresh()
    }

    /// The stream `site`'s tree is recorded on, by its harp.
    fn tree_name(&self, site: &Site) -> Result<TreeName, ReadError> {
        let harp = self.harps.harp(site.tree()).map_err(ReadError::Failed)?;
        match site {
            Site::Checkout { project, .. } => TreeName::checkout(project.clone(), &harp),
            Site::Home { .. } => TreeName::home(&harp),
        }
        .map_err(|rejection| ReadError::Failed(Box::new(rejection)))
    }

    /// Observe `site`: its files found by `scanner` (step 10c U13 rework:
    /// no git sees the tree) and, when they differ from what the record
    /// lists, the listing recorded for the aggregate to compare.
    pub async fn observe(
        &self,
        scanner: &dyn LibraryScanner,
        site: &Site,
    ) -> Result<Observed, ReadError> {
        self.sweep_once(site.tree())?;
        let tree = self.tree_name(site)?;
        let held = {
            let checkouts = self.checkouts.checkouts();
            checkouts.tree(&tree.root()).cloned().unwrap_or_default()
        };
        let files = scanner.scan(site.tree()).map_err(ReadError::Failed)?;
        if !listed_as_held(&held, &files) {
            let contents = contents_of(
                (scanner, self.sealing.as_deref(), site.project()),
                site.tree(),
                &held.listed(),
                &files,
            )?;
            let fact = RecordTreeListed {
                name: tree.to_string(),
                kind: tree.kind(),
                base_cause: held.last_cause.clone(),
                files: files
                    .into_iter()
                    .map(|(path, oid)| DirtyPath { path, oid })
                    .collect(),
                contents,
            };
            self.edge
                .handle(listing_book(&tree, &fact))
                .await
                .map_err(|failure| ReadError::Failed(Box::new(failure)))?;
        }
        // Scanned only once the listing is recorded (review 6): a record
        // that failed must not let the next hook trust the tree.
        if let Some(clock) = &self.clock {
            self.scanned
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner)
                .insert(site.tree().to_string(), clock());
        }
        self.purge_due(site.tree(), &tree).await?;
        Ok(Observed {
            tree,
            notices: Vec::new(),
        })
    }
}

impl<E: Edge> Observations<E> {
    /// A read's observations (U2): the project's checkout at `root`, then
    /// the home's library, when it has one. The home is never made a
    /// repository (the library is git-free, owner 2026-11-18, superseding
    /// G2). What both are to say, in order.
    pub async fn observe_read(
        &self,
        observers: &dyn Observers,
        project: Option<(ProjectName, &str)>,
    ) -> Result<Vec<LibraryApiNotice>, ReadError> {
        Ok(self.observe_trees(observers, project).await?.notices)
    }

    /// A write's observations (step 10b U3): [`Observations::observe_read`]'s,
    /// each tree with what it came to, in order: the project's checkout,
    /// then the home's library (when it has one).
    pub async fn observe_trees(
        &self,
        observers: &dyn Observers,
        project: Option<(ProjectName, &str)>,
    ) -> Result<Trees, ReadError> {
        let scanner = observers.scanner();
        let seeing = scanner.as_ref();
        let mut seen = Vec::new();
        let mut notices = Vec::new();
        if let Some((project, root)) = project {
            let site = Site::checkout(project, root);
            let observed = self.observe(seeing, &site).await?;
            notices.extend(observed.notices.iter().cloned());
            seen.push((site, observed));
        }
        if let Some(home) = observers.home_tree() {
            let site = Site::Home { tree: home };
            let observed = self.observe(seeing, &site).await?;
            notices.extend(observed.notices.iter().cloned());
            seen.push((site, observed));
        }
        Ok(Trees { seen, notices })
    }

    /// [`Observations::observe_trees`] as a hook asks it (step 10c U13
    /// rework: re-injection trusts an observation up to `trust` seconds
    /// old): nothing when every tree it would observe was scanned within
    /// `trust` by the daemon's clock, else every one observed. With no
    /// clock every hook observes.
    pub async fn observe_trees_trusting(
        &self,
        observers: &dyn Observers,
        project: Option<(ProjectName, &str)>,
        trust: i64,
    ) -> Result<(), ReadError> {
        let dirs: Vec<String> = project
            .as_ref()
            .map(|(name, root)| Site::checkout(name.clone(), root).tree().to_string())
            .into_iter()
            .chain(observers.home_tree())
            .collect();
        if let Some(clock) = &self.clock {
            let now = clock();
            let scanned = self
                .scanned
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner);
            let trusted = dirs.iter().all(|dir| {
                scanned
                    .get(dir)
                    .is_some_and(|at| (0..trust).contains(&(now - at)))
            });
            if trusted {
                return Ok(());
            }
        }
        self.observe_trees(observers, project).await.map(|_| ())
    }

    /// Every memo of what was seen dropped (`ctxloom context refresh`):
    /// the scanner's hashes and when each tree was scanned, so the next
    /// observation hashes every file again. The record is untouched.
    pub fn forget(&self, observers: &dyn Observers) {
        observers.scanner().forget();
        self.scanned
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clear();
    }

    /// The record's state of `tree` now (a default one for a tree never
    /// observed): what a write's token is read from.
    pub fn held(&self, tree: &TreeName) -> library_tree::Library {
        let checkouts = self.checkouts.checkouts();
        checkouts.tree(&tree.root()).cloned().unwrap_or_default()
    }

    /// The export recorded under `request_id`, in any tree (AIP-155).
    pub fn exported(&self, request_id: &str) -> Option<library_tree::BundleExported> {
        self.checkouts.checkouts().export(request_id).cloned()
    }

    /// The checkouts these observations reconcile against, shared: what a
    /// watcher watches (step 10c G7).
    pub fn checkouts(&self) -> LibraryCheckouts {
        self.checkouts.clone()
    }

    /// The intent recorded under `request_id`, in any tree (step 10b U3).
    pub fn known(&self, request_id: &str) -> Option<library_checkout::Known> {
        self.checkouts.checkouts().intent(request_id).cloned()
    }

    /// `book` (a library command or fact) through the edge.
    pub async fn send(
        &self,
        book: angzarr_embedded::v1::CommandBook,
    ) -> Result<angzarr_embedded::Answered, angzarr_embedded::Failure<angzarr_embedded::Rejected>>
    {
        self.edge.handle(book).await
    }
}

#[cfg(test)]
#[path = "observe.test.rs"]
mod tests;
