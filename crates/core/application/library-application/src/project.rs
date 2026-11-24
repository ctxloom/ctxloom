//! A project opened: its configuration and its bundles, read.

use std::collections::BTreeMap;
use std::rc::Rc;
use std::sync::Arc;

use ctxloom_ports::{CompanionProbe, ConfigFiles, ContentRepo, ProbeSite, Source};
use deps_ports::RemoteLibrary;
use library::catalog::{Catalog, Entry, RemoteOrigin};
use library::model::Bundle;
use library::tree;
use library::BundleError;

use crate::config::{self, Config, Flags};
use crate::error::{AppError, Notice};
use crate::write::WritePorts;

/// What a command came to: its answer or its error, and what it noticed.
pub struct Outcome<T> {
    pub result: Result<T, AppError>,
    pub notices: Vec<Notice>,
}

/// What a run takes from the configuration before it is planned.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RunSettings {
    /// `output.dir` as written, or its default (rulings 89, 106).
    pub output_dir: String,
    /// `sync.on_run` (rulings 59, 60).
    pub on_run: crate::OnRun,
}

/// The library, over its ports.
#[derive(Clone)]
pub struct Library {
    pub(crate) content: Arc<dyn ContentRepo>,
    pub(crate) files: Arc<dyn ConfigFiles>,
    pub(crate) environ: Vec<(String, String)>,
    pub(crate) ports: Option<WritePorts>,
    remotes: Option<Rc<dyn RemoteLibrary>>,
    pub(crate) companions: Option<Arc<dyn CompanionProbe>>,
    /// The directory a companion is probed in when the caller names no
    /// project root of its own: where the command was run (D1).
    pub(crate) probe_cwd: String,
    /// ctxloom's built-in bundles join every project's catalog.
    pub(crate) builtins: bool,
    /// The engines a plan is shown to, for what they say of it (owner
    /// ruling 168, [`run_ports::Engine::advise`]).
    pub(crate) engines: Vec<Arc<dyn run_ports::Engine>>,
    /// What each engine cannot carry ([`Library::with_hook_losses`]).
    pub(crate) hook_losses: crate::losses::HookLosses,
    /// Where the user's trust of the project is asked when a flag does not
    /// say it ([`Library::with_trust`]); none, an untrusted project.
    pub(crate) trust: Option<TrustAsk>,
    /// How a diagnostic is said in a read's answer (an agent's resolution
    /// error): the composition root's catalog; none, its code and args.
    pub(crate) words: Option<Words>,
}

/// Whether the user trusts the project, asked of her daemon (step 8 U2, D4:
/// one `GetTrust`, which may start one): asked only where it decides
/// something, a project `ctxloom` bundle that would replace an operational
/// prompt.
pub type TrustAsk = Rc<dyn Fn() -> bool>;

/// A project opened.
pub struct Project {
    pub(crate) config: Config,
    pub(crate) catalog: Catalog,
    pub(crate) content: Arc<dyn ContentRepo>,
    pub(crate) files: Arc<dyn ConfigFiles>,
    pub(crate) environ: Vec<(String, String)>,
    pub(crate) ports: Option<WritePorts>,
    pub(crate) remotes: Option<Rc<dyn RemoteLibrary>>,
    pub(crate) flags: Flags,
    /// The project has a bundles directory.
    pub(crate) has_root: bool,
    /// Bundles that are there and cannot be read, by name.
    pub(crate) unreadable: BTreeMap<String, BundleError>,
    pub(crate) notices: Vec<Notice>,
    /// Where a registered companion is found and asked for its loadout.
    pub(crate) companions: Option<Arc<dyn CompanionProbe>>,
    /// Where a companion is probed (D1): the caller's environment and the
    /// project's root (the caller's directory with no project).
    pub(crate) probe_site: ProbeSite,
    /// `--no-companions` or `CTXLOOM_NO_COMPANIONS`: no companion is asked.
    pub(crate) no_companions: bool,
    /// The `run:` bundles of the registered companions, asked for once per
    /// command and kept: `(registered name, bundle)`.
    pub(crate) companion_bundles: Option<Vec<(String, Bundle)>>,
    /// The engines a plan is shown to, for what they say of it.
    pub(crate) engines: Vec<Arc<dyn run_ports::Engine>>,
    /// What each engine cannot carry.
    pub(crate) hook_losses: crate::losses::HookLosses,
    /// Where the user's trust of the project is asked ([`TrustAsk`]).
    pub(crate) trust: Option<TrustAsk>,
}

/// How a diagnostic is said, in the catalog's words.
pub type Words = Arc<dyn Fn(&dyn coded_diagnostic::Diagnostic) -> String + Send + Sync>;

impl Library {
    /// The library over `content` and `files`; `environ` is the process's
    /// environment.
    pub fn new(
        content: Arc<dyn ContentRepo>,
        files: Arc<dyn ConfigFiles>,
        environ: Vec<(String, String)>,
    ) -> Library {
        Library {
            content,
            files,
            environ,
            ports: None,
            remotes: None,
            companions: None,
            probe_cwd: String::new(),
            builtins: false,
            engines: Vec::new(),
            hook_losses: crate::losses::HookLosses::new(),
            trust: None,
            words: None,
        }
    }

    /// The library whose reads say a diagnostic in `words` (step 10a U2: a
    /// read the daemon answers has no command's catalog to say it in).
    pub fn with_words(mut self, words: Words) -> Library {
        self.words = Some(words);
        self
    }

    /// `diagnostic` in the library's words: [`Library::with_words`]'s, else
    /// its code and args.
    pub fn say(&self, diagnostic: &dyn coded_diagnostic::Diagnostic) -> String {
        match &self.words {
            Some(words) => words(diagnostic),
            None => diagnostic.to_string(),
        }
    }

    /// The library asking `trust` whether the user trusts the project, where
    /// its flags do not say so and the answer decides something (step 8 U2,
    /// D4: a project `ctxloom` bundle replacing an operational prompt).
    pub fn with_trust(mut self, trust: TrustAsk) -> Library {
        self.trust = Some(trust);
        self
    }

    /// The library showing a run's plan to `engines`, each saying what it
    /// has to say of what the plan resolved for it as notices, on the dry
    /// run and at the launch (owner ruling 168): never a refusal.
    pub fn with_engines(mut self, engines: Vec<Arc<dyn run_ports::Engine>>) -> Library {
        self.engines = engines;
        self
    }

    /// The library with ctxloom's built-in bundles ([`library::builtin`]) in
    /// every project's catalog, under the read-only built-in origin, where the
    /// project has no bundle of the name (owner rulings 110, 111).
    pub fn with_builtins(mut self) -> Library {
        self.builtins = true;
        self
    }

    /// The library joining the registered companions' loadouts to every
    /// assembly, and checking one before it is registered, through `probe`:
    /// the one probe every caller uses (OD-8).
    pub fn with_companions(mut self, probe: Arc<dyn CompanionProbe>) -> Library {
        self.companions = Some(probe);
        self
    }

    /// The directory a companion is probed in where there is no project:
    /// the caller's own (D1). In a project it is the project's root.
    pub fn with_probe_cwd(mut self, cwd: impl Into<String>) -> Library {
        self.probe_cwd = cwd.into();
        self
    }

    /// The library with the write side's ports: the user's files, the
    /// scaffold, an editor and a companion probe.
    pub fn with_write_ports(mut self, ports: WritePorts) -> Library {
        self.ports = Some(ports);
        self
    }

    /// The library reading the remotes too: the bundles installed from them
    /// join the project's, a short reference is spelled out, and a search
    /// reaches what they publish.
    pub fn with_remotes(mut self, remotes: Rc<dyn RemoteLibrary>) -> Library {
        self.remotes = Some(remotes);
        self
    }

    /// Whether `CTXLOOM_DEGRADED=1` is set.
    pub fn degraded_by_environment(&self) -> bool {
        self.environ
            .iter()
            .any(|(name, value)| name == "CTXLOOM_DEGRADED" && value == "1")
    }

    /// What a run takes from the configuration before it is planned: where
    /// its session's outputs go and what it does with the dependencies at its
    /// start. None where the configuration cannot be read: the plan that
    /// follows reports why.
    pub fn run_settings(&self, flags: &Flags) -> Option<RunSettings> {
        let mut notices = Vec::new();
        let config = config::load(self.files.as_ref(), &self.environ, flags, &mut notices).ok()?;
        Some(RunSettings {
            output_dir: config.output_dir(),
            on_run: config.sync_on_run(),
        })
    }

    /// How `engine` is run, from the configuration alone (no agent's
    /// block): its `command`, `session_home`, `auth` and `env` passthrough,
    /// what the user daemon starts a child with. None where the
    /// configuration cannot be read or the engine is not one.
    pub fn engine_drive(&self, flags: &Flags, engine: &str) -> Option<crate::Drive> {
        let mut notices = Vec::new();
        let config = config::load(self.files.as_ref(), &self.environ, flags, &mut notices).ok()?;
        library::config::engines::engine_drive(&config.value, engine).ok()
    }

    /// The format the configuration prefers off a terminal
    /// (`non_tty_format`), if it says.
    pub fn preferred_format(&self, flags: &Flags) -> Option<String> {
        let mut flags = flags.clone();
        flags.degraded = true;
        let mut notices = Vec::new();
        let config = config::load(self.files.as_ref(), &self.environ, &flags, &mut notices).ok()?;
        let format = config.text("non_tty_format");
        (!format.is_empty()).then_some(format)
    }

    /// Open the project and run `command` on it.
    pub fn run<T>(
        &self,
        flags: &Flags,
        command: impl FnOnce(&mut Project) -> Result<T, AppError>,
    ) -> Outcome<T> {
        let mut flags = flags.clone();
        flags.degraded |= self.degraded_by_environment();
        let mut notices = Vec::new();
        match self.open(&flags, &mut notices) {
            Err(error) => Outcome {
                result: Err(error),
                notices,
            },
            Ok(mut project) => {
                project.notices = notices;
                let result = command(&mut project);
                Outcome {
                    result,
                    notices: project.notices,
                }
            }
        }
    }

    fn open(&self, flags: &Flags, notices: &mut Vec<Notice>) -> Result<Project, AppError> {
        let config = config::load(self.files.as_ref(), &self.environ, flags, notices)?;
        // Without a project there is the home's configuration and nothing
        // else: the home's content is not read as a project's.
        let reads_content = config.source == Source::Project;
        let has_root = reads_content && self.content.has_root().map_err(AppError::Cause)?;
        let names = if has_root {
            self.content.bundle_names().map_err(AppError::Cause)?
        } else {
            Vec::new()
        };
        let mut entries = Vec::new();
        let mut unreadable = BTreeMap::new();
        for name in names {
            let files = self.content.read_tree(&name).map_err(AppError::Cause)?;
            let manifest = self.content.manifest_path(&name);
            match tree::read_tree(&name, &files) {
                Ok(bundle) => entries.push(Entry {
                    name,
                    manifest_path: manifest,
                    bundle,
                }),
                Err(cause) => {
                    let dir = crate::shown::parent(&manifest).to_string();
                    // An old layout's paths are named from the project's
                    // root, as the `git mv` that migrates them is typed.
                    let cause = cause.located(&self.below_root(&dir), "");
                    notices.push(Notice::BundleSkipped {
                        dir,
                        manifest,
                        cause: cause.clone(),
                    });
                    unreadable.insert(name, cause);
                }
            }
        }
        let origins = self.installed(&mut entries, &mut unreadable, notices);
        let mut catalog = Catalog::new(entries).with_origins(origins);
        // ctxloom's built-in bundles are a project's (rulings 110, 111),
        // except where it has its own of the name, readable or not.
        if reads_content && self.builtins {
            catalog = catalog.with_builtins(&|name| unreadable.contains_key(name));
        }
        Ok(Project {
            config,
            catalog,
            content: Arc::clone(&self.content),
            files: Arc::clone(&self.files),
            environ: self.environ.clone(),
            ports: self.ports.clone(),
            remotes: self.remotes.clone(),
            flags: flags.clone(),
            has_root,
            unreadable,
            notices: Vec::new(),
            companions: self.companions.clone(),
            probe_site: ProbeSite {
                env: self.environ.clone(),
                cwd: self.probe_dir(reads_content),
            },
            no_companions: flags.no_companions
                || crate::companions::switch_on(&self.environ, "CTXLOOM_NO_COMPANIONS"),
            companion_bundles: None,
            engines: self.engines.clone(),
            hook_losses: self.hook_losses.clone(),
            trust: self.trust.clone(),
        })
    }
}

impl Library {
    /// The shown directory `dir` as it is reached from the project's root
    /// (the parent of its `.ctxloom`); `dir` itself when it is not below it.
    fn below_root(&self, dir: &str) -> String {
        let app = self.files.app_dir();
        let Some(root) = app
            .strip_suffix(".ctxloom")
            .filter(|root| root.ends_with(['/', '\\']))
        else {
            return dir.to_string();
        };
        dir.strip_prefix(root)
            .filter(|rest| !rest.is_empty())
            .map_or_else(|| dir.to_string(), ToString::to_string)
    }

    /// Where a companion runs: the project's root (the parent of its
    /// `.ctxloom`) in a project, else the caller's directory (D1, and the
    /// verified gap: a probe that names none inherits its process's).
    fn probe_dir(&self, in_project: bool) -> String {
        let app = self.files.app_dir();
        if in_project {
            if let Some(root) = app
                .strip_suffix(".ctxloom")
                .map(|r| r.trim_end_matches(['/', '\\']))
                .filter(|r| !r.is_empty())
            {
                return root.to_string();
            }
        }
        self.probe_cwd.clone()
    }

    /// The bundles installed from remotes, read and added to `entries`; where
    /// each came from, by the name it is known by. One that cannot be read is
    /// skipped and said.
    fn installed(
        &self,
        entries: &mut Vec<Entry>,
        unreadable: &mut BTreeMap<String, BundleError>,
        notices: &mut Vec<Notice>,
    ) -> BTreeMap<String, RemoteOrigin> {
        let mut origins = BTreeMap::new();
        let Some(remotes) = &self.remotes else {
            return origins;
        };
        let installed = match remotes.installed() {
            Ok(installed) => installed,
            Err(cause) => {
                notices.push(Notice::RemotesUnreadable {
                    cause: coded_diagnostic::Report::of(&*cause),
                });
                return origins;
            }
        };
        for bundle in installed {
            let manifest = format!("{}/bundle.yaml", bundle.path);
            match tree::read_tree(&bundle.name, &bundle.files) {
                Ok(read) => {
                    origins.insert(
                        bundle.name.clone(),
                        RemoteOrigin {
                            reference: bundle.reference,
                            url: bundle.url,
                            sha: bundle.sha,
                            files: bundle.files,
                        },
                    );
                    entries.push(Entry {
                        name: bundle.name,
                        manifest_path: manifest,
                        bundle: read,
                    });
                }
                Err(cause) => {
                    // An installed copy is migrated in its remote, never
                    // here: the refusal names it, and a read of the bundle
                    // is refused with it (owner T27).
                    let cause = cause.located(&bundle.path, &bundle.url);
                    if matches!(cause, BundleError::OldLayout { .. }) {
                        unreadable.insert(bundle.name.clone(), cause.clone());
                    }
                    notices.push(Notice::BundleSkipped {
                        cause,
                        dir: bundle.path,
                        manifest,
                    });
                }
            }
        }
        origins
    }
}

/// What an empty listing says outside a project.
pub const NO_PROJECT_HINT: &str =
    "No project here: run 'ctxloom init' to set one up with its default content.";

/// What an empty listing says in a project.
pub const LISTING_HINT: &str =
    "Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom deps pull";

/// What an empty profile listing says in a project.
pub const PROFILE_HINT: &str =
    "Use 'ctxloom profile create <name> --include <bundle>...' to create one.";

impl Project {
    /// A reference with a registered remote's name spelled out
    /// (`origin/demo#profiles/dev`); any other as it is.
    pub(crate) fn spelled_out(&self, reference: &str) -> String {
        match &self.remotes {
            Some(remotes) => remotes.canonical(reference, &|base| self.catalog.is_local(base)),
            None => reference.to_string(),
        }
    }

    /// Whether the configuration came from the home alone (no project).
    pub(crate) fn no_project(&self) -> bool {
        self.config.source == Source::Home
    }

    /// What to say under an empty listing.
    pub(crate) fn listing_hint(&self) -> String {
        if self.no_project() {
            NO_PROJECT_HINT.to_string()
        } else {
            LISTING_HINT.to_string()
        }
    }
}

#[cfg(test)]
#[path = "project.test.rs"]
mod tests;
