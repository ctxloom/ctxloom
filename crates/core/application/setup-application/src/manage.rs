//! `manage`: scaffolding the project, taking the harness back out, and
//! reporting what is wired (Go's `internal/adapters/cli/manage.go`).
//!
//! plan supersedes Go (8.2): `manage uninstall` is `materialize --release` at
//! the project's directory for every registered engine; only what the
//! ownership record says ctxloom wrote is removed.

use ctxloom_ports::Put;
use library::model::HOOK_EVENTS;
use library_application::{Flags, PackageRequest};
use serde::Serialize;
use surface_ports::Kind;

use crate::{Done, Setup, SetupError};

const NESTED_PATH: &str = ".ctxloom/.gitignore";

/// `manage install`.
#[derive(Clone, Debug, Default)]
pub struct InstallRequest {
    /// The engine to record when scaffolding; none is the default.
    pub engine: Option<String>,
    pub no_pull: bool,
    pub flags: Flags,
}

/// What the install's dependency pull did.
#[derive(Clone, Debug, Serialize)]
pub struct PullReport {
    /// `pulled`, `failed` or `skipped`.
    pub status: String,
    pub installed: u64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

/// What `manage install --print` says it would do.
#[derive(Clone, Debug, Serialize)]
pub struct InstallPlan {
    pub ctxloom_dir: String,
    pub ctxloom_dir_exists: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub engine: String,
    pub gitignore: String,
}

/// What `manage install` did.
#[derive(Clone, Debug, Serialize)]
pub struct InstallReport {
    pub app_dir: String,
    pub initialized: bool,
    pub gitignore: String,
    pub gitignore_status: String,
    pub pull: PullReport,
}

/// What `manage gitignore install` did.
#[derive(Clone, Debug, Serialize)]
pub struct GitignoreReport {
    pub status: String,
    pub path: String,
    pub nested_path: String,
    pub nested_written: bool,
}

/// What `manage uninstall` did.
#[derive(Clone, Debug, Serialize)]
pub struct UninstallReport {
    pub status: String,
    pub backends: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub errors: Vec<String>,
    pub note: String,
}

/// One engine's wiring.
#[derive(Clone, Debug, Serialize)]
pub struct BackendStatus {
    pub backend: String,
    pub settings_exists: bool,
    pub hooks_present: bool,
    /// Whether a launched session carries the engine's hooks itself (owner
    /// ruling 2026-11-12), so none at rest is not a hook missing.
    pub hooks_at_launch: bool,
    #[serde(rename = "statusline")]
    pub status_line: bool,
    pub mcp_present: bool,
}

/// A kind ctxloom has delivered, and whether the file still shows it.
#[derive(Clone, Debug, Serialize)]
pub struct SurfaceCurrency {
    pub route: String,
    pub backend: String,
    pub status: String,
}

/// What `manage check` shows.
#[derive(Clone, Debug, Serialize)]
pub struct CheckReport {
    pub work_dir: String,
    pub manage_statusline: bool,
    pub backends: Vec<BackendStatus>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub surfaces: Vec<SurfaceCurrency>,
}

/// One hook of `manage hooks list`.
#[derive(Clone, Debug, Serialize)]
pub struct HookRow {
    pub position: usize,
    /// Where the merge put it before any reorder.
    pub declared: usize,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub command: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub prompt: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub kind: String,
    pub source_kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source: String,
}

/// One event of `manage hooks list`.
#[derive(Clone, Debug, Serialize)]
pub struct HooksEvent {
    pub event: String,
    pub hooks: Vec<HookRow>,
}

/// What `manage hooks list` shows.
#[derive(Clone, Debug, Serialize)]
pub struct HooksList {
    pub events: Vec<HooksEvent>,
}

/// `--engine` asked of a project that was there already (`existed`) is
/// refused: the engine is recorded only while scaffolding it (Go's
/// `checkInstallEngineApplies`).
pub fn engine_applies(engine: Option<&str>, existed: bool) -> Result<(), SetupError> {
    match engine {
        Some(engine) if existed => Err(SetupError::EngineNotApplied {
            engine: engine.to_string(),
        }),
        _ => Ok(()),
    }
}

impl Setup {
    /// `manage install --engine`'s engine, refused unless this build
    /// knows it (none asked is the default's). `manage install` itself is
    /// the daemon's (step 10c U6, `InstallManage`).
    pub fn check_engine(&self, engine: Option<&str>) -> Result<(), SetupError> {
        let known = self.engines.names();
        match engine {
            Some(engine) if !known.iter().any(|k| k == engine) => Err(SetupError::UnknownEngine {
                engine: engine.to_string(),
                known: known.join(", "),
            }),
            _ => Ok(()),
        }
    }

    /// The pull `manage install` ends with, as it reports it: skipped with
    /// `--no-pull` or no puller, else pulled or failed (a failed pull is
    /// said, and the install stands).
    pub fn pull_report(&self, no_pull: bool) -> PullReport {
        self.pull(&InstallRequest {
            no_pull,
            ..InstallRequest::default()
        })
    }

    /// The steps `manage install` would take, without taking them.
    pub fn install_plan(&self, engine: Option<&str>) -> Result<InstallPlan, SetupError> {
        let exists = self
            .scaffold
            .exists("config.yaml")
            .map_err(SetupError::Cause)?;
        Ok(InstallPlan {
            ctxloom_dir: self.scaffold.app_dir(),
            ctxloom_dir_exists: exists,
            engine: if exists {
                String::new()
            } else {
                engine.unwrap_or("claude-code").to_string()
            },
            gitignore: format!("{}/.gitignore", self.project_dir),
        })
    }

    fn pull(&self, request: &InstallRequest) -> PullReport {
        let report = |status: &str, installed: u64, error: String| PullReport {
            status: status.to_string(),
            installed,
            error,
        };
        match (&self.puller, request.no_pull) {
            (_, true) | (None, _) => report("skipped", 0, String::new()),
            (Some(puller), false) => match puller.pull() {
                Ok(installed) => report("pulled", installed, String::new()),
                Err(error) => report("failed", 0, error),
            },
        }
    }

    /// `.ctxloom/.gitignore` written wholesale when it is not what ctxloom
    /// would write: whether it was.
    fn write_nested_gitignore(&self) -> Result<(String, bool), SetupError> {
        let want = library::write::scaffold::nested_gitignore();
        let held = self
            .store
            .read(&self.project_dir, NESTED_PATH)
            .map_err(SetupError::cause)?;
        if held.as_deref() == Some(want.as_bytes()) {
            return Ok(("unchanged".to_string(), false));
        }
        self.scaffold
            .write(&[Put::file(".gitignore", want)])
            .map_err(SetupError::Cause)?;
        Ok(("updated".to_string(), true))
    }

    /// `manage gitignore install`.
    pub fn gitignore_install(&self) -> Result<GitignoreReport, SetupError> {
        let (status, written) = self.write_nested_gitignore()?;
        Ok(GitignoreReport {
            status,
            path: format!("{}/.gitignore", self.project_dir),
            nested_path: format!("{}/{NESTED_PATH}", self.project_dir),
            nested_written: written,
        })
    }

    /// `manage uninstall`: everything an at-rest delivery wrote into the
    /// project's directory, for every engine, and nothing else.
    pub fn uninstall(&self) -> Result<UninstallReport, SetupError> {
        let mut backends = Vec::new();
        let mut errors = Vec::new();
        for name in self.engines.names() {
            let mut failed = false;
            for kind in Kind::ALL {
                if let Err(e) = self.store.release(&self.project_dir, &name, kind, false) {
                    failed = true;
                    errors.push(format!(
                        "{name}: {}",
                        angzarr_diagnostic::Diagnostic::code(&e)
                    ));
                }
            }
            if !failed {
                backends.push(name);
            }
        }
        Ok(UninstallReport {
            status: if errors.is_empty() {
                "released"
            } else {
                "partial"
            }
            .to_string(),
            backends,
            errors,
            note: "The .ctxloom directory and its contents were left in place.".to_string(),
        })
    }

    /// `manage check`: what is wired, read off the engines' own files.
    pub fn check(&self, flags: &Flags) -> Result<Done<CheckReport>, SetupError> {
        let outcome = self.library.package(flags, &PackageRequest::default());
        let statusline = outcome.result.map(|p| p.statusline).unwrap_or(true);
        let mut backends = Vec::new();
        let mut surfaces = Vec::new();
        for name in self.engines.names() {
            let Some(engine) = self.engines.get(&name) else {
                continue;
            };
            let wiring = engine
                .wiring(self.store.as_ref(), &self.project_dir)
                .map_err(SetupError::cause)?;
            backends.push(BackendStatus {
                backend: name.clone(),
                settings_exists: wiring.settings_exists,
                hooks_present: wiring.hooks_present,
                hooks_at_launch: engine.rides_launch(surface_ports::Kind::Hooks),
                status_line: wiring.statusline,
                mcp_present: wiring.mcp_present,
            });
            let claimed = self
                .store
                .claimed(&self.project_dir, &name)
                .map_err(SetupError::cause)?;
            let stale = self.stale_kinds(&name, &claimed, flags);
            for kind in claimed {
                let word = if stale.contains(&kind.name().to_string()) {
                    "stale"
                } else {
                    "materialized"
                };
                surfaces.push(SurfaceCurrency {
                    route: kind.name().to_string(),
                    backend: name.clone(),
                    status: word.to_string(),
                });
            }
        }
        Ok(Done {
            value: CheckReport {
                work_dir: self.project_dir.clone(),
                manage_statusline: statusline,
                backends,
                surfaces,
            },
            notices: outcome.notices,
        })
    }

    /// The kinds of `engine` an export in the project's directory holds that
    /// are no longer what `materialize --yes` would write there now (the
    /// default agent's profiles, every kind it claimed): a dry run of that
    /// materialize, which writes nothing, says which would change (be
    /// written anew, or taken out for want of anything to carry). An export
    /// whose currency cannot be told (the plan refused) is named stale by
    /// nothing.
    fn stale_kinds(&self, engine: &str, claimed: &[Kind], flags: &Flags) -> Vec<String> {
        if claimed.is_empty() {
            return Vec::new();
        }
        let request = crate::MaterializeRequest {
            yes: true,
            engines: vec![engine.to_string()],
            surfaces: claimed
                .iter()
                .map(|kind| crate::SurfaceSpec {
                    kind: *kind,
                    mechanism: String::new(),
                    dest: String::new(),
                })
                .collect(),
            dry_run: true,
            flags: flags.clone(),
            ..crate::MaterializeRequest::default()
        };
        self.materialize(&request)
            .map(|done| {
                done.value
                    .engines
                    .into_iter()
                    .flat_map(|outcome| outcome.wrote.into_iter().chain(outcome.released))
                    .collect()
            })
            .unwrap_or_default()
    }

    /// `manage hooks list`: the hooks that will fire, per event, in the order
    /// they fire in, with where each came from.
    pub fn hooks_list(
        &self,
        event: Option<&str>,
        profiles: &[String],
        flags: &Flags,
    ) -> Result<Done<HooksList>, SetupError> {
        if let Some(event) = event.filter(|e| !HOOK_EVENTS.contains(e)) {
            return Err(SetupError::UnknownEvent {
                event: event.to_string(),
                known: HOOK_EVENTS.join(", "),
            });
        }
        let outcome = self.library.package(
            flags,
            &PackageRequest {
                profiles: profiles.to_vec(),
            },
        );
        let package = outcome.result.map_err(SetupError::classed)?;
        let events = HOOK_EVENTS
            .iter()
            .filter(|e| event.is_none_or(|wanted| wanted == **e))
            .map(|name| HooksEvent {
                event: (*name).to_string(),
                hooks: package
                    .hooks
                    .iter()
                    .filter(|h| h.event == *name)
                    .map(|h| HookRow {
                        position: h.position,
                        declared: h.position,
                        command: h.hook.command.clone(),
                        args: h.hook.args.clone(),
                        prompt: h.hook.prompt.clone(),
                        kind: h.hook.kind.clone(),
                        source_kind: h.source_kind.clone(),
                        source: h.source.clone(),
                    })
                    .collect(),
            })
            .collect();
        Ok(Done {
            value: HooksList { events },
            notices: outcome.notices,
        })
    }

    /// `manage statusline install|uninstall`: whether ctxloom manages the HUD
    /// statusline; it takes effect at the next materialize or run.
    pub fn set_statusline(&self, enabled: bool, flags: &Flags) -> Result<Done<bool>, SetupError> {
        let outcome = self.library.set_statusline(flags, enabled);
        outcome.result.map_err(SetupError::classed)?;
        Ok(Done {
            value: enabled,
            notices: outcome.notices,
        })
    }
}

#[cfg(test)]
#[path = "manage.test.rs"]
mod tests;
