//! The library's reads the daemon serves (step 10a U2; D12): each request
//! of `daemon_proto::library_reads!` names what it reads (a Get's `name`,
//! a List's `parent`) under `projects/{harp}` or `users/me`, and carries
//! the caller's directory, environment and global flags (E1). A read:
//!
//! 1. resolves its project, by its harp, to the project's registered root,
//!    and the directory it is read at: the caller's (`cwd`), else that
//!    root;
//! 2. finds the checkout there as a process in that directory would (a
//!    git worktree of the project is a checkout of its own, L2) and
//!    observes it and the home's library first (U2's reconcile: a tree
//!    outside git, or one git ignores, refuses the read);
//! 3. opens the library there in the caller's environment, under its
//!    globals and the project's trust as the daemon's index holds it (U7,
//!    D7: an untrusted project's own project-only settings are held back
//!    and said), and answers by `library-api`'s use case of the read.
//!
//! What the root composes is [`Libraries`]: the library as a process at a
//! directory, in an environment, opens it.

use daemon_proto::v1;
use daemon_proto::Carries as _;
use deps_application::Deps;
use library_application::{AppError, Flags, Library, Outcome};
use run_ports::Edge;
use sessions::ProjectName;

use crate::refusal::{ApiError, ApiRefusal};
use crate::server::Api;

/// The library, as a process at a directory opens it (the root's).
pub trait Libraries: Send + Sync {
    /// The root of the checkout a process at `dir`, in the environment
    /// `env`, finds (its `.ctxloom`'s directory); none outside a project.
    fn root(&self, dir: &str, env: &[(String, String)]) -> Option<String>;

    /// `read` over the library a process at `dir`, in `env`, opens.
    fn read(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&Library),
    ) -> Result<(), ApiError>;

    /// `read` over the project's dependencies (its remotes and its lock)
    /// a process at `dir`, in `env`, opens (step 10a U2's deps reads).
    fn read_deps(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&Deps),
    ) -> Result<(), ApiError>;
}

/// What every library read request carries besides its own fields.
/// (its caller: [`daemon_proto::Carries`]).
pub trait LibraryRequest: daemon_proto::Carries {
    /// What it reads: a resource name, or a parent.
    fn target(&self) -> &str;
}

macro_rules! library_requests {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            impl LibraryRequest for v1::$request {
                fn target(&self) -> &str {
                    &self.$target
                }
            }
        )*
    };
}
daemon_proto::library_reads!(library_requests);
library_requests!(
    assemble_context (AssembleContext) AssembleContextRequest.name => AssembledContext,
    create_run (CreateRun) CreateRunRequest.parent => Run,
);

/// The parent a read's target is under: `projects/{harp}` (its project) or
/// `users/me` (none), the first two segments of its name.
pub(crate) fn parent_of(target: &str) -> Option<(String, Option<ProjectName>)> {
    let mut segments = target.splitn(3, '/');
    let parent = match (segments.next(), segments.next()) {
        (Some("users"), Some("me")) => return Some(("users/me".to_string(), None)),
        // An empty harp is refused by the parse below.
        (Some("projects"), Some(harp)) => format!("projects/{harp}"),
        _ => return None,
    };
    let name = ProjectName::parse(&parent).ok()?;
    Some((parent, Some(name)))
}

/// The global flags of a read, as the library's use cases take them: never
/// trusted, which is the daemon's own verdict ([`Api::read_site`]).
pub(crate) fn flags_of(globals: Option<&v1::Globals>) -> Flags {
    let globals = globals.cloned().unwrap_or_default();
    Flags {
        degraded: globals.degraded,
        config_set: globals.config_set,
        no_companions: globals.no_companions,
        trusted: false,
    }
}

/// A read's environment as name and value pairs.
pub(crate) fn pairs(env: Option<&v1::EnvVars>) -> Vec<(String, String)> {
    env.iter()
        .flat_map(|env| env.vars.iter())
        .map(|var| (var.name.clone(), var.value.clone()))
        .collect()
}

/// A use case's error as the read's answer: its refusal or failure.
pub(crate) fn read_error(error: &AppError) -> ApiError {
    crate::refusal::app_error(error)
}

impl<E: Edge> Api<E> {
    /// Answer `request` (step 10a U2): its project resolved, its trees
    /// observed, then `answer` over the library at its directory, under
    /// the parent it was asked under. Its notices are the observation's
    /// and the use case's.
    pub(crate) async fn library_read<Q: LibraryRequest, R>(
        &self,
        request: &Q,
        answer: impl FnOnce(&Library, &Flags, &str) -> Outcome<R>,
    ) -> Result<daemon_ports::Read<R>, ApiError> {
        let site = self.read_site(request).await?;
        let mut answer = Some(answer);
        let mut outcome = None;
        site.libraries.read(&site.at, &site.env, &mut |library| {
            if let Some(answer) = answer.take() {
                outcome = Some(answer(library, &site.flags, &site.parent));
            }
        })?;
        let outcome = outcome.ok_or_else(|| ApiError::Failed {
            detail: "the library was not read".to_string(),
        })?;
        let mut said = site.said;
        said.extend(
            outcome
                .notices
                .iter()
                .map(run_application::client::notice_of),
        );
        let answer = outcome.result.map_err(|error| read_error(&error))?;
        Ok(daemon_ports::Read {
            answer,
            notices: said,
        })
    }

    /// Answer a deps read (`remote list`, `deps list`; step 10a U2) as
    /// [`Api::library_read`] answers a library read: its project resolved,
    /// its trees observed, then `answer` over the project's dependencies
    /// at its directory, under the parent it was asked under.
    pub(crate) async fn deps_read<Q: LibraryRequest, R>(
        &self,
        request: &Q,
        answer: impl FnOnce(&Deps, &str) -> deps_application::Outcome<R>,
    ) -> Result<daemon_ports::Read<R>, ApiError> {
        let site = self.read_site(request).await?;
        let mut answer = Some(answer);
        let mut outcome = None;
        site.libraries.read_deps(&site.at, &site.env, &mut |deps| {
            if let Some(answer) = answer.take() {
                outcome = Some(answer(deps, &site.parent));
            }
        })?;
        let outcome = outcome.ok_or_else(|| ApiError::Failed {
            detail: "the dependencies were not read".to_string(),
        })?;
        let mut said = site.said;
        said.extend(outcome.notices.iter().map(|notice| v1::Notice {
            code: coded_diagnostic::Diagnostic::code(notice).to_string(),
            args: run_application::client::flat_args(notice),
            text: notice.to_string(),
        }));
        let answer = outcome
            .result
            .map_err(|error| crate::refusal::deps_error(&error))?;
        Ok(daemon_ports::Read {
            answer,
            notices: said,
        })
    }

    /// `request`'s trees observed (step 10 U11: nothing in the daemon reads
    /// a library tree outside the observation), as a read's are, before a
    /// run it asks for is planned outside this crate (a hosted one-shot, a
    /// terminal run); what the observation said. A daemon that reads no
    /// library has none to observe.
    pub(crate) async fn observe_for<Q: LibraryRequest>(
        &self,
        request: &Q,
    ) -> Result<Vec<v1::Notice>, ApiError> {
        if self.libraries.is_none() {
            return Ok(Vec::new());
        }
        Ok(self.read_site(request).await?.said)
    }

    /// Where a read is answered: its parent, the directory and environment
    /// it is read at, its flags, and what its observation said.
    async fn read_site<Q: LibraryRequest>(&self, request: &Q) -> Result<ReadSite<'_>, ApiError> {
        let (mut site, project) = self
            .resolve(
                request.target(),
                request.caller_cwd(),
                request.caller_env(),
                request.caller_globals(),
            )
            .await?;
        site.said = self
            .observed(
                project
                    .as_ref()
                    .map(|(name, root)| (name.clone(), root.as_str())),
            )
            .await?;
        Ok(site)
    }

    /// Where a library call is answered, before anything is observed: the
    /// site, and the project and checkout root its trees are observed at.
    pub(crate) async fn resolve(
        &self,
        target: &str,
        cwd: &str,
        env: Option<&v1::EnvVars>,
        globals: Option<&v1::Globals>,
    ) -> Result<(ReadSite<'_>, Option<(ProjectName, String)>), ApiError> {
        let (parent, project) = parent_of(target).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: target.to_string(),
                reason: "shape".to_string(),
            }))
        })?;
        let libraries = self.libraries.as_deref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon reads no library".to_string(),
        })?;
        let env = pairs(env);
        let registered = match &project {
            Some(name) => Some(self.registered_project(name).await?),
            None => None,
        };
        // Step 10a U7 (D7): the project's trust, as the daemon's index
        // holds it, gates the read; never the client's to say. A read under
        // `users/me` names no project, so it is untrusted.
        let trusted = registered.as_ref().is_some_and(|(_, id)| self.trusts(id));
        let registered = registered.map(|(root, _)| root);
        let dir = match (cwd, &registered) {
            (cwd, _) if !cwd.is_empty() => cwd.to_string(),
            (_, Some(root)) => root.clone(),
            (_, None) => String::new(),
        };
        let root = libraries.root(&dir, &env);
        let observed = project.zip(root.clone().or(registered));
        Ok((
            ReadSite {
                libraries,
                parent,
                at: root.unwrap_or(dir),
                env,
                flags: Flags {
                    trusted,
                    ..flags_of(globals)
                },
                said: Vec::new(),
            },
            observed,
        ))
    }
}

/// Where a read is answered ([`Api::read_site`]).
pub(crate) struct ReadSite<'a> {
    pub(crate) libraries: &'a dyn Libraries,
    pub(crate) parent: String,
    pub(crate) at: String,
    pub(crate) env: Vec<(String, String)>,
    pub(crate) flags: Flags,
    pub(crate) said: Vec<v1::Notice>,
}

impl<E: Edge> Api<E> {
    /// `CreateRun` with `validate_only`, `run --dry-run` (step 10a U2):
    /// the run planned as a library read is answered (its trees observed
    /// first, so a library outside git is refused), at the caller's
    /// directory, in its environment, under the project's trust as every
    /// read is (step 10a U7; never the client's to say, step 8 U2), and judged
    /// as the runs aggregate judges an enqueue on the untrusted project
    /// settings it reads (owner answer H: `runs-untrusted-project-settings`,
    /// beside the plan's fatal findings). Nothing is enqueued, minted or
    /// started. A project nothing has named (asked under `users/me` from
    /// inside it) keeps its names unresolved (owner ruling 2026-11-14).
    pub async fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, ApiError> {
        let project = self.host_project(&request.parent).await?;
        let unnamed = project.is_none()
            && !request.caller_cwd().is_empty()
            && self
                .libraries
                .as_ref()
                .and_then(|libraries| {
                    libraries.root(request.caller_cwd(), &pairs(request.caller_env()))
                })
                .is_some();
        let (asked, _) = run_application::client::asked_of(&request);
        let read = self
            .library_read(&request, |library, flags, _| {
                library.dry_run(flags, &asked.run)
            })
            .await?;
        let previewed = read.answer;
        let plan = &previewed.plan;
        if !plan.untrusted.is_empty() {
            let checks = runs::EnqueueRun {
                session: String::new(),
                incarnation: 1,
                mode: runs::RunMode::Interactive,
                prompt_given: true,
                engine: plan.engine.clone(),
                drivable_engines: vec![plan.engine.clone()],
                credential: runs::CredentialEvidence {
                    file_only: false,
                    builtin_variables: Vec::new(),
                    accepted_variables: Vec::new(),
                    accepted_set: false,
                },
                fatal_findings: u32::try_from(previewed.fatal.len()).unwrap_or(u32::MAX),
                fatal: Vec::new(),
                launcher_lock: String::new(),
                grant: None,
                untrusted_settings: plan.untrusted.clone(),
            };
            if let Some((refusal, status)) = runs::pre_launch_refusal(&checks) {
                return Err(ApiRefusal::typed(&refusal, status).into());
            }
        }
        let parent = (!unnamed).then_some(request.parent.as_str());
        Ok(v1::Run {
            notices: read.notices,
            ..library_api::resources::planned(parent, plan)
        })
    }
}

#[cfg(test)]
#[path = "library_reads.test.rs"]
mod tests;
