//! What trusting a project would allow (step 8 U1, ruling 142): what its
//! own config layer says that a project's config may not set on its own say
//! (ruling 46's keys, D3), and its own `ctxloom` bundle, which replaces the
//! operational prompts (ruling 128, D4). The daemon's `GetTrust` previews
//! it, and its etag is what a grant names.

use library::config::engines;

use crate::error::AppError;
use crate::project::Library;

/// The path a project's own `ctxloom` bundle is named by in a preview.
pub const CTXLOOM_BUNDLE_PATH: &str = "bundles/ctxloom";

/// One thing trusting the project allows: its dotted path (or
/// `bundles/ctxloom`) and what it says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HeldBack {
    pub path: String,
    pub value: String,
}

impl Library {
    /// What trusting this project would allow: each project-only setting
    /// its own layer says, then its own `ctxloom` bundle, where it has one.
    /// Outside a project, nothing. A layer that does not read is refused as
    /// a run would refuse it.
    pub fn trust_settings(&self) -> Result<Vec<HeldBack>, AppError> {
        let mut held = Vec::new();
        let project = match self.files.source() {
            ctxloom_ports::Source::Project => self.files.project_layer_path(),
            _ => None,
        };
        let Some(project) = project else {
            return Ok(held);
        };
        let layers = self.files.layers().map_err(AppError::Cause)?;
        for layer in layers.iter().filter(|layer| layer.path == project) {
            let mut notices = Vec::new();
            let mut value = crate::config::read_layer(layer, &mut notices)?;
            held.extend(
                engines::strip_project_only(&mut value)
                    .into_iter()
                    .map(|(path, value)| HeldBack { path, value }),
            );
        }
        let bundle = library::builtin::BUNDLE;
        if self.content.has_root().map_err(AppError::Cause)?
            && self
                .content
                .bundle_names()
                .map_err(AppError::Cause)?
                .iter()
                .any(|name| name == bundle)
        {
            held.push(HeldBack {
                path: CTXLOOM_BUNDLE_PATH.to_string(),
                value: self.content.bundle_dir(bundle),
            });
        }
        Ok(held)
    }
}

#[cfg(test)]
#[path = "trust.test.rs"]
mod tests;
