use std::sync::{Arc, Mutex};

use daemon_proto::v1;
use library_application::fakes::{Setup, ENVELOPE};
use library_application::Library;

use super::{flags_of, parent_of, Libraries};
use crate::server::tests::{fixture, observing, resolve, Gits, COMMIT, ROOT};
use crate::{ApiError, Calls};
use run_ports::Edge as _;

#[test]
fn a_read_s_parent_is_its_project_or_the_home() {
    let (parent, project) = parent_of("projects/calm-otter/bundles/demo").unwrap();
    assert_eq!(parent, "projects/calm-otter");
    assert_eq!(project.unwrap().harp(), "calm-otter");
    let (parent, project) = parent_of("users/me").unwrap();
    assert_eq!(parent, "users/me");
    assert!(project.is_none());
    assert!(parent_of("bundles/demo").is_none());
    assert!(parent_of("projects/").is_none());
    assert!(parent_of("projects//bundles").is_none());
}

#[test]
fn a_read_s_globals_are_its_flags_never_trusted() {
    let flags = flags_of(Some(&v1::Globals {
        degraded: true,
        no_companions: true,
        config_set: vec!["a=b".to_string()],
    }));
    assert!(flags.degraded && flags.no_companions && !flags.trusted);
    assert_eq!(flags.config_set, vec!["a=b".to_string()]);
    assert!(!flags_of(None).degraded);
}

const SKILLS: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: Reviews\n---\nBODY\n",
    ),
    ("fragments/go.md", "GO-BODY\n"),
];

/// The library a test reads, and where each read was asked to open it.
#[derive(Default)]
struct Opened(Mutex<Vec<Asked>>);

/// A directory a read opened the library at, and its environment.
type Asked = (String, Vec<(String, String)>);

impl Libraries for Opened {
    fn root(&self, dir: &str, _: &[(String, String)]) -> Option<String> {
        // A worktree below the project is a checkout of its own (L2).
        Some(dir.to_string())
    }

    fn read(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&Library),
    ) -> Result<(), ApiError> {
        self.0.lock().unwrap().push((dir.to_string(), env.to_vec()));
        let library = Setup {
            bundles: &[("demo", SKILLS)],
            layers: &["schema_version: 9\n"],
            ..Setup::default()
        }
        .library();
        read(&library);
        Ok(())
    }

    fn read_deps(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&deps_application::Deps),
    ) -> Result<(), ApiError> {
        self.0.lock().unwrap().push((dir.to_string(), env.to_vec()));
        let fx = deps_application::fakes::Fixture::new();
        fx.origin("origin", "file:///srv/origin.git");
        if dir.ends_with("/corrupt") {
            fx.files.set_lock("not: [a lock");
        }
        read(&fx.deps);
        Ok(())
    }
}

fn path(value: &str) -> Option<v1::EnvVars> {
    Some(v1::EnvVars {
        vars: vec![v1::EnvVar {
            name: "PATH".to_string(),
            value: value.to_string(),
        }],
    })
}

// Step 10a U2 (E1): a library read is answered at the caller's directory,
// in its environment, after its tree is observed; what the observation
// said rides beside the answer (NOTICE_METADATA's notices).
#[tokio::test]
async fn a_library_read_is_observed_then_answered_at_the_callers_directory() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(Opened::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    let read = api
        .list_skills(v1::ListSkillsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                env: path("/opt/git/bin"),
                ..v1::Caller::default()
            }),
            ..v1::ListSkillsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        read.answer.skills[0].name,
        "projects/calm-quiet-heron/bundles/demo/skills/reviewer"
    );
    assert!(read.notices.is_empty(), "{:?}", read.notices);
    let opened = opened.0.lock().unwrap();
    assert_eq!(opened[0].0, "/work/calm-wt");
    assert_eq!(
        opened[0].1,
        vec![("PATH".to_string(), "/opt/git/bin".to_string())]
    );
    assert_eq!(gits.scans.load(std::sync::atomic::Ordering::SeqCst), 1);
}

// With no directory of its own, a read is answered at the project's
// registered root.
#[tokio::test]
async fn a_read_with_no_directory_is_answered_at_the_registered_root() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(Opened::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    let read = api
        .list_agents(v1::ListAgentsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            ..v1::ListAgentsRequest::default()
        })
        .await
        .unwrap();
    assert!(read.answer.agents.is_empty());
    assert!(read.notices.is_empty(), "{:?}", read.notices);
    assert_eq!(opened.0.lock().unwrap()[0].0, ROOT);
}

// A library read is answered wherever its project is, git or none (the
// library is git-free, owner 2026-11-18, superseding G1): observed, then
// opened.
#[tokio::test]
async fn a_library_read_is_observed_and_answered_in_any_directory() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(Opened::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    api.list_profiles(v1::ListProfilesRequest {
        parent: "projects/calm-quiet-heron".to_string(),
        ..v1::ListProfilesRequest::default()
    })
    .await
    .unwrap();
    assert_eq!(gits.scans.load(std::sync::atomic::Ordering::SeqCst), 1);
    assert_eq!(opened.0.lock().unwrap().len(), 1);
}

// E5: a decline of the read's use case is its typed refusal.
#[tokio::test]
async fn a_read_a_name_does_not_shape_is_refused_name_invalid() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let refused = f
        .api
        .reading(Arc::new(Opened::default()))
        .list_agents(v1::ListAgentsRequest {
            parent: "agents".to_string(),
            ..v1::ListAgentsRequest::default()
        })
        .await;
    let Err(ApiError::Refused(refusal)) = refused else {
        panic!("{refused:?}");
    };
    assert!(refusal.refusal.type_url.ends_with("NameInvalid"));
}

// Slice 3 step 6, step 10a U2: AssembleContext is a library read: made at
// the project's REGISTERED root when the caller gives no directory; a
// project nothing registered is not found; a name of no shape is invalid.
#[tokio::test]
async fn assemble_context_is_made_at_the_projects_registered_root() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let opened = Arc::new(Opened::default());
    let none = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .assemble_context(v1::AssembleContextRequest {
            name: "projects/calm-quiet-heron".to_string(),
            ..v1::AssembleContextRequest::default()
        })
        .await;
    assert!(
        matches!(&none, Err(ApiError::Refused(r)) if r.refusal.type_url.ends_with("ProjectNotFound")),
        "{none:?}"
    );
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>);
    let made = api
        .assemble_context(v1::AssembleContextRequest {
            name: "projects/calm-quiet-heron".to_string(),
            fragments: vec!["demo#fragments/go".to_string()],
            ..v1::AssembleContextRequest::default()
        })
        .await
        .unwrap();
    assert!(made.context.contains("GO-BODY"), "{made:?}");
    assert_eq!(
        made.fragments[0].fragment,
        "projects/calm-quiet-heron/bundles/demo/fragments/go"
    );
    assert_eq!(opened.0.lock().unwrap()[0].0, ROOT);
    let invalid = api
        .assemble_context(v1::AssembleContextRequest {
            name: "calm".to_string(),
            ..v1::AssembleContextRequest::default()
        })
        .await;
    assert!(
        matches!(&invalid, Err(ApiError::Refused(r)) if r.refusal.type_url.ends_with("NameInvalid")),
        "{invalid:?}"
    );
}

// Step 10a U2: AssembleContext observes the project's tree first, in the
// caller's environment, and records its baseline once; a git operation in
// progress is said in its answer's notices (the MCP tool's message).
#[tokio::test]
async fn assemble_context_observes_the_projects_library_first() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::new(Opened::default()))
        .observing(observing(&f.world, &gits));
    let request = v1::AssembleContextRequest {
        name: "projects/calm-quiet-heron".to_string(),
        caller: Some(v1::Caller {
            env: path("/usr/bin"),
            ..v1::Caller::default()
        }),
        ..v1::AssembleContextRequest::default()
    };
    let made = api.assemble_context(request.clone()).await.unwrap();
    assert!(made.notices.is_empty(), "{:?}", made.notices);
    api.assemble_context(request).await.unwrap();
    assert_eq!(gits.scans.load(std::sync::atomic::Ordering::SeqCst), 2);
    let tree = library_tree::TreeName::checkout(
        sessions::ProjectName::new("calm-quiet-heron").unwrap(),
        "brisk-copper-moth",
    )
    .unwrap();
    let cover = angzarr_embedded::v1::Cover {
        domain: library_tree::LIBRARY_DOMAIN.to_string(),
        root: Some(tree.root()),
        edition: None,
    };
    assert_eq!(f.world.events(&cover, 0).await.unwrap().len(), 1);
}

/// The library a dry run plans over: one agent, the default, on claude.
#[derive(Default)]
struct WithAgent(Mutex<Vec<String>>);

impl Libraries for WithAgent {
    fn root(&self, dir: &str, _: &[(String, String)]) -> Option<String> {
        Some(dir.to_string())
    }

    fn read(
        &self,
        dir: &str,
        _: &[(String, String)],
        read: &mut dyn FnMut(&Library),
    ) -> Result<(), ApiError> {
        self.0.lock().unwrap().push(dir.to_string());
        let library = Setup {
            bundles: &[("demo", SKILLS)],
            layers: &[
                "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\ndefault_agent: dev\n",
            ],
            ..Setup::default()
        }
        .library();
        read(&library);
        Ok(())
    }

    fn read_deps(
        &self,
        _: &str,
        _: &[(String, String)],
        _: &mut dyn FnMut(&deps_application::Deps),
    ) -> Result<(), ApiError> {
        Err(ApiError::Failed {
            detail: "no deps here".to_string(),
        })
    }
}

// Step 10a U2: `run --dry-run` is `CreateRun` with `validate_only`, planned
// as a library read at the caller's directory and named under its project:
// the run's agent, engine and profiles resource names, its workspace axis
// and runtime those it settles on.
#[tokio::test]
async fn a_preview_is_the_plan_named_under_its_project() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(WithAgent::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    let run = api
        .create_run(v1::CreateRunRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            mode: v1::run::Mode::Terminal as i32,
            prompt: "hello".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                ..v1::Caller::default()
            }),
            validate_only: true,
            ..v1::CreateRunRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(run.name, "projects/calm-quiet-heron/runs/-");
    assert_eq!(run.engine, "projects/calm-quiet-heron/engines/claude-code");
    assert_eq!(run.prompt, "hello");
    assert_eq!(run.settled_workspace, "none");
    assert_eq!(run.settled_runtime, "host");
    assert_eq!(run.settings_agent, "dev");
    assert_eq!(opened.0.lock().unwrap()[0], "/work/calm-wt");
}

// A project nothing has named is asked under `users/me` from inside it: its
// run keeps its names unresolved (owner ruling 2026-11-14).
#[tokio::test]
async fn an_unnamed_projects_preview_names_nothing() {
    let f = fixture(vec![]);
    let api = f.api.reading(Arc::new(WithAgent::default()));
    let run = api
        .create_run(v1::CreateRunRequest {
            parent: "users/me".to_string(),
            mode: v1::run::Mode::Terminal as i32,
            caller: Some(v1::Caller {
                cwd: "/work/unnamed".to_string(),
                ..v1::Caller::default()
            }),
            validate_only: true,
            ..v1::CreateRunRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(run.name, "");
    assert_eq!(run.engine, "claude-code");
}

// Step 10a U2: the deps reads are answered as the library's, observed
// first, at the caller's directory; a lock that does not read is a decline
// (exit 20), its cause the read model's own diagnostic.
#[tokio::test]
async fn a_deps_read_is_observed_then_answered_and_a_decline_carries_its_cause() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(Opened::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    let read = api
        .list_remotes(v1::ListRemotesRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::ListRemotesRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        read.answer.remotes[0].name,
        "projects/calm-quiet-heron/remotes/origin"
    );
    assert_eq!(opened.0.lock().unwrap()[0].0, "/work/calm-wt");
    let refused = api
        .list_dependencies(v1::ListDependenciesRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/corrupt".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::ListDependenciesRequest::default()
        })
        .await
        .unwrap_err();
    let ApiError::Refused(refusal) = refused else {
        panic!("{refused:?}");
    };
    let report = crate::library_refusals::rejection(&refusal.refusal).unwrap();
    assert!(
        angzarr_diagnostic::Diagnostic::code(&report).starts_with("deps-"),
        "{report:?}"
    );
}

// Step 10a U2 (owner ruling 2026-11-17): `remote show` is `BrowseRemote`,
// a deps read; a remote that cannot be browsed is a failure said in the
// use case's own words (its code and args), not an internal one.
#[tokio::test]
async fn a_remote_is_browsed_as_a_deps_read_and_its_failure_keeps_its_words() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let opened = Arc::new(Opened::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::clone(&opened) as Arc<dyn Libraries>)
        .observing(observing(&f.world, &gits));
    let read = api
        .browse_remote(v1::BrowseRemoteRequest {
            name: "projects/calm-quiet-heron/remotes/origin".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::BrowseRemoteRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        read.answer.remote,
        "projects/calm-quiet-heron/remotes/origin"
    );
    assert_eq!(read.answer.items[0].item, "demo");
    let failed = api
        .browse_remote(v1::BrowseRemoteRequest {
            name: "projects/calm-quiet-heron/remotes/nope".to_string(),
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::BrowseRemoteRequest::default()
        })
        .await
        .unwrap_err();
    let ApiError::Diagnosed { code, args } = failed else {
        panic!("{failed:?}");
    };
    assert_eq!(code, "deps-application-browse");
    assert!(args.iter().any(|arg| arg.value == "nope"), "{args:?}");
}

/// A library whose project's own config names the program claude-code runs
/// (ruling 46's key), its home one agent on it, as a read opens it under
/// whatever flags it is asked.
struct SaysCommand;

impl Libraries for SaysCommand {
    fn root(&self, dir: &str, _: &[(String, String)]) -> Option<String> {
        Some(dir.to_string())
    }

    fn read(
        &self,
        _: &str,
        _: &[(String, String)],
        read: &mut dyn FnMut(&Library),
    ) -> Result<(), ApiError> {
        let library = Setup {
            layers: &[
                "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\ndefault_agent: dev\n",
                "schema_version: 9\nengines:\n  claude-code:\n    command: /opt/their-claude\n",
            ],
            ..Setup::default()
        }
        .library();
        read(&library);
        Ok(())
    }

    fn read_deps(
        &self,
        _: &str,
        _: &[(String, String)],
        _: &mut dyn FnMut(&deps_application::Deps),
    ) -> Result<(), ApiError> {
        Err(ApiError::Failed {
            detail: "no deps here".to_string(),
        })
    }
}

struct NoSettings;

impl crate::Trusts for NoSettings {
    fn settings(&self, _: &str) -> Result<Vec<v1::TrustSetting>, ApiError> {
        Ok(Vec::new())
    }
}

/// The daemon over `SaysCommand`, its project registered, its trusts read
/// from a warm index in which the user trusted it when `trusted`.
async fn reading_trusts(trusted: bool) -> crate::Api<crate::server::tests::World> {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    let index = trust_index::TrustIndexes::default();
    if trusted {
        let name = "projects/calm-quiet-heron/trust".to_string();
        let grant = trust::GrantTrust {
            name: name.clone(),
            project_id: crate::server::tests::PROJECT.to_string(),
            etag: "e".to_string(),
            current_etag: "e".to_string(),
            actor: trust::Actor {
                user: "alice".to_string(),
            },
        };
        let cover = angzarr_embedded::v1::Cover {
            domain: trust::TRUST_DOMAIN.to_string(),
            root: Some(trust::trust_root(&name)),
            edition: None,
        };
        f.world
            .handle(sessions::keyed_book(cover, &grant, "grant"))
            .await
            .unwrap();
    }
    let mut caught = trust_index::caught_up(index.clone());
    caught
        .step(&f.world.store, f.world.registry.catalog())
        .await
        .unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    f.restarted()
        .await
        .reading(Arc::new(SaysCommand))
        .observing(observing(&f.world, &gits))
        .trusting(crate::Trusting {
            trusts: Arc::new(NoSettings),
            index,
        })
}

fn engine_show() -> v1::GetEngineRequest {
    v1::GetEngineRequest {
        name: "projects/calm-quiet-heron/engines/claude-code".to_string(),
        ..v1::GetEngineRequest::default()
    }
}

// Step 10a U7 (D7): the daemon applies trust on every library read it
// serves. An untrusted project's own project-only setting is held back,
// and the read says so as its typed notice.
#[tokio::test]
async fn an_untrusted_projects_read_holds_its_own_setting_back_and_says_so() {
    let api = reading_trusts(false).await;
    let read = api.get_engine(engine_show()).await.unwrap();
    let command = read.answer.command.unwrap();
    assert_eq!(
        (command.value.as_str(), command.source.as_str()),
        ("claude", "default")
    );
    let said: Vec<&str> = read.notices.iter().map(|n| n.code.as_str()).collect();
    assert_eq!(said, ["library-application-notice-untrusted-setting"]);
}

// Once the user trusts the project, the same read shows its setting and
// holds nothing back.
#[tokio::test]
async fn a_trusted_projects_read_shows_its_own_setting() {
    let api = reading_trusts(true).await;
    let read = api.get_engine(engine_show()).await.unwrap();
    let command = read.answer.command.unwrap();
    assert_eq!(
        (command.value.as_str(), command.source.as_str()),
        ("/opt/their-claude", "engine")
    );
    assert!(read.notices.is_empty(), "{:?}", read.notices);
}

// A preview's read says nothing of git beside the plan (the library is
// git-free, owner 2026-11-18).
#[tokio::test]
async fn a_preview_says_nothing_of_git_beside_its_plan() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(Gits::default());
    let api = f
        .restarted()
        .await
        .reading(Arc::new(WithAgent::default()))
        .observing(observing(&f.world, &gits));
    let run = api
        .create_run(v1::CreateRunRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            mode: v1::run::Mode::Terminal as i32,
            caller: Some(v1::Caller {
                cwd: "/work/calm-wt".to_string(),
                ..v1::Caller::default()
            }),
            validate_only: true,
            ..v1::CreateRunRequest::default()
        })
        .await
        .unwrap();
    assert!(run.notices.is_empty(), "{:?}", run.notices);
}

fn preview() -> v1::CreateRunRequest {
    v1::CreateRunRequest {
        parent: "projects/calm-quiet-heron".to_string(),
        mode: v1::run::Mode::Terminal as i32,
        caller: Some(v1::Caller {
            cwd: ROOT.to_string(),
            ..v1::Caller::default()
        }),
        validate_only: true,
        ..v1::CreateRunRequest::default()
    }
}

// Owner answer H: a preview on an untrusted project's own held-back
// settings is refused as its launch would be; trusted, it is planned.
#[tokio::test]
async fn an_untrusted_projects_preview_is_refused_as_its_launch_would_be() {
    let refused = reading_trusts(false).await.create_run(preview()).await;
    let Err(ApiError::Refused(refusal)) = refused else {
        panic!("not refused: {refused:?}");
    };
    assert!(
        refusal
            .refusal
            .type_url
            .ends_with("UntrustedProjectSettings"),
        "{refusal:?}"
    );
    let planned = reading_trusts(true)
        .await
        .create_run(preview())
        .await
        .unwrap();
    assert_eq!(
        planned.engine,
        "projects/calm-quiet-heron/engines/claude-code"
    );
}
