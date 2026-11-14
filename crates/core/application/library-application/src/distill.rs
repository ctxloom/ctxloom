//! `fragment|command|bundle distill` (plan step 5, finishes M3): an item's
//! body compressed by a model, stored beside it as its `.distilled.md`,
//! which records the source's digest (owner T30). The model is asked through
//! the [`Distiller`] port, which the composition root implements with a
//! one-shot run on no context (`run --no-context`, OD-38), the way init's
//! interview runs a session; the prompt is the catalog's
//! `ctxloom#prompts/distill` (owner ruling 128).
//!
//! A refused answer (implausibly short, [`library::distill::implausibly_short`])
//! and a failed distiller both fail the command and write nothing, so the
//! form there before is kept, and an item never distilled stays raw. There
//! is no `work` domain and no provenance (owner, 2026-11-13): the file is
//! the record.

use std::rc::Rc;

use angzarr_diagnostic::Diagnostic;
use library::distill::{self, DistilledState};
use library::write::item;

use crate::error::{AppError, Notice};
use crate::items::Kind;
use crate::project::{Library, Outcome, Project};
use crate::Flags;

/// What a distiller answered: the model's answer, or why there is none, and
/// what the run noticed on the way.
pub struct Distilled {
    pub answer: Result<String, Box<dyn Diagnostic>>,
    pub notices: Vec<Notice>,
}

/// The model that distills: given the whole request (the distill prompt and
/// the item's body, [`library::distill::request`]), its answer.
pub trait Distiller {
    fn distill(&self, flags: &Flags, request: &str) -> Distilled;
}

/// What one item's distillation came to.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ItemDistilled {
    /// `distilled`, `fresh` (its form already matched its source; nothing
    /// asked) or `no_distill` (the item is marked so; nothing asked).
    pub status: String,
    pub kind: String,
    pub bundle: String,
    pub name: String,
    /// The distilled form's path in the bundle's tree.
    pub path: String,
}

/// What `bundle distill` came to: each item's, fragments then commands.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BundleDistilled {
    pub bundle: String,
    pub items: Vec<ItemDistilled>,
}

/// What an item's distillation is to ask, once read.
enum Ask {
    /// Nothing to ask: the answer already.
    Done(ItemDistilled),
    /// The request, the digest of the source it was read from, and the body
    /// the answer is measured against.
    Model {
        done: ItemDistilled,
        request: String,
        digest: String,
        body: String,
    },
}

impl Library {
    /// The library distilling through `distiller`.
    pub fn with_distiller(mut self, distiller: Rc<dyn Distiller>) -> Library {
        self.distiller = Some(distiller);
        self
    }

    /// `fragment distill <ref> [-f]`, `command distill <ref> [-f]`: the
    /// item's distilled form made again from its source, unless it is marked
    /// `no_distill` (said, nothing asked: the author's word comes first), or
    /// its form already matches its source and `force` is not given.
    pub fn item_distill(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        force: bool,
    ) -> Outcome<ItemDistilled> {
        let builtins = self.builtins;
        let asked = self.run(flags, |p| p.distill_ask(kind, reference, force, builtins));
        let mut notices = asked.notices;
        let ask = match asked.result {
            Ok(ask) => ask,
            Err(error) => {
                return Outcome {
                    result: Err(error),
                    notices,
                }
            }
        };
        let result = self.distilled(flags, reference, ask, &mut notices);
        Outcome { result, notices }
    }

    /// `bundle distill <bundle> [-f]` (OD-39, by name): every fragment and
    /// command of the bundle, as [`Library::item_distill`] each; the first
    /// that fails fails the command, and what was distilled before it stays.
    pub fn bundle_distill(
        &self,
        flags: &Flags,
        bundle: &str,
        force: bool,
    ) -> Outcome<BundleDistilled> {
        let listed = self.run(flags, |p| {
            let entry = p.bundle(bundle)?;
            p.writable(&entry.name)?;
            let mut refs: Vec<(Kind, String)> = Vec::new();
            for name in entry.bundle.fragments.keys() {
                refs.push((Kind::Fragment, format!("{}#fragments/{name}", entry.name)));
            }
            for name in entry.bundle.commands.keys() {
                refs.push((Kind::Command, format!("{}#commands/{name}", entry.name)));
            }
            Ok((entry.name.clone(), refs))
        });
        let mut notices = listed.notices;
        let (name, refs) = match listed.result {
            Ok(listed) => listed,
            Err(error) => {
                return Outcome {
                    result: Err(error),
                    notices,
                }
            }
        };
        let mut items = Vec::new();
        for (kind, reference) in refs {
            let one = self.item_distill(flags, kind, &reference, force);
            notices.extend(one.notices);
            match one.result {
                Ok(done) => items.push(done),
                Err(error) => {
                    return Outcome {
                        result: Err(error),
                        notices,
                    }
                }
            }
        }
        Outcome {
            result: Ok(BundleDistilled {
                bundle: name,
                items,
            }),
            notices,
        }
    }

    /// The model asked, its answer judged, and the distilled form written.
    fn distilled(
        &self,
        flags: &Flags,
        reference: &str,
        ask: Ask,
        notices: &mut Vec<Notice>,
    ) -> Result<ItemDistilled, AppError> {
        let (done, request, digest, body) = match ask {
            Ask::Done(done) => return Ok(done),
            Ask::Model {
                done,
                request,
                digest,
                body,
            } => (done, request, digest, body),
        };
        let distiller = self
            .distiller
            .as_ref()
            .ok_or_else(|| AppError::DistillFailed {
                reference: reference.to_string(),
                cause: angzarr_diagnostic::Report::of(&NoDistiller),
            })?;
        let answered = distiller.distill(flags, &request);
        notices.extend(answered.notices);
        let answer = answered.answer.map_err(|cause| AppError::DistillFailed {
            reference: reference.to_string(),
            cause: angzarr_diagnostic::Report::of(cause.as_ref()),
        })?;
        if distill::implausibly_short(&body, &answer) {
            return Err(AppError::DistillTruncated {
                reference: reference.to_string(),
                answer: answer.trim().chars().count() as u64,
                source: body.trim().chars().count() as u64,
            });
        }
        let written = self.run(flags, |p| {
            let mut plan = p.plan_of(&done.bundle)?;
            plan.write(&done.path, distill::distilled_file(&digest, &answer));
            p.commit(&done.bundle, plan)
        });
        notices.extend(written.notices);
        written.result.map(|()| done)
    }
}

impl Project {
    /// What distilling the item `reference` is to ask, read from its bundle
    /// as it is now.
    fn distill_ask(
        &self,
        kind: Kind,
        reference: &str,
        force: bool,
        builtins: bool,
    ) -> Result<Ask, AppError> {
        self.require_project()?;
        let (bundle, name) = self.item_target(kind, reference)?;
        let entry = self.bundle(&bundle)?;
        self.writable(&entry.name)?;
        let body = match kind {
            Kind::Fragment => entry.bundle.fragments.get(&name).map(|f| &f.body),
            Kind::Command => entry.bundle.commands.get(&name).map(|c| &c.body),
        };
        let Some(body) = body else {
            let available: Vec<&str> = match kind {
                Kind::Fragment => entry.bundle.fragments.keys().map(String::as_str).collect(),
                Kind::Command => entry.bundle.commands.keys().map(String::as_str).collect(),
            };
            return Err(AppError::ItemMissing {
                kind: kind.word().to_string(),
                name,
                available: available.join(", "),
            });
        };
        let done = |status: &str| ItemDistilled {
            status: status.to_string(),
            kind: kind.word().to_string(),
            bundle: entry.name.clone(),
            name: name.clone(),
            path: item::distilled_path(kind.tree_dir(), &name),
        };
        // The author's own word first: nothing is asked of a distiller.
        if body.no_distill {
            return Ok(Ask::Done(done("no_distill")));
        }
        if !force && body.distilled_state() == DistilledState::Fresh {
            return Ok(Ask::Done(done("fresh")));
        }
        let instruction = self.operational_prompt(library::builtin::DISTILL_NAME, builtins)?;
        Ok(Ask::Model {
            done: done("distilled"),
            request: distill::request(&instruction, &body.content),
            digest: body.source_digest.clone(),
            body: body.content.clone(),
        })
    }
}

/// A library built with no distiller.
#[derive(Debug)]
pub(crate) struct NoDistiller;

impl std::fmt::Display for NoDistiller {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for NoDistiller {
    fn code(&self) -> &str {
        angzarr_diagnostic::code!("no-distiller")
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        Vec::new()
    }
}

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
