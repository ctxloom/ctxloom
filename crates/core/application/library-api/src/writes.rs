//! A library write as the daemon makes it (step 10b U3; protos.md §1.1):
//! the use case PLANS it over recording ports
//! ([`library_application::write::recording`]), and what it planned is
//! shaped here into the one step of the library domain it is: one file
//! replaced, one new directory, or one path into the trash. A plan of more
//! than one file is refused `OneChangePerCall` before any command (T29),
//! in the domain's own words; a plan of nothing writes nothing.
//!
//! What the step's command is (its paths, the ids git would give what it
//! writes) is [`intent_of`]; the atomic operation that makes it (U8) is
//! [`operation`]; what the step left, as the re-observe sees it, is
//! [`left_by`].

use std::collections::BTreeMap;

pub use ctxloom_ports::Scope;
use ctxloom_ports::{Change, Operation, Put};
use library_application::write::recording::Planned;
use library_tree::{
    CreateDirectory, DirtyPath, Evidence, LibraryTreeRejection, MovePath, PurgeSlot, ReplaceFile,
    RunSequence, Step, TrashPath,
};

/// Where a bundle's directory is in its tree.
pub const BUNDLES: &str = "content/bundles/v2";

/// The config file's path in its tree.
pub const CONFIG: &str = "config.yaml";

/// Which tree a write lands in: the content's (the project's, else the
/// home's) or a config layer's.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lands {
    Content,
    Layer(Scope),
}

/// The one step a write is, its paths relative to its tree's `.ctxloom`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Shape {
    /// One file written whole.
    Replace {
        path: String,
        bytes: Vec<u8>,
        executable: bool,
    },
    /// One new directory and the files in it, each below it.
    Create { path: String, files: Vec<Put> },
    /// One path taken away (into the trash, U8), with every file under it
    /// (below it; none for a file).
    Trash { path: String, files: Vec<String> },
    /// One path renamed to another (ctxloom's own move, L7), with every
    /// file under it (below it; none for a file).
    Move {
        from: String,
        to: String,
        files: Vec<String>,
        /// The other tree it lands in, by its directory (a local `bundle
        /// move` into another project, QUESTIONS 1(a)); none, this one.
        into: Option<String>,
    },
    /// The trash slot of the write's own request removed whole: a
    /// sequence's last step, so it leaves no trash (G12(a)).
    Purge,
    /// A retryable atomic sequence (T28): its steps in order, each one
    /// atomic operation, listed in its intent and resumed on a retry at the
    /// first not done.
    Sequence(Vec<Shape>),
}

impl Shape {
    /// The path the step changes: what its etag is of (a sequence's, its
    /// first step's; a purge's, none).
    pub fn path(&self) -> &str {
        match self {
            Shape::Replace { path, .. }
            | Shape::Create { path, .. }
            | Shape::Trash { path, .. } => path,
            Shape::Move { from, .. } => from,
            Shape::Purge => "",
            Shape::Sequence(steps) => steps.first().map_or("", Shape::path),
        }
    }

    /// Its steps in order: a sequence's, else itself alone.
    pub fn steps(&self) -> Vec<&Shape> {
        match self {
            Shape::Sequence(steps) => steps.iter().collect(),
            one => vec![one],
        }
    }
}

/// A planned write, shaped: the tree it lands in, and its step.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Shaped {
    pub lands: Lands,
    pub shape: Shape,
}

/// The atomic operation that makes the step `shape` (step 10b U8), its
/// paths relative to its tree; none for a sequence, whose steps each are
/// one.
pub fn operation(shape: &Shape) -> Option<Operation> {
    Some(match shape {
        Shape::Replace {
            path,
            bytes,
            executable,
        } => Operation::Replace(Put {
            path: path.clone(),
            bytes: bytes.clone(),
            executable: *executable,
        }),
        Shape::Create { path, files } => Operation::CreateDir {
            path: path.clone(),
            files: files.clone(),
        },
        Shape::Trash { path, .. } => Operation::Trash { path: path.clone() },
        Shape::Move { from, to, into, .. } => Operation::Move {
            from: from.clone(),
            to: to.clone(),
            into: into.clone(),
        },
        Shape::Purge => Operation::Purge,
        Shape::Sequence(_) => return None,
    })
}

/// A bundle's directory in its tree.
pub fn bundle_path(bundle: &str) -> String {
    format!("{BUNDLES}/{bundle}")
}

/// What `planned` (a use case's recorded writes, in order) is: none when it
/// writes nothing; a bundle taken away and made anew (a forced import) the
/// sequence trash, create, purge (T28); refused `OneChangePerCall` when it
/// changes more than one file otherwise, each named below its bundle. `files_under` answers the files
/// below a directory of a bundle now (`""`: the bundle's own), each below
/// it: several files written into a directory with none are one new
/// directory (a skill).
pub fn shape(
    planned: Vec<Planned>,
    files_under: impl Fn(&str, &str) -> Vec<String>,
) -> Result<Option<Shaped>, LibraryTreeRejection> {
    let mut planned = planned;
    if let [Planned::BundleGone { bundle: gone }, Planned::NewBundle { bundle, files }] =
        &planned[..]
    {
        if gone == bundle {
            let path = bundle_path(bundle);
            return Ok(Some(Shaped {
                lands: Lands::Content,
                shape: Shape::Sequence(vec![
                    Shape::Trash {
                        path: path.clone(),
                        files: files_under(bundle, ""),
                    },
                    Shape::Create {
                        path,
                        files: files.clone(),
                    },
                    Shape::Purge,
                ]),
            }));
        }
    }
    let one = match planned.len() {
        0 => return Ok(None),
        1 => planned.remove(0),
        _ => {
            return Err(LibraryTreeRejection::OneChangePerCall {
                paths: planned.iter().flat_map(paths_of).collect(),
            })
        }
    };
    let shaped = |lands, shape| Ok(Some(Shaped { lands, shape }));
    match &one {
        Planned::Layer { scope, text } => shaped(
            Lands::Layer(*scope),
            Shape::Replace {
                path: CONFIG.to_string(),
                bytes: text.as_bytes().to_vec(),
                executable: false,
            },
        ),
        Planned::NewBundle { bundle, files } => shaped(
            Lands::Content,
            Shape::Create {
                path: bundle_path(bundle),
                files: files.clone(),
            },
        ),
        Planned::BundleGone { bundle } => shaped(
            Lands::Content,
            Shape::Trash {
                path: bundle_path(bundle),
                files: files_under(bundle, ""),
            },
        ),
        Planned::Changed { bundle, changes } => {
            let shape = changed(bundle, changes, &files_under)?;
            match shape {
                Some(shape) => shaped(Lands::Content, shape),
                None => Ok(None),
            }
        }
        // A bundle moved into another tree is one move out of this one
        // (QUESTIONS 1(a)): its every file goes.
        Planned::MovedInto { bundle, app_dir } => shaped(
            Lands::Content,
            Shape::Move {
                from: bundle_path(bundle),
                to: bundle_path(bundle),
                files: files_under(bundle, ""),
                into: Some(app_dir.clone()),
            },
        ),
        // A file of the tree's own, its scaffold's (step 10c U5): one file
        // replaced; several are more than one change.
        Planned::TreeFiles { files } => match &files[..] {
            [] => Ok(None),
            [put] => shaped(
                Lands::Content,
                Shape::Replace {
                    path: put.path.clone(),
                    bytes: put.bytes.clone(),
                    executable: put.executable,
                },
            ),
            _ => Err(LibraryTreeRejection::OneChangePerCall {
                paths: paths_of(&one),
            }),
        },
        // A tree written whole where it was not (step 10c U6: a first
        // init's scaffold): one new directory, the tree's root itself.
        Planned::NewTree { files } => shaped(
            Lands::Content,
            Shape::Create {
                path: String::new(),
                files: files.clone(),
            },
        ),
        // A tree outside ctxloom's trees is no tree's step (D2's
        // EXCEPTION): the daemon takes it aside before it shapes.
        Planned::Outside { .. } => Ok(None),
    }
}

/// The step of changes to an existing bundle: one file written or taken
/// away, several written into one new directory, or several taken away (the
/// sequence of their trashes).
fn changed(
    bundle: &str,
    changes: &[Change],
    files_under: &impl Fn(&str, &str) -> Vec<String>,
) -> Result<Option<Shape>, LibraryTreeRejection> {
    let in_bundle = |path: &str| format!("{}/{path}", bundle_path(bundle));
    match changes {
        [] => Ok(None),
        [Change::Write(put)] => Ok(Some(Shape::Replace {
            path: in_bundle(&put.path),
            bytes: put.bytes.clone(),
            executable: put.executable,
        })),
        [Change::Remove { path }] => Ok(Some(Shape::Trash {
            path: in_bundle(path),
            files: Vec::new(),
        })),
        [Change::RemoveDir { path }] => Ok(Some(Shape::Trash {
            path: in_bundle(path),
            files: files_under(bundle, path),
        })),
        several
            if several
                .iter()
                .all(|change| !matches!(change, Change::Write(_)))
                && one_item(several) =>
        {
            // Several paths taken away (an item with its distilled form):
            // the sequence of their trashes (T28), each kept as a removal's
            // is. Two items' are two changes (T29), refused below.
            Ok(Some(Shape::Sequence(
                several
                    .iter()
                    .map(|change| {
                        let path = change_path(change);
                        let files = match change {
                            Change::RemoveDir { .. } => files_under(bundle, &path),
                            _ => Vec::new(),
                        };
                        Shape::Trash {
                            path: in_bundle(&path),
                            files,
                        }
                    })
                    .collect(),
            )))
        }
        several => {
            let writes: Option<Vec<_>> = several
                .iter()
                .map(|change| match change {
                    Change::Write(put) => Some(put),
                    _ => None,
                })
                .collect();
            let common = writes.as_ref().and_then(|puts| {
                let dir = puts.first()?.path.rsplit_once('/')?.0.to_string();
                let under = format!("{dir}/");
                puts.iter()
                    .all(|put| put.path.starts_with(&under))
                    .then_some(dir)
            });
            match (writes, common) {
                (Some(puts), Some(dir)) if files_under(bundle, &dir).is_empty() => {
                    Ok(Some(Shape::Create {
                        path: in_bundle(&dir),
                        files: puts
                            .iter()
                            .map(|put| Put {
                                path: put.path[dir.len() + 1..].to_string(),
                                ..(*put).clone()
                            })
                            .collect(),
                    }))
                }
                _ => Err(LibraryTreeRejection::OneChangePerCall {
                    paths: several.iter().map(change_path).collect(),
                }),
            }
        }
    }
}

/// Whether `changes` are one item's paths: its file and its distilled form
/// (`<dir>/<name>.md` beside `<dir>/<name>.distilled.md`).
fn one_item(changes: &[Change]) -> bool {
    let item = |change: &Change| {
        let path = change_path(change);
        match path.strip_suffix(".distilled.md") {
            Some(stem) => format!("{stem}.md"),
            None => path,
        }
    };
    let first = changes.first().map(item);
    changes.iter().all(|change| Some(item(change)) == first)
}

fn change_path(change: &Change) -> String {
    match change {
        Change::Write(put) => put.path.clone(),
        Change::Remove { path } | Change::RemoveDir { path } => path.clone(),
    }
}

/// The files a planned write names, below its bundle (a layer's file, its
/// own name).
fn paths_of(planned: &Planned) -> Vec<String> {
    match planned {
        Planned::Layer { .. } => vec![CONFIG.to_string()],
        Planned::NewBundle { files, .. } => files.iter().map(|put| put.path.clone()).collect(),
        Planned::BundleGone { bundle } => vec![bundle.clone()],
        Planned::Changed { changes, .. } => changes.iter().map(change_path).collect(),
        Planned::Outside { dir, .. } => vec![dir.clone()],
        Planned::TreeFiles { files } | Planned::NewTree { files } => {
            files.iter().map(|put| put.path.clone()).collect()
        }
        Planned::MovedInto { bundle, .. } => vec![bundle.clone()],
    }
}

/// A write's command: the step its shape is, against the token `seen` of
/// its path, the content ids of what it writes (`library_tree::content_id`,
/// step 10c U13 rework), under `verb` and the evidence `evidence`; a sequence's
/// is a `RunSequence` of its steps (T28), its trash not kept (its last
/// step, the purge of `request_id`'s own slot, removes it), each step on
/// its first path judged on `seen` (the caller's etag), each other on its
/// own path's token now (`token_now`).
pub fn intent_of(
    tree: &str,
    verb: &str,
    shape: &Shape,
    (seen, token_now): (u64, &dyn Fn(&str) -> u64),
    evidence: Evidence,
    request_id: &str,
) -> Intended {
    if let Shape::Sequence(shapes) = shape {
        // A sequence that purges its own slot keeps no trash (G12(a));
        // one that does not (`clean`) keeps each, as a lone removal does.
        let keep = !shapes.contains(&Shape::Purge);
        let mut steps = Vec::new();
        for one in shapes {
            if *one == Shape::Purge {
                steps.push(purge_step(request_id.to_string()));
                continue;
            }
            let seen = if one.path() == shape.path() {
                seen
            } else {
                token_now(one.path())
            };
            let intended = one_intent(tree, verb, one, seen, evidence.clone());
            let mut step = intended.step(request_id);
            if let Some(trash) = step.trash.as_mut() {
                trash.keep = keep;
            }
            steps.push(step);
        }
        return Intended::Sequence(RunSequence {
            foreign: Vec::new(),
            name: tree.to_string(),
            verb: verb.to_string(),
            steps,
            evidence,
        });
    }
    one_intent(tree, verb, shape, seen, evidence)
}

/// The command of one step: its shape's kind, its content ids.
fn one_intent(tree: &str, verb: &str, shape: &Shape, seen: u64, evidence: Evidence) -> Intended {
    match shape {
        Shape::Replace { path, bytes, .. } => Intended::Replace(ReplaceFile {
            foreign: false,
            name: tree.to_string(),
            verb: verb.to_string(),
            path: path.clone(),
            seen_sequence: seen,
            new_oid: library_tree::content_id(bytes),
            evidence,
        }),
        Shape::Create { path, files } => {
            let mut entries = Vec::new();
            for put in files {
                entries.push(DirtyPath {
                    path: put.path.clone(),
                    oid: library_tree::content_id(&put.bytes),
                });
            }
            Intended::Create(CreateDirectory {
                name: tree.to_string(),
                verb: verb.to_string(),
                path: path.clone(),
                entries,
                evidence,
            })
        }
        Shape::Trash { path, .. } => Intended::Trash(TrashPath {
            name: tree.to_string(),
            verb: verb.to_string(),
            path: path.clone(),
            seen_sequence: seen,
            evidence,
            // A lone removal's trash is kept 7 days (G12): only a
            // sequence's own (U9) is removed by its last step.
            keep: true,
        }),
        Shape::Move { from, to, into, .. } => Intended::Move(MovePath {
            name: tree.to_string(),
            verb: verb.to_string(),
            from: from.clone(),
            to: to.clone(),
            seen_sequence: seen,
            evidence,
            to_tree: into.clone().unwrap_or_default(),
        }),
        // A purge, or a sequence, is only ever a step of one ([`intent_of`]
        // makes those): alone, it is a sequence of nothing.
        Shape::Purge | Shape::Sequence(_) => Intended::Sequence(RunSequence {
            foreign: Vec::new(),
            name: tree.to_string(),
            verb: verb.to_string(),
            steps: Vec::new(),
            evidence,
        }),
    }
}

/// A step with none of its arms set, for one arm to be laid over.
fn no_step() -> Step {
    Step {
        replace: None,
        create: None,
        trash: None,
        r#move: None,
        purge: None,
    }
}

/// The step that removes the trash slot of `request_id` whole.
fn purge_step(request_id: String) -> Step {
    Step {
        purge: Some(PurgeSlot { request_id }),
        ..no_step()
    }
}

/// A write's command, as its step's kind.
#[derive(Clone, Debug, PartialEq)]
pub enum Intended {
    Replace(ReplaceFile),
    Create(CreateDirectory),
    Trash(TrashPath),
    Move(MovePath),
    Sequence(RunSequence),
}

impl Intended {
    /// Its steps as its intent lists them: a sequence's, else its one.
    pub fn steps(&self, request_id: &str) -> Vec<Step> {
        match self {
            Intended::Sequence(sequence) => sequence.steps.clone(),
            one => vec![one.step(request_id)],
        }
    }

    /// The one step a lone command is (a sequence's first).
    fn step(&self, request_id: &str) -> Step {
        match self {
            Intended::Replace(command) => Step {
                replace: Some(command.clone()),
                ..no_step()
            },
            Intended::Create(command) => Step {
                create: Some(command.clone()),
                ..no_step()
            },
            Intended::Trash(command) => Step {
                trash: Some(command.clone()),
                ..no_step()
            },
            Intended::Move(command) => Step {
                r#move: Some(command.clone()),
                ..no_step()
            },
            Intended::Sequence(sequence) => sequence
                .steps
                .first()
                .cloned()
                .unwrap_or_else(|| purge_step(request_id.to_string())),
        }
    }
}

/// The paths a step changes, each with the id it leaves (empty: taken
/// away): what the re-observe looks at. A trash leaves gone each file of
/// its shape (the path itself, for a file).
pub fn expected(intended: &Intended, shape: &Shape) -> Vec<DirtyPath> {
    expected_step(&intended.step(""), Some(shape))
}

/// What the step `step` leaves, as [`expected`] says it, its files below
/// its path those of `shape` (the plan's step for it; none for a step
/// resumed from its intent alone, whose trash or move then names its
/// path); a purge leaves nothing in the library.
pub fn expected_step(step: &Step, shape: Option<&Shape>) -> Vec<DirtyPath> {
    let left = expected_in_tree(step, shape);
    left.into_iter()
        .filter(|path| in_library(&path.path))
        .collect()
}

/// Whether `path` (below its tree's `.ctxloom`) is in the library's
/// pathspec (L13): the bundles, `config.yaml`, `remotes.yaml`,
/// `lock.yaml`. Never `cache/`, `sessions/` or `state/`, which git never
/// sees and the record never follows.
pub fn in_library(path: &str) -> bool {
    path.starts_with("content/") || matches!(path, CONFIG | "remotes.yaml" | "lock.yaml")
}

/// What `step` leaves in its tree, inside the library or not.
fn expected_in_tree(step: &Step, shape: Option<&Shape>) -> Vec<DirtyPath> {
    let gone = |path: String| DirtyPath {
        path,
        oid: String::new(),
    };
    let under = |dir: &str, files: &[String]| -> Vec<String> {
        if files.is_empty() {
            vec![dir.to_string()]
        } else {
            files.iter().map(|file| format!("{dir}/{file}")).collect()
        }
    };
    if let Some(replace) = &step.replace {
        return vec![DirtyPath {
            path: replace.path.clone(),
            oid: replace.new_oid.clone(),
        }];
    }
    if let Some(create) = &step.create {
        return create
            .entries
            .iter()
            .map(|entry| DirtyPath {
                // A root create's (step 10c U6) are the tree's own paths.
                path: if create.path.is_empty() {
                    entry.path.clone()
                } else {
                    format!("{}/{}", create.path, entry.path)
                },
                oid: entry.oid.clone(),
            })
            .collect();
    }
    if let Some(trash) = &step.trash {
        let files = match shape {
            Some(Shape::Trash { files, .. }) => under(&trash.path, files),
            _ => vec![trash.path.clone()],
        };
        return files.into_iter().map(gone).collect();
    }
    if let Some(moved) = &step.r#move {
        let files: &[String] = match shape {
            Some(Shape::Move { files, .. }) => files,
            _ => &[],
        };
        // Into another tree, only where it came from is this tree's.
        let here = moved.to_tree.is_empty();
        let to = if here {
            under(&moved.to, files)
        } else {
            Vec::new()
        };
        return under(&moved.from, files)
            .into_iter()
            .chain(to)
            .map(gone)
            .collect();
    }
    Vec::new()
}

/// The step of the plan `replanned` a recorded step `step` (T28: a
/// sequence resumed from its intent) takes what it writes and the files it
/// expects from: the one of its kind at its path. None for a purge, or a
/// step the plan no longer has.
pub fn content_for<'a>(step: &Step, replanned: &'a Shape) -> Option<&'a Shape> {
    replanned.steps().into_iter().find(|shape| match shape {
        Shape::Replace { path, .. } => step.replace.as_ref().is_some_and(|r| &r.path == path),
        Shape::Create { path, .. } => step.create.as_ref().is_some_and(|c| &c.path == path),
        Shape::Trash { path, .. } => step.trash.as_ref().is_some_and(|t| &t.path == path),
        Shape::Move { from, .. } => step.r#move.as_ref().is_some_and(|m| &m.from == from),
        Shape::Purge | Shape::Sequence(_) => false,
    })
}

/// The atomic operation that makes the recorded step `step`, its content
/// (a file's bytes, a new directory's files) the plan's step for it,
/// `shape`: none when the step writes content and no plan has it.
pub fn operation_of(step: &Step, shape: Option<&Shape>) -> Option<Operation> {
    if let Some(trash) = &step.trash {
        return Some(Operation::Trash {
            path: trash.path.clone(),
        });
    }
    if let Some(moved) = &step.r#move {
        return Some(Operation::Move {
            from: moved.from.clone(),
            to: moved.to.clone(),
            into: (!moved.to_tree.is_empty()).then(|| moved.to_tree.clone()),
        });
    }
    if step.purge.is_some() {
        return Some(Operation::Purge);
    }
    operation(shape?)
}

/// What a step left, as the scanner sees it now: each path it changed with
/// its content id there (empty where nothing is).
pub fn left_by(
    scanner: &dyn ctxloom_ports::LibraryScanner,
    dir: &str,
    paths: &[DirtyPath],
) -> Result<Vec<DirtyPath>, ctxloom_ports::Failure> {
    let asked: Vec<&str> = paths.iter().map(|p| p.path.as_str()).collect();
    let ids: BTreeMap<String, String> = scanner.content_ids(dir, &asked)?;
    Ok(paths
        .iter()
        .map(|p| DirtyPath {
            path: p.path.clone(),
            oid: ids.get(&p.path).cloned().unwrap_or_default(),
        })
        .collect())
}

/// The cover of `tree`'s stream.
fn cover_of(tree: &library_tree::TreeName) -> angzarr_embedded::v1::Cover {
    angzarr_embedded::v1::Cover {
        domain: library_tree::LIBRARY_DOMAIN.to_string(),
        root: Some(tree.root()),
        edition: None,
    }
}

/// The book that sends `intended` to `tree`'s stream under the command id
/// `request_id` (a UUID's 16 bytes): the request's own id, so its one retry
/// is a DUPLICATE (AIP-155).
pub fn command_book(
    tree: &library_tree::TreeName,
    intended: &Intended,
    request_id: [u8; 16],
) -> angzarr_embedded::v1::CommandBook {
    use angzarr_embedded::v1::{command_page, Command, CommandPage, Uuid};
    let payload = match intended {
        Intended::Replace(command) => command.clone().to_wire_any(),
        Intended::Create(command) => command.clone().to_wire_any(),
        Intended::Trash(command) => command.clone().to_wire_any(),
        Intended::Move(command) => command.clone().to_wire_any(),
        Intended::Sequence(command) => command.clone().to_wire_any(),
    };
    angzarr_embedded::v1::CommandBook {
        cover: Some(cover_of(tree)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(payload),
                handling: None,
            })),
            command_id: Some(Uuid::from_bytes(request_id)),
            ..CommandPage::default()
        }),
    }
}

/// The book that sends the purge `purge` of a kept trash slot to `tree`'s
/// stream under the command id `id` (G12): one derived from the slot, so a
/// second purge of it is a DUPLICATE.
pub fn purge_book(
    tree: &library_tree::TreeName,
    purge: library_tree::PurgeTrash,
    id: [u8; 16],
) -> angzarr_embedded::v1::CommandBook {
    use angzarr_embedded::v1::{command_page, Command, CommandPage, Uuid};
    angzarr_embedded::v1::CommandBook {
        cover: Some(cover_of(tree)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(purge.to_wire_any()),
                handling: None,
            })),
            command_id: Some(Uuid::from_bytes(id)),
            ..CommandPage::default()
        }),
    }
}

/// A command as the `Any` its book carries.
trait WireAny {
    fn to_wire_any(self) -> angzarr_embedded::Any;
}

impl<M: angzarr_embedded::markup::DomainMessage> WireAny for M {
    fn to_wire_any(self) -> angzarr_embedded::Any {
        angzarr_embedded::Any::from_msg(&self.to_wire()).expect("a library command encodes")
    }
}

/// The book that records a step's end on `tree`'s stream: `fact` keyed
/// `{kind}:{request_id}:{step}`, so its redelivery is a DUPLICATE. (The
/// home step's `RecordHomeRepositoryMade` rides it too, keyed by its tree.)
pub fn step_book<M: angzarr_embedded::markup::DomainMessage>(
    tree: &library_tree::TreeName,
    fact: &M,
    key: &str,
) -> angzarr_embedded::v1::CommandBook {
    use angzarr_embedded::v1::{command_page, CommandPage, Fact, FactOrigin};
    angzarr_embedded::v1::CommandBook {
        cover: Some(cover_of(tree)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&fact.to_wire())
                        .expect("a step's fact encodes"),
                ),
                origin: Some(FactOrigin {
                    external_id: key.to_string(),
                    description: "a library write's step".to_string(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

#[cfg(test)]
#[path = "writes.test.rs"]
mod tests;
