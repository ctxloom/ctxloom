//! A skill package in a bundle's tree (Go's `bundles/skill.go`,
//! `surface_skill.go`): the directory `skills/<n>/` is the skill, and the
//! sidecar `skills/.<n>.meta.yaml` says what the directory cannot: its tags
//! and notes and which files are executable. A file's mode bit is not
//! portable; the sidecar's `executable:` is the whole of it (plan §8.2).

use std::collections::BTreeSet;

use angzarr_yaml::edit::Document;
use angzarr_yaml::Value;

use super::yamledit::drop_key;
use crate::frontmatter::{is_meta_path, meta_path_for_name, stem_of};
use crate::BundleError;

/// The description `skill create` writes when none is given.
pub const DEFAULT_DESCRIPTION: &str = "TODO: describe when an agent should use this skill.";

/// `skills/<name>`.
pub fn package_dir(name: &str) -> String {
    format!("skills/{name}")
}

/// `skills/.<name>.meta.yaml`.
pub fn sidecar_path(name: &str) -> String {
    meta_path_for_name("skills", name)
}

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

/// The files the sidecar declares executable; none for a sidecar that is
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
/// skills' sidecars declare executable, by path in the bundle: on every OS
/// the sidecar's `executable:` is the whole of it, never a mode bit (owner
/// ruling 2026-11-12). A listed file the tree does not hold is not one.
pub fn declared_executables(files: &crate::tree::Files) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for (path, bytes) in files {
        let Some(rest) = path.strip_prefix("skills/") else {
            continue;
        };
        if rest.contains('/') || !is_meta_path(rest) {
            continue;
        }
        let name = stem_of(rest);
        let text = String::from_utf8_lossy(bytes);
        for listed in executable_files(Some(&text)) {
            let file = format!("{}/{listed}", package_dir(&name));
            if files.contains_key(&file) {
                out.insert(file);
            }
        }
    }
    out
}

fn unwritable(error: angzarr_yaml::edit::EditError) -> BundleError {
    BundleError::BadValue {
        path: "skills sidecar".to_string(),
        detail: format!("cannot be edited in place: {:?}", error.problem),
    }
}

/// The sidecar with its `executable:` list set to `files` (sorted); every
/// other key stays as written, and a list left empty takes the key. None when
/// nothing is left to say.
pub fn with_executable(
    sidecar: Option<&str>,
    files: &[String],
) -> Result<Option<String>, BundleError> {
    let mut sorted: Vec<String> = files.to_vec();
    sorted.sort();
    sorted.dedup();
    let text = sidecar.unwrap_or_default();
    let mut doc = Document::parse(text).map_err(|cause| BundleError::Syntax { cause })?;
    let list = Value::Seq(sorted.iter().cloned().map(Value::Scalar).collect());
    if matches!(doc.value(), Value::Null) {
        if sorted.is_empty() {
            return Ok(None);
        }
        let entry = Value::Map(vec![("executable".to_string(), list)]);
        return Ok(Some(format!(
            "{text}{}",
            angzarr_yaml::write(&entry, usize::MAX)
        )));
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
    Ok(left.then(|| {
        let mut out = doc.text();
        if !out.ends_with('\n') {
            out.push('\n');
        }
        out
    }))
}

#[cfg(test)]
#[path = "skill.test.rs"]
mod tests;
