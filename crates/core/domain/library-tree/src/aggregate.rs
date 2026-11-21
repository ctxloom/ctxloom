//! The library aggregate on angzarr-embedded's markup: one stream per tree.
//!
//! Commands are operation-shaped (L17) and decided on evidence (L14): the
//! application looks, `decide` judges what it found against the state and
//! the token the caller saw (G4 superseded), and records an INTENT. Facts
//! are what already happened (an observation, a step done or failed) and
//! are never refused: one the tree cannot use records nothing.

// The `#[aggregate]` macro generates the facts' enum from its handlers, and
// every fact is `Record*` (the naming rule), so its variants share the
// prefix by design.
#![allow(clippy::enum_variant_names)]

use angzarr_embedded::markup::{
    pack_message, Bug, Context, DomainMessage, FactEmit, Failed, PageContext, Refuses, Snapshots,
};
use angzarr_embedded_macros::{aggregate, applies, handles_cmd, handles_fact};

use crate::refusal::refuse;
use crate::{
    BuiltinReadOnly, NoSuchResource, NoSuchSlot, OneChangePerCall, PlanRejected, PlanStale,
    ResourceExists, SlotNotDue,
};
use crate::{
    BundleExported, ChangeFailed, ChangeIntended, ChangeStatus, CreateDirectory, DirtyPath,
    Evidence, External, HomeRepositoryMade, Intent, IntentSuperseded, Library, LibraryCommand,
    LibraryEvent, LibraryTreeRejection, MovePath, PurgeSlot, PurgeTrash, RecordBundleExported,
    RecordStepDone, RecordStepFailed, RecordTreeInstalled, RecordTreeListed, RecordTreeObserved,
    ReplaceFile, RunSequence, Step, StepApplied, TrashPath, TreeInstalled, TreeObserved, KEEP_DAYS,
};
use std::collections::BTreeMap;

/// The code a plan's rejection takes when the tree no longer reads and the
/// plan named no diagnostic of its own.
pub const TREE_UNREADABLE: &str = "library-tree-tree-unreadable";

/// The verbs that write other tools' files (step 10c U10, L15, T26): only
/// a sequence of one of them may hold a foreign step.
pub const FOREIGN_WRITERS: &[&str] = &[
    "materialize",
    // A consented pull's project refresh (`deps pull --yes`, step 10c U4).
    "deps pull",
    "doctor --fix",
    "manage install",
    "manage uninstall",
    "bundle move",
];

/// The code of a foreign step planned where no foreign writer's sequence
/// holds it (protos.md §1.1: a bug of the plan, never the caller's).
pub const FOREIGN_OUTSIDE_WRITER: &str = "library-tree-foreign-outside-writer";

/// A foreign step planned where nothing may hold one.
fn foreign_outside() -> LibraryTreeRejection {
    LibraryTreeRejection::PlanRejected {
        code: FOREIGN_OUTSIDE_WRITER.to_string(),
        args: Default::default(),
    }
}

/// The library aggregate: one tree, folded from its stream.
#[derive(Clone, Debug, Default)]
pub struct LibraryAggregate {
    library: Library,
}

impl LibraryAggregate {
    /// The folded tree.
    pub fn state(&self) -> &Library {
        &self.library
    }
}

/// Which of the evidence's checks a command answers to: each command
/// judges only what its `refuses` lists.
#[derive(Clone, Copy, Default)]
struct Checks {
    plan: bool,
    builtin: bool,
}

/// The bundle a library path is in (`content/bundles/v2/<bundle>/...`),
/// else the path itself: what `BuiltinReadOnly` names.
fn bundle_of(path: &str) -> String {
    path.strip_prefix("content/bundles/v2/")
        .and_then(|rest| rest.split('/').next())
        .filter(|bundle| !bundle.is_empty())
        .unwrap_or(path)
        .to_string()
}

/// The evidence's verdict, in order: the built-in bundle, the plan. None
/// when it all holds. git is no evidence (the library is git-free, owner
/// 2026-11-18).
fn judge(evidence: &Evidence, path: &str, checks: Checks) -> Result<(), LibraryTreeRejection> {
    if checks.builtin && evidence.builtin {
        return Err(LibraryTreeRejection::BuiltinReadOnly {
            bundle: bundle_of(path),
        });
    }
    if checks.plan {
        if let Some(rejected) = &evidence.plan_rejection {
            return Err(LibraryTreeRejection::PlanRejected {
                code: rejected.code.clone(),
                args: rejected.args.clone(),
            });
        }
        if !evidence.tree_reads {
            return Err(LibraryTreeRejection::PlanRejected {
                code: TREE_UNREADABLE.to_string(),
                args: Default::default(),
            });
        }
    }
    Ok(())
}

impl LibraryAggregate {
    /// `path`'s token now, refused stale when the caller saw another.
    fn fresh(&self, path: &str, seen: u64) -> Result<(), LibraryTreeRejection> {
        let now = self.library.token(path);
        if seen == now {
            return Ok(());
        }
        Err(LibraryTreeRejection::PlanStale {
            path: path.to_string(),
            seen,
            now,
        })
    }

    /// `path` as ONE file (T29): refused when the record knows several
    /// files under it (a directory), each named below it.
    fn one_file(&self, path: &str) -> Result<(), LibraryTreeRejection> {
        let under = format!("{}/", path.trim_end_matches('/'));
        let paths: Vec<String> = self
            .library
            .resources
            .iter()
            .filter(|(key, resource)| key.starts_with(&under) && !resource.oid.is_empty())
            .map(|(key, _)| key[under.len()..].to_string())
            .collect();
        if paths.len() < 2 {
            return Ok(());
        }
        Err(LibraryTreeRejection::OneChangePerCall { paths })
    }

    fn present(&self, path: &str) -> Result<(), LibraryTreeRejection> {
        if self.library.known_absent(path) {
            return Err(LibraryTreeRejection::NoSuchResource {
                path: path.to_string(),
            });
        }
        Ok(())
    }

    fn absent(&self, path: &str) -> Result<(), LibraryTreeRejection> {
        if self.library.known_present(path) {
            return Err(LibraryTreeRejection::ResourceExists {
                path: path.to_string(),
            });
        }
        Ok(())
    }

    /// One step of a sequence judged on the token its caller saw. Not on
    /// existence: a sequence refuses only what its `refuses` lists, and a
    /// retry resumes steps whose paths an earlier try already made or took
    /// away (T28), so the operation's own NOREPLACE is the word on that
    /// (10b, U8).
    fn step(&self, step: &Step) -> Result<(), LibraryTreeRejection> {
        if let Some(replace) = step.replace.as_ref().filter(|replace| !replace.foreign) {
            return self.fresh(&replace.path, replace.seen_sequence);
        }
        if let Some(trash) = &step.trash {
            return self.fresh(&trash.path, trash.seen_sequence);
        }
        if let Some(moved) = &step.r#move {
            return self.fresh(&moved.from, moved.seen_sequence);
        }
        Ok(())
    }

    /// The intent of the command `ctx` carries, its steps `steps`.
    fn intent(&self, verb: String, steps: Vec<Step>, ctx: &Context) -> ChangeIntended {
        let request_id = ctx
            .command
            .command_id
            .as_ref()
            .and_then(|id| uuid::Uuid::from_slice(&id.value).ok())
            .map(|id| id.to_string())
            .unwrap_or_default();
        ChangeIntended {
            intent: Intent {
                request_id,
                verb,
                steps,
                base_cause: self.library.last_cause.clone(),
                done: 0,
                // A foreign writer's sequence lists its files (step 10c
                // U10): `sequence` sets them.
                foreign: Vec::new(),
            },
        }
    }

    /// What an observation adds to the state: its changes less those the
    /// record already holds at the same oid (an observation made against
    /// an older state, whose base another has recorded since, or one that
    /// says again what was said). None when it adds nothing.
    fn observation(&self, fact: RecordTreeObserved) -> Option<TreeObserved> {
        let library = &self.library;
        if !library.name.is_empty() && fact.name != library.name {
            return None;
        }
        let stale = fact.base_cause != library.last_cause;
        let changes: Vec<_> = fact
            .changes
            .into_iter()
            .filter(|change| {
                library.resources.get(&change.path).map(|r| r.oid.as_str())
                    != Some(change.to_oid.as_str())
                    || (change.status == ChangeStatus::Renamed
                        && library
                            .resources
                            .get(&change.from_path)
                            .is_some_and(|r| !r.oid.is_empty()))
            })
            .collect();
        let dirty_after: std::collections::BTreeMap<_, _> = fact
            .dirty_after
            .iter()
            .map(|dirty| (dirty.path.clone(), dirty.oid.clone()))
            .collect();
        let first = !library.baselined;
        let baseline = fact.baseline || first;
        // HEAD alone moving is nothing (QUESTIONS A), and nor is a motion
        // alone: a read during a merge that sees what the last one saw
        // records nothing more.
        let same =
            changes.is_empty() && dirty_after == library.dirty && fact.tree_after == library.tree;
        if same && !baseline {
            return None;
        }
        if same && fact.baseline && stale {
            // A baseline someone else recorded since this one was made.
            return None;
        }
        Some(TreeObserved {
            name: fact.name,
            external: Some(External {
                head_before: fact.head_before,
                head_after: fact.head_after,
            }),
            intended: None,
            changes,
            dirty_after: fact.dirty_after,
            tree_after: fact.tree_after,
            baseline,
        })
    }
}

/// What a step leaves, as an observation would see it: each path it
/// writes with the id it writes there; for a trash, `under` the path whose
/// every change is a deletion.
enum Leaves {
    Ids(Vec<(String, String)>),
    Gone(String),
    /// A move: not attributed by oid (U9's).
    Unknown,
}

fn leaves(step: &Step) -> Leaves {
    if let Some(replace) = &step.replace {
        return Leaves::Ids(vec![(replace.path.clone(), replace.new_oid.clone())]);
    }
    if let Some(create) = &step.create {
        let dir = create.path.trim_end_matches('/');
        return Leaves::Ids(
            create
                .entries
                .iter()
                .map(|entry| (format!("{dir}/{}", entry.path), entry.oid.clone()))
                .collect(),
        );
    }
    if let Some(trash) = &step.trash {
        return Leaves::Gone(trash.path.clone());
    }
    Leaves::Unknown
}

/// Whether `changes` (an outside observation's) touch a path the step
/// `step` is still to write, with ids that are not what it writes there
/// (step 10c U13, protos.md §1.3): the base it was decided on has moved.
fn overtakes(step: &Step, changes: &[crate::Change]) -> bool {
    let at_or_under = |path: &str, change: &crate::Change| {
        let under = format!("{}/", path.trim_end_matches('/'));
        change.path == path || change.path.starts_with(&under)
    };
    if let Some(replace) = &step.replace {
        return changes
            .iter()
            .any(|c| c.path == replace.path && c.to_oid != replace.new_oid);
    }
    if let Some(create) = &step.create {
        let dir = create.path.trim_end_matches('/');
        return changes.iter().filter(|c| at_or_under(dir, c)).any(|c| {
            !create
                .entries
                .iter()
                .any(|entry| format!("{dir}/{}", entry.path) == c.path && entry.oid == c.to_oid)
        });
    }
    if let Some(trash) = &step.trash {
        return changes
            .iter()
            .any(|c| at_or_under(&trash.path, c) && !c.to_oid.is_empty());
    }
    if let Some(moved) = &step.r#move {
        return changes.iter().any(|c| {
            (at_or_under(&moved.from, c) && !c.to_oid.is_empty())
                || (moved.to_tree.is_empty() && at_or_under(&moved.to, c))
        });
    }
    false
}

/// The cause a fact's context names: its external id.
fn fact_cause(ctx: &Context) -> String {
    match &ctx.command.arm {
        angzarr_embedded::markup::InputArm::Fact {
            origin: Some(origin),
        } => origin.external_id.clone(),
        _ => String::new(),
    }
}

impl LibraryAggregate {
    /// The pending intents (but `except`) whose steps not yet done an
    /// outside observation's `changes` overtake, by request id: each is
    /// superseded (step 10c U13), its retry refused stale.
    fn superseded(&self, changes: &[crate::Change], except: Option<&str>) -> Vec<String> {
        self.library
            .pending
            .values()
            .filter(|intent| Some(intent.request_id.as_str()) != except)
            .filter(|intent| {
                intent
                    .steps
                    .iter()
                    .skip(intent.done as usize)
                    .any(|step| overtakes(step, changes))
            })
            .map(|intent| intent.request_id.clone())
            .collect()
    }

    /// The pending intent whose next step's new ids `changes` show (the
    /// first by request id), that step, and what the changes say of its
    /// paths.
    fn attributed(&self, changes: &[crate::Change]) -> Option<(String, u32, Vec<DirtyPath>)> {
        self.library.pending.values().find_map(|intent| {
            let step = intent.steps.get(intent.done as usize)?;
            let seen = match leaves(step) {
                Leaves::Ids(ids) if !ids.is_empty() => ids
                    .iter()
                    .map(|(path, oid)| {
                        changes
                            .iter()
                            .find(|c| &c.path == path && &c.to_oid == oid)
                            .map(|_| DirtyPath {
                                path: path.clone(),
                                oid: oid.clone(),
                            })
                    })
                    .collect::<Option<Vec<_>>>()?,
                Leaves::Gone(path) => {
                    let under = format!("{}/", path.trim_end_matches('/'));
                    let mine: Vec<_> = changes
                        .iter()
                        .filter(|c| c.path == path || c.path.starts_with(&under))
                        .collect();
                    if mine.is_empty() || mine.iter().any(|c| !c.to_oid.is_empty()) {
                        return None;
                    }
                    mine.iter()
                        .map(|c| DirtyPath {
                            path: c.path.clone(),
                            oid: String::new(),
                        })
                        .collect()
                }
                _ => return None,
            };
            Some((intent.request_id.clone(), intent.done, seen))
        })
    }
}

impl LibraryAggregate {
    /// `observed` recorded, then every pending intent but `except` it
    /// overtakes superseded, by this observation's cause (step 10c U13).
    fn outside(
        &self,
        observed: TreeObserved,
        except: Option<&str>,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let superseded = self.superseded(&observed.changes, except);
        let by_cause = fact_cause(ctx);
        let mut events = vec![pack_message(&observed)];
        events.extend(superseded.into_iter().map(|request_id| {
            pack_message(&IntentSuperseded {
                request_id,
                by_cause: by_cause.clone(),
            })
        }));
        Ok(FactEmit::of(events))
    }
}

/// The changes from `held` (a tree's last listing, path to content id) to
/// `found` (every file now): each path added, changed or gone, sorted by
/// path.
pub fn listing_changes(held: &BTreeMap<String, String>, found: &[DirtyPath]) -> Vec<crate::Change> {
    let now: BTreeMap<&str, &str> = found
        .iter()
        .map(|file| (file.path.as_str(), file.oid.as_str()))
        .collect();
    let mut changes = Vec::new();
    let paths: std::collections::BTreeSet<&str> = held
        .keys()
        .map(String::as_str)
        .chain(now.keys().copied())
        .collect();
    for path in paths {
        let (was, is) = (held.get(path).map(String::as_str), now.get(path).copied());
        let status = match (was, is) {
            (None, Some(_)) => ChangeStatus::Added,
            (Some(_), None) => ChangeStatus::Deleted,
            (Some(was), Some(is)) if was != is => ChangeStatus::Modified,
            _ => continue,
        };
        changes.push(crate::Change {
            path: path.to_string(),
            status,
            from_oid: was.unwrap_or_default().to_string(),
            to_oid: is.unwrap_or_default().to_string(),
            from_path: String::new(),
        });
    }
    changes
}

/// An event a fact records, or none.
fn recorded<E: DomainMessage>(event: Option<E>) -> Result<FactEmit<LibraryAggregate>, Bug> {
    Ok(FactEmit::of(event.map(|event| pack_message(&event))))
}

/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for LibraryAggregate {
    type Anchor = crate::generated::LibraryState;
}

#[aggregate(domain = "library", command = LibraryCommand, event = LibraryEvent)]
impl LibraryAggregate {
    /// Replace (or create) one file: judged on the built-in bundle and the
    /// plan, then on the path's token.
    #[handles_cmd(
        ReplaceFile,
        refuses(PlanStale, BuiltinReadOnly, PlanRejected, OneChangePerCall)
    )]
    fn replace(
        &self,
        cmd: ReplaceFile,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, ReplaceFile>>> {
        let checks = Checks {
            plan: true,
            builtin: true,
        };
        judge(&cmd.evidence, &cmd.path, checks)
            .and_then(|()| {
                if cmd.foreign {
                    return Err(foreign_outside());
                }
                Ok(())
            })
            .and_then(|()| self.one_file(&cmd.path))
            .and_then(|()| self.fresh(&cmd.path, cmd.seen_sequence))
            .map_err(|rejection| {
                refuse!(
                    LibraryTreeRejection,
                    rejection,
                    Refuses<Self, ReplaceFile>,
                    [
                        OneChangePerCall { paths } => OneChangePerCall,
                        PlanStale { path, seen, now } => PlanStale,
                        BuiltinReadOnly { bundle } => BuiltinReadOnly,
                        PlanRejected { code, args } => PlanRejected,
                    ]
                )
            })?;
        let verb = cmd.verb.clone();
        let step = Step {
            replace: Some(cmd),
            create: None,
            trash: None,
            r#move: None,
            purge: None,
        };
        Ok(Some(self.intent(verb, vec![step], ctx)))
    }

    /// A new directory, never over something the record knows is there.
    #[handles_cmd(
        CreateDirectory,
        refuses(ResourceExists, BuiltinReadOnly, PlanRejected)
    )]
    fn create(
        &self,
        cmd: CreateDirectory,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, CreateDirectory>>> {
        let checks = Checks {
            plan: true,
            builtin: true,
        };
        judge(&cmd.evidence, &cmd.path, checks)
            .and_then(|()| self.absent(&cmd.path))
            .map_err(|rejection| {
                refuse!(
                    LibraryTreeRejection,
                    rejection,
                    Refuses<Self, CreateDirectory>,
                    [
                        ResourceExists { path } => ResourceExists,
                        BuiltinReadOnly { bundle } => BuiltinReadOnly,
                        PlanRejected { code, args } => PlanRejected,
                    ]
                )
            })?;
        let verb = cmd.verb.clone();
        let step = Step {
            replace: None,
            create: Some(cmd),
            trash: None,
            r#move: None,
            purge: None,
        };
        Ok(Some(self.intent(verb, vec![step], ctx)))
    }

    /// A path into the trash: it must not be known gone, and its token
    /// must be the one the caller saw.
    #[handles_cmd(TrashPath, refuses(NoSuchResource, PlanStale, BuiltinReadOnly))]
    fn trash(
        &self,
        cmd: TrashPath,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, TrashPath>>> {
        let checks = Checks {
            plan: false,
            builtin: true,
        };
        judge(&cmd.evidence, &cmd.path, checks)
            .and_then(|()| self.present(&cmd.path))
            .and_then(|()| self.fresh(&cmd.path, cmd.seen_sequence))
            .map_err(|rejection| {
                refuse!(
                    LibraryTreeRejection,
                    rejection,
                    Refuses<Self, TrashPath>,
                    [
                        NoSuchResource { path } => NoSuchResource,
                        PlanStale { path, seen, now } => PlanStale,
                        BuiltinReadOnly { bundle } => BuiltinReadOnly,
                    ]
                )
            })?;
        let verb = cmd.verb.clone();
        let step = Step {
            replace: None,
            create: None,
            trash: Some(cmd),
            r#move: None,
            purge: None,
        };
        Ok(Some(self.intent(verb, vec![step], ctx)))
    }

    /// ctxloom's own move: from a path not known gone, to one not known
    /// there, `from`'s token the one the caller saw.
    #[handles_cmd(MovePath, refuses(NoSuchResource, ResourceExists, PlanStale))]
    fn move_path(
        &self,
        cmd: MovePath,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, MovePath>>> {
        // Into another tree (QUESTIONS 1(a)), where it goes is that tree's
        // record, never this one's: its NOREPLACE rename judges it.
        let here = cmd.to_tree.is_empty();
        judge(&cmd.evidence, &cmd.from, Checks::default())
            .and_then(|()| self.present(&cmd.from))
            .and_then(|()| if here { self.absent(&cmd.to) } else { Ok(()) })
            .and_then(|()| self.fresh(&cmd.from, cmd.seen_sequence))
            .map_err(|rejection| {
                refuse!(
                    LibraryTreeRejection,
                    rejection,
                    Refuses<Self, MovePath>,
                    [
                        NoSuchResource { path } => NoSuchResource,
                        ResourceExists { path } => ResourceExists,
                        PlanStale { path, seen, now } => PlanStale,
                    ]
                )
            })?;
        let verb = cmd.verb.clone();
        let step = Step {
            replace: None,
            create: None,
            trash: None,
            r#move: Some(cmd),
            purge: None,
        };
        Ok(Some(self.intent(verb, vec![step], ctx)))
    }

    /// A retryable sequence (T28): its evidence judged once, each step's
    /// token judged as its own command's; the steps kept in the intent.
    #[handles_cmd(RunSequence, refuses(PlanStale, PlanRejected))]
    fn sequence(
        &self,
        cmd: RunSequence,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, RunSequence>>> {
        let checks = Checks {
            plan: true,
            builtin: false,
        };
        let writes_foreign = FOREIGN_WRITERS.contains(&cmd.verb.as_str());
        judge(&cmd.evidence, "", checks)
            .and_then(|()| {
                let foreign = cmd
                    .steps
                    .iter()
                    .any(|step| step.replace.as_ref().is_some_and(|r| r.foreign));
                if foreign && !writes_foreign {
                    return Err(foreign_outside());
                }
                Ok(())
            })
            .and_then(|()| cmd.steps.iter().try_for_each(|step| self.step(step)))
            .map_err(|rejection| {
                refuse!(
                    LibraryTreeRejection,
                    rejection,
                    Refuses<Self, RunSequence>,
                    [
                        PlanStale { path, seen, now } => PlanStale,
                        PlanRejected { code, args } => PlanRejected,
                    ]
                )
            })?;
        let mut intended = self.intent(cmd.verb, cmd.steps, ctx);
        intended.intent.foreign = cmd.foreign;
        Ok(Some(intended))
    }

    /// The purge of a kept trash slot (G12, L12): the slot must be in the
    /// state and, by the daemon's clock (its age the EVIDENCE: decide reads
    /// no clock), at least [`KEEP_DAYS`] old. Its one step removes the
    /// slot; its `StepApplied` drops it from the state.
    #[handles_cmd(PurgeTrash, refuses(NoSuchSlot, SlotNotDue))]
    fn purge(
        &self,
        cmd: PurgeTrash,
        ctx: &Context,
    ) -> Result<Option<ChangeIntended>, Failed<Refuses<Self, PurgeTrash>>> {
        let refused = if !self.library.trashed.contains_key(&cmd.request_id) {
            Some(LibraryTreeRejection::NoSuchSlot {
                request_id: cmd.request_id.clone(),
            })
        } else if cmd.age_days < KEEP_DAYS {
            Some(LibraryTreeRejection::SlotNotDue {
                request_id: cmd.request_id.clone(),
                age_days: cmd.age_days,
            })
        } else {
            None
        };
        if let Some(rejection) = refused {
            return Err(refuse!(
                LibraryTreeRejection,
                rejection,
                Refuses<Self, PurgeTrash>,
                [
                    NoSuchSlot { request_id } => NoSuchSlot,
                    SlotNotDue { request_id, age_days } => SlotNotDue,
                ]
            ));
        }
        let step = Step {
            replace: None,
            create: None,
            trash: None,
            r#move: None,
            purge: Some(PurgeSlot {
                request_id: cmd.request_id,
            }),
        };
        Ok(Some(self.intent(
            "trash purge".to_string(),
            vec![step],
            ctx,
        )))
    }

    /// An observation (L5): recorded when it adds anything to the state, a
    /// tree's first one always (its baseline, L16); another tree's, or one
    /// that adds nothing, records nothing.
    ///
    /// An observation that sees a pending intent's own new ids is the
    /// intent's (U3's attribution, by oid): its step done (`StepApplied`)
    /// when that is all it sees and the dirty set is what the step leaves,
    /// else a `TreeObserved` caused by the intent.
    #[handles_fact(RecordTreeObserved)]
    fn observed(&self, fact: RecordTreeObserved, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let Some(mut observed) = self.observation(fact) else {
            return recorded(None::<TreeObserved>);
        };
        // A baseline, or a sight past a moved HEAD (a commit, a checkout),
        // is the outside's: its cause is the commits.
        let moved = observed
            .external
            .as_ref()
            .is_some_and(|external| external.head_after != self.library.head);
        if observed.baseline || moved {
            return self.outside(observed, None, ctx);
        }
        let Some((request_id, step, seen)) = self.attributed(&observed.changes) else {
            return self.outside(observed, None, ctx);
        };
        let whole = seen.len() == observed.changes.len()
            && observed.tree_after == self.library.tree
            && self.library.dirty_after(&seen)
                == observed
                    .dirty_after
                    .iter()
                    .map(|d| (d.path.clone(), d.oid.clone()))
                    .collect();
        if whole {
            return recorded(Some(StepApplied {
                request_id,
                step,
                observed: seen,
            }));
        }
        observed.external = None;
        observed.intended = Some(request_id.clone());
        self.outside(observed, Some(&request_id), ctx)
    }

    /// RecordTreeListed (step 10c U13 rework, the scanner): every file the
    /// scan found, compared with the listing the state holds ([`listing_changes`]),
    /// recorded as the observation a read's would be: the first a baseline
    /// of every file, a match nothing, an intent's own change attributed.
    #[handles_fact(RecordTreeListed)]
    fn listed(&self, fact: RecordTreeListed, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        // A tree never listed holds no listing: every file is added, and
        // `observation` records it as the tree's baseline.
        let held = self.library.listed();
        let changes = listing_changes(&held, &fact.files);
        if changes.is_empty() && self.library.baselined {
            // What the record lists, a path a step took away left out.
            return recorded(None::<TreeObserved>);
        }
        let observed = RecordTreeObserved {
            name: fact.name,
            kind: fact.kind,
            base_cause: fact.base_cause,
            head_before: self.library.head.clone(),
            head_after: String::new(),
            tree_after: String::new(),
            changes,
            dirty_after: fact.files,
            baseline: false,
        };
        self.observed(observed, ctx)
    }

    /// A pending intent's next step done: what it left becomes its paths'
    /// record at this event's sequence. A step not pending records nothing.
    #[handles_fact(RecordStepDone)]
    fn step_done(&self, fact: RecordStepDone, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let pending = self
            .library
            .pending
            .get(&fact.request_id)
            .is_some_and(|intent| fact.step == intent.done);
        recorded(pending.then_some(StepApplied {
            request_id: fact.request_id,
            step: fact.step,
            observed: fact.observed,
        }))
    }

    /// A pending intent's next step, a new directory in the cache, done as
    /// an installed tree (step 10c U4, protos.md §1.2): its sha's tree
    /// whole. A step not next, or not a new directory, records nothing.
    #[handles_fact(RecordTreeInstalled)]
    fn tree_was_installed(
        &self,
        fact: RecordTreeInstalled,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let next_create = self
            .library
            .pending
            .get(&fact.request_id)
            .is_some_and(|intent| {
                fact.step == intent.done
                    && intent
                        .steps
                        .get(fact.step as usize)
                        .is_some_and(|step| step.create.is_some())
            });
        recorded(next_create.then_some(TreeInstalled {
            request_id: fact.request_id,
            step: fact.step,
            identity: fact.identity,
            sha: fact.sha,
            tree: fact.tree,
        }))
    }

    /// A pending intent's step failed: final.
    #[handles_fact(RecordStepFailed)]
    fn step_failed(&self, fact: RecordStepFailed, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let pending = self.library.pending.contains_key(&fact.request_id);
        recorded(pending.then_some(ChangeFailed {
            request_id: fact.request_id,
            step: fact.step,
            ext_err_txt: fact.ext_err_txt,
        }))
    }

    /// A bundle of this tree exported, as it was made (AIP-155).
    #[handles_fact(RecordBundleExported)]
    fn bundle_exported(
        &self,
        fact: RecordBundleExported,
        ctx: &Context,
    ) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        recorded(Some(BundleExported {
            request_id: fact.request_id,
            bundle: fact.bundle,
            source: fact.source,
            destination: fact.destination,
        }))
    }

    #[applies(BundleExported)]
    fn bundle_was_exported(&mut self, event: BundleExported, page: &PageContext) {
        self.library.apply_page(
            &LibraryEvent::BundleExported(event),
            page.sequence,
            page.meta,
        );
    }

    /// Never emitted since the git-free ruling (owner 2026-11-18); a 10b
    /// stream's is applied as nothing, so the stream still replays.
    #[applies(HomeRepositoryMade)]
    fn home_repository_was_made(&mut self, event: HomeRepositoryMade, page: &PageContext) {
        self.library.apply_page(
            &LibraryEvent::HomeRepositoryMade(event),
            page.sequence,
            page.meta,
        );
    }

    #[applies(TreeObserved)]
    fn tree_observed(&mut self, event: TreeObserved, page: &PageContext) {
        self.library
            .apply_page(&LibraryEvent::TreeObserved(event), page.sequence, page.meta);
    }

    #[applies(ChangeIntended)]
    fn change_intended(&mut self, event: ChangeIntended, page: &PageContext) {
        self.library.apply_page(
            &LibraryEvent::ChangeIntended(event),
            page.sequence,
            page.meta,
        );
    }

    #[applies(StepApplied)]
    fn step_applied(&mut self, event: StepApplied, page: &PageContext) {
        self.library
            .apply_page(&LibraryEvent::StepApplied(event), page.sequence, page.meta);
    }

    #[applies(ChangeFailed)]
    fn change_failed(&mut self, event: ChangeFailed, page: &PageContext) {
        self.library
            .apply_page(&LibraryEvent::ChangeFailed(event), page.sequence, page.meta);
    }

    #[applies(TreeInstalled)]
    fn tree_installed(&mut self, event: TreeInstalled, page: &PageContext) {
        self.library.apply_page(
            &LibraryEvent::TreeInstalled(event),
            page.sequence,
            page.meta,
        );
    }

    #[applies(IntentSuperseded)]
    fn intent_superseded(&mut self, event: IntentSuperseded, page: &PageContext) {
        self.library.apply_page(
            &LibraryEvent::IntentSuperseded(event),
            page.sequence,
            page.meta,
        );
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
mod tests;
