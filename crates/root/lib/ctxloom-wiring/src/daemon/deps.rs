//! The remotes', the lock's and the cache's writes, planned in the daemon
//! where the caller stands (step 10c U4): the deps use cases over a
//! RECORDING store, in a wiring of the caller's directory and environment,
//! so git runs on the caller's `PATH` with its `HOME` and credentials (the
//! remote-git adapter scrubs `GIT_*`, as ever), never in the daemon's. What
//! the store recorded is the daemon's sequence to write; each tree it
//! installed is told with git's own id of it, as remote-git reports it.

use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;

use daemon_api::{ApiError, DepsAnswer, DepsAsk, DepsPlanned, DepsTree};
use daemon_proto::v1;
use deps_application::{CreateRequest, Edit, Outcome};
use deps_files::{ProjectFiles, Recorded, RecordingDepsFiles};
use deps_ports::{DepsFiles, Remotes, Repository};
use surface_files::SurfaceFiles;

/// The deps use cases' writes, each planned as a process at the caller's
/// directory in the caller's environment would run it, with ctxloom's
/// `home`.
pub(crate) struct DepsWriters {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

/// A deps use case's notice as the API says it.
fn notice_of(notice: &deps_application::Notice) -> v1::Notice {
    super::foreign::notice_of(notice)
}

/// A deps use case's error said as a notice: its code, args and text.
fn notice_of_error(error: &deps_application::AppError) -> v1::Notice {
    super::foreign::notice_of(error)
}

/// `outcome`'s answer, or the call's refusal or failure; its notices as
/// the API says them.
fn answered<T>(outcome: Outcome<T>) -> Result<(T, Vec<v1::Notice>), ApiError> {
    let notices = outcome.notices.iter().map(notice_of).collect();
    let answer = outcome
        .result
        .map_err(|error| daemon_api::deps_error(&error))?;
    Ok((answer, notices))
}

/// The catalog a refresh's refusals are said from, as the CLI says them.
fn catalog() -> Result<presentation::Catalog, ApiError> {
    let mut sources = crate::catalog_sources();
    sources.extend([
        library::EN_US,
        library_application::EN_US,
        deps::EN_US,
        deps_application::EN_US,
        surface_ports::EN_US,
        setup_application::EN_US,
    ]);
    presentation::Catalog::en_us(&sources).map_err(|e| ApiError::Failed {
        detail: format!("{e:?}"),
    })
}

impl daemon_api::DepsPlans for DepsWriters {
    fn plan(
        &self,
        (dir, env): (&str, &[(String, String)]),
        flags: &library_application::Flags,
        parent: &str,
        ask: &DepsAsk,
    ) -> Result<DepsPlanned, ApiError> {
        let at = if dir.is_empty() {
            self.home.display().to_string()
        } else {
            dir.to_string()
        };
        let pairs = env
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()));
        let wiring = super::caller_wiring(&self.home, &self.temp, (&at, None), pairs);
        let (_, app) = wiring.locate();
        let files = Arc::new(ProjectFiles::new(wiring.fs(), app.path.clone()));
        let store = Arc::new(RecordingDepsFiles::over(Arc::clone(&files)));
        let failed = |e: &dyn coded_diagnostic::Diagnostic| ApiError::diagnosed(e);
        let deps = Rc::new(
            crate::commands::build_deps_over(&wiring, store.clone(), Arc::clone(&files))
                .map_err(|e| failed(&e))?,
        );
        let mut foreign = Vec::new();
        let answer = match ask {
            DepsAsk::Pull { force, lock, yes } => {
                let (answer, puts) = pull(&wiring, (&deps, flags, parent), (*force, *lock, *yes))?;
                foreign = puts;
                answer
            }
            DepsAsk::Upgrade { reference, apply } => {
                let outcome = match reference {
                    Some(reference) => deps.upgrade_only(reference, *apply),
                    None => deps.upgrade(*apply),
                };
                let (report, notices) = answered(outcome)?;
                DepsAnswer::Upgraded(v1::UpgradeDependenciesResponse {
                    notices,
                    ..deps_cli::wire::upgraded(&report, reference.as_deref())
                })
            }
            DepsAsk::CreateRemote { name, url, forge } => {
                let (created, notices) = answered(deps.remote_create(&CreateRequest {
                    name: name.clone(),
                    url: url.clone(),
                    forge: forge.clone(),
                }))?;
                DepsAnswer::Created(v1::CreateRemoteResponse {
                    notices,
                    ..deps_cli::wire::created(parent, &created, forge)
                })
            }
            DepsAsk::EditRemote {
                remote,
                name,
                url,
                forge,
            } => {
                let edit = Edit {
                    name: name.clone(),
                    url: url.clone(),
                    forge: forge.clone(),
                };
                let (edited, notices) = answered(deps.remote_edit(remote, &edit))?;
                DepsAnswer::Edited(v1::UpdateRemoteResponse {
                    notices,
                    ..deps_cli::wire::edited(parent, &edited)
                })
            }
            DepsAsk::RemoveRemote { name, apply } => {
                let (removed, notices) = answered(deps.remote_remove(name, *apply))?;
                DepsAnswer::Removed(v1::DeleteRemoteResponse {
                    notices,
                    ..deps_cli::wire::removed(parent, name, &removed)
                })
            }
            DepsAsk::DefaultRemote { name } => {
                let (set, notices) = answered(deps.remote_default(name.as_deref()))?;
                DepsAnswer::Defaulted(v1::SetDefaultRemoteResponse {
                    notices,
                    ..deps_cli::wire::defaulted(parent, &set)
                })
            }
        };
        let remotes = crate::commands::git_remotes(&wiring, Arc::clone(&files));
        planned(answer, (&store.take(), files.as_ref()), &remotes, foreign)
    }
}

/// `deps pull` planned over `deps` in `wiring`, its project refresh over
/// a recording surface store: its answer, and the engine files a consented
/// refresh would write (none where the project refused it, which the answer
/// says: the pull itself applies).
fn pull(
    wiring: &crate::Wiring,
    (deps, flags, parent): (
        &Rc<deps_application::Deps>,
        &library_application::Flags,
        &str,
    ),
    (force, lock, yes): (bool, bool, bool),
) -> Result<(DepsAnswer, Vec<daemon_api::ForeignPut>), ApiError> {
    let surfaces = Arc::new(SurfaceFiles::recording(
        wiring.fs(),
        wiring.home().join(".ctxloom/records"),
    ));
    let setup = crate::commands::build_setup_over(wiring, Rc::clone(deps), surfaces.clone())
        .map_err(|e| ApiError::diagnosed(&e))?;
    let refresher = crate::commands::SetupRefresh {
        setup: &setup,
        flags: flags.clone(),
    };
    let (pulled, notices) = answered(deps.pull_and_refresh(&refresher, force, lock, yes))?;
    let refresh_refusal = pulled.failure.as_ref().map(notice_of_error);
    let foreign = match refresh_refusal {
        Some(_) => Vec::new(),
        None => super::foreign::puts_of(&surfaces),
    };
    let catalog = catalog()?;
    let skipped = pulled
        .skipped
        .as_ref()
        .map(|refresh| deps_cli::wire::view(refresh, &catalog));
    let answer = DepsAnswer::Pulled(v1::PullDependenciesResponse {
        notices,
        refresh_refusal,
        ..deps_cli::wire::pulled(parent, &pulled.report, skipped.as_ref())
    });
    Ok((answer, foreign))
}

/// What `recorded` would write, as the daemon writes it: the registry and
/// the lock where they differ from the files (`on_disk`) as they are, each
/// tree installed with git's id of it (asked of `remotes`).
pub(crate) fn planned(
    answer: DepsAnswer,
    (recorded, on_disk): (&Recorded, &dyn DepsFiles),
    remotes: &dyn Remotes,
    foreign: Vec<daemon_api::ForeignPut>,
) -> Result<DepsPlanned, ApiError> {
    let changed = |written: &Option<String>, now: Result<Option<String>, String>| {
        let now = now.ok().flatten();
        written
            .as_ref()
            .filter(|text| now.as_ref() != Some(*text))
            .map(|text| text.as_bytes().to_vec())
    };
    let installs = recorded
        .installs
        .iter()
        .map(|install| {
            let bundle = install.slot.bundle.segments().join("/");
            let url = deps::reference::RemoteRef::parse(&install.slot.identity)
                .map(|remote| remote.url)
                .unwrap_or_default();
            let repo = Repository {
                url,
                cache: install.slot.repository.clone(),
            };
            let tree = remotes
                .bundle_tree(&repo, &install.sha, &bundle)
                .map_err(|why| ApiError::Failed { detail: why })?;
            Ok(DepsTree {
                path: install.path.clone(),
                files: install
                    .files
                    .iter()
                    .map(|(path, bytes, executable)| ctxloom_ports::Put {
                        path: path.clone(),
                        bytes: bytes.clone(),
                        executable: *executable,
                    })
                    .collect(),
                identity: install.slot.identity.clone(),
                sha: install.sha.clone(),
                tree,
            })
        })
        .collect::<Result<Vec<_>, ApiError>>()?;
    Ok(DepsPlanned {
        answer,
        registry: changed(&recorded.registry, on_disk.read_registry()),
        lock: changed(&recorded.lock, on_disk.read_lock()),
        installs,
        removed: recorded.removed.clone(),
        foreign,
    })
}

#[cfg(test)]
#[path = "deps.test.rs"]
mod tests;
