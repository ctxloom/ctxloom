//! The use case: read the files the checks need through the ports, run the
//! checks, and (with `fix`) remove the stale hook entries.

use std::path::Path;
use std::sync::Arc;

use auth_application::Auth;
use ctxloom_ports::{ConfigFiles, Executables, Failure, Scope, Source, UserFiles};
use serde::Serialize;

use crate::checks::{self, Check, Status};
use crate::hooks;

/// The report: every check, in order.
#[derive(Clone, PartialEq, Eq, Debug, Serialize)]
pub struct Report {
    pub checks: Vec<Check>,
}

impl Report {
    /// Whether any row failed: the one finding that makes doctor exit 20 (refused).
    pub fn failed(&self) -> bool {
        self.checks.iter().any(|c| c.status == Status::Fail)
    }
}

/// A stale hook entry `fix` took out.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Removed {
    pub file: String,
    pub event: String,
    pub sub: String,
}

/// The doctor, over the ports it reads.
pub struct Doctor {
    config: Arc<dyn ConfigFiles>,
    files: Arc<dyn UserFiles>,
    executables: Arc<dyn Executables>,
    auth: Auth,
    home: String,
    version: String,
}

impl Doctor {
    pub fn new(
        config: Arc<dyn ConfigFiles>,
        files: Arc<dyn UserFiles>,
        executables: Arc<dyn Executables>,
        auth: Auth,
        home: &str,
        version: &str,
    ) -> Doctor {
        Doctor {
            config,
            files,
            executables,
            auth,
            home: home.to_string(),
            version: version.to_string(),
        }
    }

    /// The project's root: the directory holding its `.ctxloom`; none when
    /// there is no project.
    fn root(&self) -> Option<String> {
        match self.config.source() {
            Source::Project => Path::new(&self.config.app_dir())
                .parent()
                .map(|p| p.display().to_string()),
            Source::Home => None,
        }
    }

    /// The text of the file at `path`; none where it is absent or unreadable.
    fn text(&self, path: &str) -> Option<String> {
        match self.files.exists(path) {
            Ok(true) => String::from_utf8(self.files.read_file(path).ok()?).ok(),
            _ => None,
        }
    }

    /// The settings files the engine reads, as `(shown path, text)` where
    /// they are there.
    fn present(&self, candidates: &[(String, String)]) -> Vec<(String, String)> {
        candidates
            .iter()
            .filter_map(|(shown, path)| Some((shown.clone(), self.text(path)?)))
            .collect()
    }

    /// `(shown, path)` of the engine's settings files: the project's, then
    /// the user's.
    fn settings_paths(&self) -> Vec<(String, String)> {
        let mut paths = Vec::new();
        if let Some(root) = self.root() {
            for name in [".claude/settings.json", ".claude/settings.local.json"] {
                paths.push((name.to_string(), format!("{root}/{name}")));
            }
        }
        paths.push((
            "~/.claude/settings.json".to_string(),
            format!("{}/.claude/settings.json", self.home),
        ));
        paths
    }

    /// `(shown, path)` of the files that register MCP servers.
    fn mcp_paths(&self) -> Vec<(String, String)> {
        let mut paths = Vec::new();
        if let Some(root) = self.root() {
            paths.push((".mcp.json".to_string(), format!("{root}/.mcp.json")));
        }
        paths.push((
            "~/.claude.json".to_string(),
            format!("{}/.claude.json", self.home),
        ));
        paths
    }

    /// The report; with `deps_only`, the machine-capability probes alone
    /// (true or false whether anything is configured).
    pub fn report(&self, deps_only: bool) -> Report {
        let gitconfig = self.text(&format!("{}/.gitconfig", self.home));
        let deps = checks::deps(self.executables.find("git").is_some(), gitconfig.as_deref());
        if deps_only {
            return Report { checks: vec![deps] };
        }
        let config = self
            .config
            .layer(Scope::Project)
            .ok()
            .flatten()
            .map(|layer| layer.text);
        let layers = self.layer_texts();
        let settings = self.present(&self.settings_paths());
        let mut stale = Vec::new();
        for (file, text) in &settings {
            stale.extend(hooks::stale(text).into_iter().map(|s| (file.clone(), s)));
        }
        let mcp = self.present(&self.mcp_paths());
        let mcp_and_settings: Vec<(String, String)> =
            mcp.into_iter().chain(settings.iter().cloned()).collect();
        let root = self.root();
        let gitignore = root
            .as_ref()
            .and_then(|root| self.text(&format!("{root}/.gitignore")));
        // Only a project has private state for git to see.
        let unignored = match &root {
            Some(root) => library::write::scaffold::unignored_private_state(
                gitignore.as_deref(),
                self.text(&format!("{root}/.ctxloom/.gitignore")).as_deref(),
            ),
            None => Vec::new(),
        };
        let refused = self.refusal();
        Report {
            checks: vec![
                checks::setup_marker(
                    root.is_some(),
                    config.as_deref(),
                    refused.as_ref().map(|(m, f)| (m.as_str(), f.as_str())),
                ),
                deps,
                checks::agents(config.as_deref()),
                checks::credentials(&layers, &self.auth),
                checks::version(&self.version),
                checks::hooks(&settings),
                checks::gitignore(gitignore.as_deref(), &unignored),
                checks::mcp_invocation(&mcp_and_settings),
                checks::stale_hooks(&stale),
                checks::ingestion(),
            ],
        }
    }

    /// What every run would refuse the config with, read as a run reads it
    /// (`library_application::load_config`): its en-US message and fix. A
    /// config that does not parse is the setup marker's own finding, a
    /// warning (the diagnosis goes on past it), so it is none here.
    fn refusal(&self) -> Option<(String, String)> {
        use angzarr_diagnostic::Diagnostic;
        let error = match library_application::load_config(
            self.config.as_ref(),
            &[],
            &library_application::Flags::default(),
            &mut Vec::new(),
        ) {
            Ok(_) | Err(library_application::AppError::ConfigUnparsable { .. }) => return None,
            Err(error) => error,
        };
        let catalog = library_application::EN_US.1;
        let message = English(catalog, &error).to_string();
        let fix = English(
            catalog,
            &angzarr_diagnostic::Report::new(format!("{}-fix", error.code())),
        )
        .to_string();
        // A code with no fix in the catalog renders as the code itself.
        let fix = if fix.starts_with(error.code()) {
            String::new()
        } else {
            fix
        };
        Some((message, fix))
    }

    /// The config files' texts, lowest layer first; none where they cannot
    /// be read.
    pub fn layer_texts(&self) -> Vec<String> {
        self.config
            .layers()
            .map(|layers| layers.into_iter().map(|layer| layer.text).collect())
            .unwrap_or_default()
    }

    /// `--fix`: take every stale hook entry out of the engine's project and
    /// user settings, and nothing else; what went.
    pub fn fix(&self) -> Result<Vec<Removed>, Failure> {
        let mut removed = Vec::new();
        for (shown, path) in self.settings_paths() {
            let Some(text) = self.text(&path) else {
                continue;
            };
            let Some((fixed, gone)) = hooks::strip(&text) else {
                continue;
            };
            self.files.write_file(&path, fixed.as_bytes(), true)?;
            removed.extend(gone.into_iter().map(|s| Removed {
                file: shown.clone(),
                event: s.event,
                sub: s.sub,
            }));
        }
        Ok(removed)
    }
}

/// A diagnostic in the en-US catalog `.0`.
struct English<'a>(&'a str, &'a dyn angzarr_diagnostic::Diagnostic);

impl std::fmt::Display for English<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        angzarr_diagnostic::catalog::render(self.0, self.1, f)
    }
}
