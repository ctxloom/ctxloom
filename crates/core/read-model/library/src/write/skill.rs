//! A skill package in a bundle's tree (Go's `bundles/skill.go`,
//! `surface_skill.go`): the directory `skills/<n>/` is the skill, and its
//! metadata `skills/<n>/.meta.yaml`, a file of the directory delivered with
//! it (owner T27), says what the rest cannot: its tags and notes and which
//! files are executable. A file's mode bit is not portable; the metadata's
//! `executable:` is the whole of it (plan §8.2), on
//! every OS (owner ruling 2026-11-12): no mode is read from disk or from git,
//! by `skill show`, an export, an import, an installed or a fetched bundle,
//! or a pin's disclosure, and a push commits the declared files 100755.

use std::collections::BTreeSet;

use angzarr_yaml::edit::Document;
use angzarr_yaml::Value;

use super::yamledit::drop_key;
use crate::tree::SKILL_META;
use crate::BundleError;

/// The description `skill create` writes when none is given.
pub const DEFAULT_DESCRIPTION: &str = "TODO: describe when an agent should use this skill.";

/// `skills/<name>`.
pub fn package_dir(name: &str) -> String {
    format!("skills/{name}")
}

/// `skills/<name>/.meta.yaml`.
pub fn meta_path(name: &str) -> String {
    format!("{}/{SKILL_META}", package_dir(name))
}

/// The metadata a new skill starts with: the empty mapping (owner T27).
pub const EMPTY_META: &str = "{}\n";

/// The `SKILL.md` a new skill starts as.
pub fn skill_md(name: &str, description: &str) -> String {
    let front = Value::Map(vec![
        ("name".to_string(), Value::Scalar(name.to_string())),
        (
            "description".to_string(),
            Value::Scalar(description.to_string()),
        ),
    ]);
    format!(
        "---\n{}---\n\n# {name}\n\nTODO: describe what this skill does and how to use it.\n",
        angzarr_yaml::write(&front, usize::MAX)
    )
}

/// The files the metadata declares executable; none for metadata that is
/// absent, empty or does not say.
pub fn executable_files(sidecar: Option<&str>) -> Vec<String> {
    sidecar
        .and_then(|text| angzarr_yaml::parse(text).ok())
        .and_then(|doc| {
            doc.get("executable").and_then(Value::as_seq).map(|items| {
                items
                    .iter()
                    .filter_map(|i| i.as_str().map(ToString::to_string))
                    .collect()
            })
        })
        .unwrap_or_default()
}

/// The files of a bundle's tree (`files`, by path in the bundle) that its
/// skills' metadata (`skills/<n>/.meta.yaml`) declares executable, by path
/// in the bundle: on every OS the metadata's `executable:` is the whole of
/// it, never a mode bit (owner ruling 2026-11-12). A listed file the tree
/// does not hold is not one.
pub fn declared_executables(files: &crate::tree::Files) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for (path, bytes) in files {
        let Some(name) = path
            .strip_prefix("skills/")
            .and_then(|rest| rest.strip_suffix(SKILL_META))
            .and_then(|rest| rest.strip_suffix('/'))
        else {
            continue;
        };
        if name.is_empty() || name.contains('/') {
            continue;
        }
        let text = String::from_utf8_lossy(bytes);
        for listed in executable_files(Some(&text)) {
            let file = format!("{}/{listed}", package_dir(name));
            if files.contains_key(&file) {
                out.insert(file);
            }
        }
    }
    out
}

fn unwritable(error: angzarr_yaml::edit::EditError) -> BundleError {
    BundleError::BadValue {
        path: format!("skills metadata ({SKILL_META})"),
        detail: format!("cannot be edited in place: {:?}", error.problem),
    }
}

/// The metadata with its `executable:` list set to `files` (sorted); every
/// other key stays as written, and a list left empty takes the key. The empty
/// mapping when nothing is left to say: a skill keeps its metadata file.
pub fn with_executable(meta: Option<&str>, files: &[String]) -> Result<String, BundleError> {
    let mut sorted: Vec<String> = files.to_vec();
    sorted.sort();
    sorted.dedup();
    let text = meta.unwrap_or_default();
    let mut doc = Document::parse(text).map_err(|cause| BundleError::Syntax { cause })?;
    let list = Value::Seq(sorted.iter().cloned().map(Value::Scalar).collect());
    let empty_map = matches!(doc.value(), Value::Map(entries) if entries.is_empty());
    if matches!(doc.value(), Value::Null) || empty_map {
        if sorted.is_empty() {
            return Ok(EMPTY_META.to_string());
        }
        // A blank document's comments stay; an empty mapping is replaced.
        let kept = if empty_map { "" } else { text };
        let entry = Value::Map(vec![("executable".to_string(), list)]);
        return Ok(format!("{kept}{}", angzarr_yaml::write(&entry, usize::MAX)));
    }
    if sorted.is_empty() {
        if doc.value().get("executable").is_some() {
            drop_key(&mut doc, "executable").map_err(unwritable)?;
        }
    } else {
        doc.set(&[], "executable", &list, usize::MAX)
            .map_err(unwritable)?;
    }
    let left = match doc.value() {
        Value::Map(entries) => !entries.is_empty(),
        _ => false,
    };
    if !left {
        return Ok(EMPTY_META.to_string());
    }
    let mut out = doc.text();
    if !out.ends_with('\n') {
        out.push('\n');
    }
    Ok(out)
}

#[cfg(test)]
#[path = "skill.test.rs"]
mod tests;
