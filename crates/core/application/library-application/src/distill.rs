//! `fragment|command|bundle distill` (plan step 5, finishes M3): an item's
//! body compressed by a model, stored beside it as its `.distilled.md`,
//! which records the source's digest (owner T30). The DAEMON owns the model
//! call, as a run (step 10b, owner ruling 2026-11-18), and the write
//! follows from the run's outcome; this is the use case's two halves the
//! daemon plans each with:
//!
//! - [`Library::item_distill_ask`]: what distilling an item is to ask (the
//!   catalog's `ctxloom#prompts/distill`, owner ruling 128, around the
//!   item's body), or that nothing is (its form fresh, or it is marked
//!   `no_distill`);
//! - [`Library::item_distilled`]: the run's answer judged and written, the
//!   item read again as it is now: refused when its source moved while the
//!   model was asked ([`AppError::DistillSourceMoved`]), or the answer is
//!   implausibly short ([`library::distill::implausibly_short`]). A refused
//!   answer writes nothing, so the form there before is kept, and an item
//!   never distilled stays raw.
//!
//! There is no `work` domain and no provenance (owner, 2026-11-13): the
//! file is the record.

use library::distill::{self, DistilledState};
use library::write::item;

use crate::error::AppError;
use crate::items::Kind;
use crate::project::{Library, Outcome, Project};
use crate::Flags;

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

/// What distilling an item is to ask, once read.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DistillAsk {
    /// Nothing to ask: the answer already (`fresh`, `no_distill`).
    Done(ItemDistilled),
    /// The model to ask.
    Model(ModelAsk),
}

/// What the model is asked for one item.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ModelAsk {
    /// What a distillation that is written comes to (`distilled`).
    pub done: ItemDistilled,
    /// The whole request: the distill prompt and the item's body
    /// ([`library::distill::request`]).
    pub request: String,
    /// The digest of the source the request was read from: what the
    /// distilled form records, and what the source must still be when the
    /// answer is written.
    pub digest: String,
}

/// The item a distillation reads, as it is now.
struct Source {
    done: ItemDistilled,
    content: String,
    digest: String,
    no_distill: bool,
    fresh: bool,
}

impl Library {
    /// `fragment distill <ref> [-f]`, `command distill <ref> [-f]`: what the
    /// model is to be asked of the item, unless it is marked `no_distill`
    /// (said, nothing asked: the author's word comes first), or its form
    /// already matches its source and `force` is not given.
    pub fn item_distill_ask(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        force: bool,
    ) -> Outcome<DistillAsk> {
        let builtins = self.builtins;
        self.run(flags, |p| {
            let source = p.distill_source(kind, reference)?;
            let done = |status: &str| ItemDistilled {
                status: status.to_string(),
                ..source.done.clone()
            };
            if source.no_distill {
                return Ok(DistillAsk::Done(done("no_distill")));
            }
            if !force && source.fresh {
                return Ok(DistillAsk::Done(done("fresh")));
            }
            let instruction = p.operational_prompt(library::builtin::DISTILL_NAME, builtins)?;
            Ok(DistillAsk::Model(ModelAsk {
                done: done("distilled"),
                request: distill::request(&instruction, &source.content),
                digest: source.digest,
            }))
        })
    }

    /// The model's `answer` for the item, asked when its source's digest
    /// was `digest`, written as its distilled form: refused, and nothing
    /// written, when its source is no longer what was sent, or the answer
    /// is implausibly short for it.
    pub fn item_distilled(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        (digest, answer): (&str, &str),
    ) -> Outcome<ItemDistilled> {
        self.run(flags, |p| {
            let source = p.distill_source(kind, reference)?;
            if source.digest != digest {
                return Err(AppError::DistillSourceMoved {
                    reference: reference.to_string(),
                });
            }
            if distill::implausibly_short(&source.content, answer) {
                return Err(AppError::DistillTruncated {
                    reference: reference.to_string(),
                    answer: answer.trim().chars().count() as u64,
                    source: source.content.trim().chars().count() as u64,
                });
            }
            let done = ItemDistilled {
                status: "distilled".to_string(),
                ..source.done
            };
            let mut plan = p.plan_of(&done.bundle)?;
            plan.write(&done.path, distill::distilled_file(digest, answer));
            p.commit(&done.bundle, plan)?;
            Ok(done)
        })
    }
}

impl Project {
    /// The item `reference` a distillation reads, from its bundle as it is
    /// now (a bundle that is not writable, the built-in, is refused).
    fn distill_source(&mut self, kind: Kind, reference: &str) -> Result<Source, AppError> {
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
        Ok(Source {
            done: ItemDistilled {
                status: String::new(),
                kind: kind.word().to_string(),
                bundle: entry.name.clone(),
                name: name.clone(),
                path: item::distilled_path(kind.tree_dir(), &name),
            },
            content: body.content.clone(),
            digest: body.source_digest.clone(),
            no_distill: body.no_distill,
            fresh: body.distilled_state() == DistilledState::Fresh,
        })
    }
}

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
