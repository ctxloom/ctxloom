//! `bundle create|edit|remove|export|import` (Go's `operations.CreateBundle`,
//! `UpdateBundle`, `DeleteBundle`, `ExportBundle`, `ImportBundle`).

use ctxloom_ports::Put;
use library::tree::Files;
use library::write::envelope::{edit_envelope, new_envelope, EnvelopeEdit};
use library::write::item::{self, FRAGMENTS, PROMPTS};
use library::write::mcp as mcpfile;

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::write::plan::Plan;
use crate::write::requests::BundleEditRequest;
use crate::write::views::{
    BundleCreated, BundleEdited, BundleExported, BundleImported, BundleMoved, BundleRemoved,
    Preview, Removal,
};
use crate::write::WriteError;
use crate::Flags;

impl Library {
    /// `bundle create <name> [-d description]`: the envelope, an example
    /// fragment and an example command, in a directory claimed in one step.
    pub fn bundle_create(
        &self,
        flags: &Flags,
        name: &str,
        description: &str,
    ) -> Outcome<BundleCreated> {
        self.run(flags, |p| p.bundle_create(name, description))
    }

    /// `bundle edit <name> ...`: metadata and items by flag; nothing to change
    /// says so and writes nothing.
    pub fn bundle_edit(&self, flags: &Flags, request: &BundleEditRequest) -> Outcome<BundleEdited> {
        self.run(flags, |p| p.bundle_edit(request))
    }

    /// `bundle remove <name> [--yes]`: without `--yes` what would go, and
    /// nothing goes; with it the bundle's whole tree and nothing else.
    pub fn bundle_remove(
        &self,
        flags: &Flags,
        name: &str,
        yes: bool,
    ) -> Outcome<Removal<BundleRemoved>> {
        self.run(flags, |p| p.bundle_remove(name, yes))
    }

    /// `bundle export <name> [dest-dir] [-o path]`: the whole tree, copied.
    pub fn bundle_export(
        &self,
        flags: &Flags,
        name: &str,
        dest_dir: &str,
        output: &str,
    ) -> Outcome<BundleExported> {
        self.run(flags, |p| p.bundle_export(name, dest_dir, output))
    }

    /// A local `bundle move <name> --to <dir>` (step 10b U9, QUESTIONS
    /// 1(a)): the project's own bundle moved whole into the project at
    /// `dir`, the same place below its `.ctxloom`, as ONE rename. A move
    /// never replaces what is there, so a bundle there is refused (a
    /// `--force` too); its own place is no destination.
    pub fn bundle_move_into(&self, flags: &Flags, name: &str, dir: &str) -> Outcome<BundleMoved> {
        self.run(flags, |p| p.bundle_move_into(name, dir))
    }

    /// `bundle import <path> [--force]`: a tree named by its directory or its
    /// `bundle.yaml`, copied whole under its own name; over a bundle that is
    /// there it refuses unless forced.
    pub fn bundle_import(&self, flags: &Flags, path: &str, force: bool) -> Outcome<BundleImported> {
        self.run(flags, |p| p.bundle_import(path, force))
    }
}

/// A path as its steps say it, `.` steps and separators' spelling taken
/// out (no filesystem asked): two names of one place compare equal.
fn lexical(path: &str) -> Vec<String> {
    path.split(['/', '\\'])
        .filter(|step| !step.is_empty() && *step != ".")
        .map(str::to_string)
        .collect()
}

/// Whether `name` can be a bundle's: a path of plain segments, nested names
/// allowed, none empty, `.` or `..`.
pub(crate) fn valid_bundle_name(name: &str) -> bool {
    !name.is_empty()
        && !name.starts_with('/')
        && !name.contains(['\\', '\0'])
        && name
            .split('/')
            .all(|p| !p.is_empty() && p != "." && p != ".." && !p.starts_with('.'))
}

fn add_names(
    plan: &mut Plan,
    names: &[String],
    kind: &str,
    path: impl Fn(&str) -> String,
    content: impl Fn(&str) -> Vec<(String, String)>,
    changes: &mut Vec<String>,
) -> Result<(), AppError> {
    for name in names {
        if !item::valid_name(name) {
            return Err(WriteError::BadItemName {
                kind: kind.to_string(),
                name: name.clone(),
            }
            .into());
        }
        if plan.has(&path(name)) {
            continue;
        }
        super::require_id(kind, name)?;
        for (file, text) in content(name) {
            plan.write(&file, text);
        }
        changes.push(format!("set {kind}: {name}"));
    }
    Ok(())
}

impl Project {
    fn bundle_create(&self, name: &str, description: &str) -> Result<BundleCreated, AppError> {
        self.require_project()?;
        let ports = self.content.clone();
        if !valid_bundle_name(name) {
            return Err(WriteError::BadBundleName {}.into());
        }
        super::require_id("bundle", name)?;
        let manifest = self.content.manifest_path(name);
        if !self.content.claim(name).map_err(AppError::Cause)? {
            return Err(WriteError::BundleExists { path: manifest }.into());
        }
        let mut plan = Plan::of(Files::new(), Default::default());
        plan.write("bundle.yaml", new_envelope("1.0.0", description));
        plan.write(
            &item::item_path(FRAGMENTS, "example"),
            item::seed_fragment(),
        );
        plan.write(&item::item_path(PROMPTS, "example"), item::seed_prompt());
        if let Err(error) = self.commit(name, plan) {
            // The claim is a directory nobody can use: give it back.
            let _ = ports.remove_bundle(name);
            return Err(error);
        }
        Ok(BundleCreated {
            status: "created".to_string(),
            name: name.to_string(),
            path: manifest,
        })
    }

    fn bundle_edit(&self, request: &BundleEditRequest) -> Result<BundleEdited, AppError> {
        self.require_project()?;
        let entry = self.bundle(&request.name)?;
        let name = entry.name.clone();
        let path = entry.manifest_path.clone();
        let mut plan = self.plan_of(&name)?;
        let manifest = plan.text("bundle.yaml").unwrap_or_default();
        let (text, mut changes) = edit_envelope(
            &manifest,
            &EnvelopeEdit {
                description: request.description.clone(),
                version: request.version.clone(),
                add_tags: request.add_tags.clone(),
                remove_tags: request.remove_tags.clone(),
            },
        )
        .map_err(AppError::cause)?;
        plan.write("bundle.yaml", text);
        add_names(
            &mut plan,
            &request.add_fragments,
            "fragment",
            |n| item::item_path(FRAGMENTS, n),
            |n| vec![(item::item_path(FRAGMENTS, n), item::placeholder_fragment(n))],
            &mut changes,
        )?;
        remove_items(
            &mut plan,
            &request.remove_fragments,
            "fragment",
            FRAGMENTS,
            &mut changes,
        );
        add_names(
            &mut plan,
            &request.add_prompts,
            "prompt",
            |n| item::item_path(PROMPTS, n),
            |n| vec![(item::item_path(PROMPTS, n), item::placeholder_prompt(n))],
            &mut changes,
        )?;
        remove_items(
            &mut plan,
            &request.remove_prompts,
            "prompt",
            PROMPTS,
            &mut changes,
        );
        add_names(
            &mut plan,
            &request.add_mcp,
            "mcp",
            mcpfile::content_path,
            |n| {
                vec![(
                    mcpfile::content_path(n),
                    mcpfile::content_file(&mcpfile::placeholder(n)),
                )]
            },
            &mut changes,
        )?;
        for name in &request.remove_mcp {
            if plan.remove(&mcpfile::content_path(name)) {
                changes.push(format!("removed mcp: {name}"));
            }
        }
        if changes.is_empty() {
            return Ok(BundleEdited {
                status: "no_changes".to_string(),
                name: request.name.clone(),
                changes,
                path,
            });
        }
        self.commit(&name, plan)?;
        Ok(BundleEdited {
            status: "updated".to_string(),
            name: request.name.clone(),
            changes,
            path,
        })
    }

    fn bundle_remove(&self, name: &str, yes: bool) -> Result<Removal<BundleRemoved>, AppError> {
        self.require_project()?;
        let entry = self.bundle(name)?;
        self.writable(&entry.name)?;
        if !yes {
            let b = &entry.bundle;
            let mut parts = Vec::new();
            for (n, what) in [
                (b.fragments.len(), "fragment(s)"),
                (b.commands.len(), "command(s)"),
                (b.mcp.len(), "MCP server(s)"),
                (b.skills.len(), "skill package(s)"),
                (b.profiles.len(), "profile(s)"),
            ] {
                if n > 0 {
                    parts.push(format!("{n} {what}"));
                }
            }
            let mut detail = if parts.is_empty() {
                Vec::new()
            } else {
                vec![parts.join(", ")]
            };
            // Owner ruling 2026-11-19: every profile and agent that includes
            // it is named, and a run of each is refused until it is edited.
            // DRAFT text.
            let dependents = self.bundle_dependents(&entry.name);
            for dependent in &dependents {
                detail.push(format!(
                    "{} {} includes it",
                    dependent.kind.word(),
                    dependent.name
                ));
            }
            if !dependents.is_empty() {
                detail.push("a run of each is refused until it is edited".to_string());
            }
            let mut preview = Preview::of(
                format!("bundle \"{name}\""),
                detail,
                format!("ctxloom bundle remove {name} --yes"),
            );
            preview.dependents = dependents;
            return Ok(Removal::Preview(preview));
        }
        let (dir, path) = (entry.name.clone(), entry.manifest_path.clone());
        self.content.remove_bundle(&dir).map_err(AppError::Cause)?;
        Ok(Removal::Done(BundleRemoved {
            status: "deleted".to_string(),
            name: name.to_string(),
            path,
        }))
    }

    fn bundle_export(
        &self,
        name: &str,
        dest_dir: &str,
        output: &str,
    ) -> Result<BundleExported, AppError> {
        if output.is_empty() && dest_dir.is_empty() {
            return Err(WriteError::ExportNeedsDest {}.into());
        }
        let ports = self.write_ports()?;
        let entry = self.bundle(name)?;
        let dest = if output.is_empty() {
            format!("{}/{}", dest_dir.trim_end_matches('/'), entry.name)
        } else {
            output.to_string()
        };
        if ports.user.exists(&dest).map_err(AppError::Cause)? {
            return Err(WriteError::ExportDestinationExists { path: dest }.into());
        }
        let plan = self.plan_to_read(&entry.name)?;
        let puts: Vec<Put> = plan
            .files()
            .iter()
            .map(|(path, bytes)| Put {
                path: path.clone(),
                bytes: bytes.clone(),
                executable: plan.is_executable(path),
            })
            .collect();
        ports
            .user
            .write_tree(&dest, &puts)
            .map_err(AppError::Cause)?;
        Ok(BundleExported {
            status: "exported".to_string(),
            name: name.to_string(),
            source: entry.manifest_path.clone(),
            dest,
        })
    }

    fn bundle_move_into(&self, name: &str, dir: &str) -> Result<BundleMoved, AppError> {
        self.require_project()?;
        let ports = self.write_ports()?;
        let entry = self.bundle(name)?;
        self.writable(&entry.name)?;
        let own = self.content.bundle_names().map_err(AppError::Cause)?;
        if !own.contains(&entry.name) {
            return Err(AppError::cause(library::CatalogError::BundleNotFound {
                ask: name.to_string(),
            }));
        }
        let app_dir = format!("{}/.ctxloom", dir.trim_end_matches(['/', '\\']));
        let dest = self.content.bundle_dir_in(&app_dir, &entry.name);
        if lexical(&dest) == lexical(&self.content.bundle_dir(&entry.name)) {
            return Err(WriteError::MoveDestinationItself { dest }.into());
        }
        if ports.user.exists(&dest).map_err(AppError::Cause)?
            || !self
                .content
                .move_into(&entry.name, &app_dir)
                .map_err(AppError::Cause)?
        {
            return Err(WriteError::MoveDestinationExists {
                dest,
                bundle: entry.name.clone(),
                dir: dir.to_string(),
            }
            .into());
        }
        Ok(BundleMoved {
            name: entry.name.clone(),
            source: entry.manifest_path.clone(),
            dest,
        })
    }

    fn bundle_import(&self, path: &str, force: bool) -> Result<BundleImported, AppError> {
        self.require_project()?;
        let ports = self.write_ports()?;
        let dir = import_dir(path);
        let files: Files = ports.user.read_tree(dir).map_err(AppError::Cause)?;
        if !files.contains_key("bundle.yaml") {
            return Err(WriteError::NotABundle {
                path: dir.to_string(),
            }
            .into());
        }
        let name = import_name(dir).to_string();
        if !valid_bundle_name(&name) || name == "." {
            return Err(WriteError::BadBundleName {}.into());
        }
        super::require_id("bundle", &name)?;
        let bundle = library::tree::read_tree(&name, &files).map_err(|cause| {
            WriteError::ImportUnreadable {
                path: dir.to_string(),
                cause: cause.to_string(),
            }
        })?;
        // A built-in bundle of the name is not there: the import makes the
        // project's own, which overrides it.
        let there = self
            .catalog
            .entries()
            .iter()
            .any(|e| e.name == name && !self.catalog.is_builtin(&name))
            || self.unreadable.contains_key(&name);
        let dest = self.content.bundle_dir(&name);
        if there && !force {
            return Err(WriteError::ImportExists {
                what: "bundle".to_string(),
                path: dest,
            }
            .into());
        }
        if there {
            // The bundle's tree as it is (a built-in's never: it is not
            // there), read first: one the import may not replace is said
            // before anything is planned.
            self.plan_of(&name)?;
        }
        // The new tree whole, every file imported, from nothing: a forced
        // import REPLACES the bundle (step 10b U9, T28: the old one gone,
        // then the new one made, which the daemon carries out as the
        // sequence trash, create, purge), never merges into it.
        let mut plan = Plan::of(Files::new(), Default::default());
        // The skills' metadata the import brings says what is executable,
        // never the modes of the files imported (owner ruling 2026-11-12).
        let exec = library::write::skill::declared_executables(&files);
        for (path, bytes) in &files {
            plan.put(path, bytes.clone(), exec.contains(path));
        }
        // The new tree reads before the old one is touched.
        library::tree::read_tree(&name, plan.files()).map_err(|cause| {
            AppError::BundleUnreadable {
                ask: name.clone(),
                cause: Box::new(cause),
            }
        })?;
        if there {
            self.content.remove_bundle(&name).map_err(AppError::Cause)?;
        }
        if !self.content.claim(&name).map_err(AppError::Cause)? {
            return Err(WriteError::ImportExists {
                what: "bundle".to_string(),
                path: dest,
            }
            .into());
        }
        if let Err(error) = self.commit(&name, plan) {
            let _ = self.content.remove_bundle(&name);
            return Err(error);
        }
        Ok(BundleImported {
            status: "imported".to_string(),
            source: dir.to_string(),
            dest,
            version: bundle.version.clone(),
            fragments: bundle.fragments.len() as u64,
            commands: bundle.commands.len() as u64,
            mcp: bundle.mcp.len() as u64,
        })
    }
}

fn remove_items(
    plan: &mut Plan,
    names: &[String],
    kind: &str,
    dir: &str,
    changes: &mut Vec<String>,
) {
    for name in names {
        let raw = plan.remove(&item::item_path(dir, name));
        let distilled = plan.remove(&item::distilled_path(dir, name));
        if raw || distilled {
            changes.push(format!("removed {kind}: {name}"));
        }
    }
}

/// The directory an import reads, from the path it was given (the bundle's
/// directory, or its `bundle.yaml`), either separator: the daemon joins a
/// relative one to the caller's directory as the platform joins, so on
/// Windows it is `C:\\...\\handoff/bundle.yaml` (CI, 2026-11-19).
fn import_dir(path: &str) -> &str {
    let typed = path.trim_end_matches(['/', '\\']);
    match typed.strip_suffix("bundle.yaml") {
        Some(parent) if parent.is_empty() || parent.ends_with(['/', '\\']) => {
            let parent = parent.trim_end_matches(['/', '\\']);
            if parent.is_empty() {
                "."
            } else {
                parent
            }
        }
        _ => typed,
    }
}

/// The bundle an import of `dir` makes: its last segment, either separator.
fn import_name(dir: &str) -> &str {
    dir.rsplit(['/', '\\']).next().unwrap_or(dir)
}

#[cfg(test)]
#[path = "bundles.test.rs"]
mod tests;
