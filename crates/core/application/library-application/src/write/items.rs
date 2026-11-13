//! `fragment|command create|remove|edit` (Go's `operations.AddItem`,
//! `DeleteItem`, `SetItemContent`, and the editor round trip of
//! `cli/item_crud.go`).

use library::tree::Files;
use library::write::envelope::new_envelope;
use library::write::item;
use library::CatalogError;

use crate::error::AppError;
use crate::items::Kind;
use crate::project::{Library, Outcome, Project};
use crate::write::bundles::valid_bundle_name;
use crate::write::editor::editor_command;
use crate::write::plan::Plan;
use crate::write::views::{ItemCreated, ItemEdited, ItemRemoved, Preview, Removal};
use crate::write::WriteError;
use crate::Flags;

impl Library {
    /// `fragment create <bundle> <name>`, `command create`: a placeholder in
    /// the bundle's tree.
    pub fn item_create(
        &self,
        flags: &Flags,
        kind: Kind,
        bundle: &str,
        name: &str,
    ) -> Outcome<ItemCreated> {
        self.run(flags, |p| p.item_create(kind, bundle, name))
    }

    /// `fragment create <bundle>#fragments/<name>`, `command create
    /// <bundle>#commands/<name>`: the editor opened on the template, and what
    /// it wrote stored (an editor that changes nothing leaves the template;
    /// one that empties it is refused). A bundle nobody has yet is made, bare,
    /// for the item: packaging content into a bundle of its own needs no
    /// `bundle create` first. Nothing is written until the editor is done.
    pub fn item_create_at(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
    ) -> Outcome<ItemCreated> {
        self.run(flags, |p| p.item_create_at(kind, reference))
    }

    /// `fragment remove <bundle>#fragments/<name> [--yes]`: the item and its
    /// distilled form; without `--yes` what would go, and nothing goes.
    pub fn item_remove(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        yes: bool,
    ) -> Outcome<Removal<ItemRemoved>> {
        self.run(flags, |p| p.item_remove(kind, reference, yes))
    }

    /// `fragment edit <ref> [--no-distill]`: the body through the editor. The
    /// front matter stays (its stale hash dropped) and so does the rest of
    /// the tree; a body that did not change, or came back empty, is not saved.
    /// It writes the `.md` alone (owner T29, T30): its distilled form, if it
    /// has one, is left and reads stale by its digest until `distill` runs.
    pub fn item_edit(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        no_distill: bool,
    ) -> Outcome<ItemEdited> {
        self.run(flags, |p| p.item_edit(kind, reference, no_distill))
    }
}

impl Project {
    fn item_create(&self, kind: Kind, bundle: &str, name: &str) -> Result<ItemCreated, AppError> {
        self.require_project()?;
        let entry = self.bundle(bundle)?;
        if !item::valid_name(name) {
            return Err(WriteError::BadItemName {
                kind: kind.word().to_string(),
                name: name.to_string(),
            }
            .into());
        }
        let mut plan = self.plan_of(&entry.name)?;
        let path = item::item_path(kind.tree_dir(), name);
        if plan.has(&path) {
            return Err(WriteError::ItemExists {
                kind: kind.word().to_string(),
                name: name.to_string(),
            }
            .into());
        }
        super::require_id(kind.word(), name)?;
        plan.write(&path, item::placeholder_fragment(name));
        self.commit(&entry.name, plan)?;
        Ok(ItemCreated {
            status: "created".to_string(),
            bundle: bundle.to_string(),
            name: name.to_string(),
            path: entry.manifest_path.clone(),
            kind: kind.word().to_string(),
            bundle_created: false,
        })
    }

    fn item_create_at(&self, kind: Kind, reference: &str) -> Result<ItemCreated, AppError> {
        self.require_project()?;
        let (bundle, name) = self.item_target(kind, reference)?;
        if !item::valid_name(&name) {
            return Err(WriteError::BadItemName {
                kind: kind.word().to_string(),
                name,
            }
            .into());
        }
        super::require_id(kind.word(), &name)?;
        let existing = match self.catalog.lookup(&bundle) {
            Ok(entry) => Some(entry),
            Err(CatalogError::BundleNotFound { .. } | CatalogError::ItemNotBundle { .. })
                if !self.unreadable.contains_key(&bundle) =>
            {
                None
            }
            Err(_) => Some(self.bundle(&bundle)?),
        };
        let path = item::item_path(kind.tree_dir(), &name);
        let (mut plan, manifest) = match existing {
            Some(entry) => {
                let plan = self.plan_of(&entry.name)?;
                if plan.has(&path) {
                    return Err(WriteError::ItemExists {
                        kind: kind.word().to_string(),
                        name,
                    }
                    .into());
                }
                (plan, entry.manifest_path.clone())
            }
            None => {
                if !valid_bundle_name(&bundle) {
                    return Err(WriteError::BadBundleName {}.into());
                }
                super::require_id("bundle", &bundle)?;
                let mut plan = Plan::of(Files::new(), Default::default());
                plan.write("bundle.yaml", new_envelope("1.0.0", ""));
                (plan, self.content.manifest_path(&bundle))
            }
        };
        let template = item::placeholder_fragment(&name);
        let ports = self.write_ports()?;
        let command = editor_command(&self.config, &self.environ);
        let edited = ports
            .editor
            .edit(&command, &format!("{name}.md"), &template)
            .map_err(AppError::Cause)?;
        if edited.trim().is_empty() {
            return Err(WriteError::EmptyEdit {
                kind: kind.word().to_string(),
                name,
            }
            .into());
        }
        plan.write(&path, edited);
        let bundle_created = existing.is_none();
        if bundle_created {
            if !self.content.claim(&bundle).map_err(AppError::Cause)? {
                return Err(WriteError::BundleExists { path: manifest }.into());
            }
            if let Err(error) = self.commit(&bundle, plan) {
                let _ = self.content.remove_bundle(&bundle);
                return Err(error);
            }
        } else {
            self.commit(&bundle, plan)?;
        }
        Ok(ItemCreated {
            status: "created".to_string(),
            bundle,
            name,
            path: manifest,
            kind: kind.word().to_string(),
            bundle_created,
        })
    }

    fn item_remove(
        &self,
        kind: Kind,
        reference: &str,
        yes: bool,
    ) -> Result<Removal<ItemRemoved>, AppError> {
        self.require_project()?;
        let (bundle, name) = self.item_target(kind, reference)?;
        let entry = self.bundle(&bundle)?;
        let mut plan = self.plan_of(&entry.name)?;
        let path = item::item_path(kind.tree_dir(), &name);
        if !plan.has(&path) {
            return Err(WriteError::ItemNotFound {
                kind: kind.word().to_string(),
                name,
            }
            .into());
        }
        if !yes {
            return Ok(Removal::Preview(Preview::of(
                format!("{} \"{name}\" from bundle \"{bundle}\"", kind.word()),
                Vec::new(),
                format!("ctxloom {} remove {reference} --yes", kind.word()),
            )));
        }
        plan.remove(&path);
        plan.remove(&item::distilled_path(kind.tree_dir(), &name));
        self.commit(&entry.name, plan)?;
        Ok(Removal::Done(ItemRemoved {
            status: "deleted".to_string(),
            bundle,
            name,
            path: entry.manifest_path.clone(),
            kind: kind.word().to_string(),
        }))
    }

    fn item_edit(
        &self,
        kind: Kind,
        reference: &str,
        no_distill: bool,
    ) -> Result<ItemEdited, AppError> {
        self.require_project()?;
        let (bundle, name) = self.item_target(kind, reference)?;
        let entry = self.bundle(&bundle)?;
        let names: Vec<&str> = match kind {
            Kind::Fragment => entry.bundle.fragments.keys().map(String::as_str).collect(),
            Kind::Command => entry.bundle.commands.keys().map(String::as_str).collect(),
        };
        if !names.contains(&name.as_str()) {
            return Err(AppError::ItemMissing {
                kind: kind.word().to_string(),
                name,
                available: names.join(", "),
            });
        }
        let ports = self.write_ports()?;
        let mut plan = self.plan_of(&entry.name)?;
        let path = item::item_path(kind.tree_dir(), &name);
        let held = plan.text(&path).unwrap_or_default();
        let body = item::body_of(&held).to_string();
        let command = editor_command(&self.config, &self.environ);
        let edited = ports
            .editor
            .edit(&command, &format!("{name}.md"), &body)
            .map_err(AppError::Cause)?;
        let word = kind.word().to_string();
        let done = |status: &str, path: String, skipped: bool| ItemEdited {
            status: status.to_string(),
            bundle: bundle.clone(),
            name: name.clone(),
            path,
            distilled: false,
            kind: word.clone(),
            reference: reference.to_string(),
            distill_skipped: skipped,
        };
        if edited == body {
            return Ok(done("unchanged", String::new(), false));
        }
        if edited.trim().is_empty() {
            return Err(WriteError::EmptyEdit { kind: word, name }.into());
        }
        let marked = item::marked_no_distill(&held);
        // The `.md` alone (owner T29, T30): a distilled form beside it is left
        // as it is, and reads stale by its digest until `distill` runs again.
        plan.write(&path, item::replace_body(&held, &edited));
        self.commit(&entry.name, plan)?;
        Ok(done(
            "updated",
            entry.manifest_path.clone(),
            no_distill && !marked,
        ))
    }
}

#[cfg(test)]
#[path = "items.test.rs"]
mod tests;
