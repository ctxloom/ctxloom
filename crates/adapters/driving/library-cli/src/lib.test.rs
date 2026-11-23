//! The library commands as the program runs them: the command line parsed by
//! this crate's own nouns, the application over in-memory ports, the answer
//! as bytes. Each test names the Go command (`internal/adapters/cli`) or the
//! Gherkin feature it holds the adapter to.

use angzarr_presentation::{Catalog, Format};
use clap::Command;
use library_application::fakes::{Setup, ENVELOPE};

use super::*;
pub(crate) use daemon_proto::Carries as _;

pub(crate) const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "fragments/golang.md",
        "---\ndescription: when editing Go code\ntags: [go]\n---\nGO-RULES\n",
    ),
    ("fragments/testing.md", "# testing\n\nTEST-BODY\n"),
    ("fragments/ctl.md", "a\u{7}b\n"),
    (
        "fragments/dist.md",
        "---\ntags: [distilled]\n---\nlong form\n",
    ),
    (
        "fragments/dist.distilled.md",
        // The source's digest (T30): the form is fresh.
        "---\nsource_sha256: 1e6de09466e07ecb91f3f923292ba979acc739dc0d20700659e433e1859f8be5\n---\nshort form\n",
    ),
    (
        "prompts/review.md",
        "---\ndescription: Review code\n---\nREVIEW-BODY\n",
    ),
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: Reviews code\n---\nSKILL-BODY\n",
    ),
    (
        "mcp/tree-sitter.yaml",
        "command: tree-sitter\nargs: [--stdio]\n",
    ),
];

pub(crate) const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    ("profiles/broken.yaml", "bundles: [does-not-exist]\n"),
    (
        "profiles/dev.yaml",
        "description: dev profile\nbundles: [demo]\ntags: [team]\n",
    ),
];

pub(crate) const AGENTS: &str = "schema_version: 9\ncompanions:\n  acme: {}\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\n    runtime: host\ndefault_agent: dev\n";

/// The daemon's reads in the tests: answered as the daemon answers them
/// (library-api's reads), over the same library, under the parent `names`
/// gives.
pub(crate) struct ServedBy<'a> {
    pub(crate) library: &'a Library,
    pub(crate) names: &'a dyn Names,
}

impl ServedBy<'_> {
    /// The daemon's distill of `name`: nothing asked (fresh, no_distill),
    /// else its run, its model answering with what it was sent, and its
    /// write made at once, as the daemon's reaction makes it once the run
    /// ends.
    fn distilled(
        &self,
        kind: ItemKind,
        name: &str,
        force: bool,
        globals: Option<&v1::Globals>,
    ) -> Result<(library_application::ItemDistilled, String, Vec<v1::Notice>), Failure> {
        let globals = globals.cloned().unwrap_or_default();
        let flags = library_application::Flags {
            degraded: globals.degraded,
            no_companions: globals.no_companions,
            config_set: globals.config_set.clone(),
            trusted: false,
        };
        let reference = library_api::distill::reference_of(name);
        let asked = self
            .library
            .item_distill_ask(&flags, kind, &reference, force);
        let notices = asked
            .notices
            .iter()
            .map(library_api::reads::notice_of)
            .collect();
        match asked.result.map_err(failed)? {
            library_application::DistillAsk::Done(done) => Ok((done, String::new(), notices)),
            library_application::DistillAsk::Model(model) => {
                self.library
                    .item_distilled(&flags, kind, &reference, (&model.digest, &model.request))
                    .result
                    .map_err(failed)?;
                Ok((model.done, FAKE_RUN.to_string(), notices))
            }
        }
    }
}

/// The run the fake daemon's distill answers with.
pub(crate) const FAKE_RUN: &str = "projects/calm-otter/runs/0195f0a4-7b1e-7000-8000-000000000001";

impl Served for ServedBy<'_> {
    fn in_project(&self) -> bool {
        self.names.parent().is_ok_and(|parent| parent != "users/me")
    }

    crate::served_reads!(served_by_library_api);

    library_api::served_writes!(written_by_library_api);

    /// The daemon's distill (step 10b), as `distilled` answers it.
    fn distill_fragment(
        &self,
        under: &str,
        request: v1::DistillFragmentRequest,
    ) -> Result<v1::DistillFragmentResponse, Failure> {
        let name = format!("{}{under}", self.names.parent()?);
        let (done, run, notices) = self.distilled(
            ItemKind::Fragment,
            &name,
            request.force,
            request.caller_globals(),
        )?;
        Ok(library_api::distill::fragment_response(
            &name, &done, run, notices,
        ))
    }

    fn distill_command(
        &self,
        under: &str,
        request: v1::DistillCommandRequest,
    ) -> Result<v1::DistillCommandResponse, Failure> {
        let name = format!("{}{under}", self.names.parent()?);
        let (done, run, notices) = self.distilled(
            ItemKind::Command,
            &name,
            request.force,
            request.caller_globals(),
        )?;
        Ok(library_api::distill::command_response(
            &name, &done, run, notices,
        ))
    }

    /// The fake run's end: answered, its write made already.
    fn follow_run(&self, _: &str) -> RunFollowed {
        RunFollowed {
            notices: Vec::new(),
            ended: Ok(()),
        }
    }

    /// Nothing to hear in process.
    fn hear_stops(&self) {}

    /// The library's own editor, as the CLI would launch it.
    fn at_terminal(&self) -> bool {
        true
    }

    fn edit_document(&self, edit: &v1::Edit) -> Result<String, Failure> {
        self.library
            .edit_document(&edit.file_name, &edit.text)
            .map_err(failed)
    }

    /// The daemon's dry run, as it plans one: under the parent the names
    /// give, unnamed where they name none.
    fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, Failure> {
        let parent = self.names.named_parent()?;
        let globals = request.caller_globals().cloned().unwrap_or_default();
        let flags = library_application::Flags {
            degraded: globals.degraded,
            no_companions: globals.no_companions,
            config_set: globals.config_set.clone(),
            trusted: false,
        };
        let asked = library_application::RunRequest {
            agent: request.agent.clone(),
            profile: request.profiles.first().cloned().unwrap_or_default(),
            fragments: request.fragments.clone(),
            tags: request.tags.clone(),
            engine: request.engine.clone(),
            engine_named: false,
            prompt: request.prompt.clone(),
            saved_command: request.command.clone(),
            workspace: request.workspace.clone(),
            no_context: request.no_context,
        };
        let outcome = self.library.dry_run(&flags, &asked);
        let notices = outcome
            .notices
            .iter()
            .map(library_api::reads::notice_of)
            .collect();
        let previewed = outcome.result.map_err(failed)?;
        Ok(v1::Run {
            notices,
            ..library_api::resources::planned(parent.as_deref(), &previewed.plan)
        })
    }

    /// The daemon's `GetLoadout`, as it answers one under `users/me`.
    fn own_loadout(
        &self,
        request: v1::GetLoadoutRequest,
    ) -> Result<(v1::Loadout, Vec<v1::Notice>), Failure> {
        let outcome = library_api::reads::get_loadout(
            self.library,
            &library_application::Flags::default(),
            "users/me",
            &request,
        );
        let notices = outcome
            .notices
            .iter()
            .map(library_api::reads::notice_of)
            .collect();
        Ok((outcome.result.map_err(failed)?, notices))
    }

    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Failure> {
        let parent = self.names.parent()?;
        let globals = request.caller_globals().cloned().unwrap_or_default();
        let flags = library_application::Flags {
            degraded: globals.degraded,
            no_companions: globals.no_companions,
            config_set: globals.config_set.clone(),
            trusted: false,
        };
        let outcome = library_api::reads::assemble_context(self.library, &flags, &parent, &request);
        let mut assembled = outcome.result.map_err(failed)?;
        assembled.notices = outcome
            .notices
            .iter()
            .map(|notice| v1::Notice {
                code: notice.code().to_string(),
                args: Vec::new(),
                text: notice.to_string(),
            })
            .collect();
        Ok(assembled)
    }
}

/// [`ServedBy`]'s reads: each library-api's, over the test's library, its
/// target the parent `names` gives followed by `under`, its notices said
/// by code and text.
macro_rules! served_by_library_api {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<(v1::$response, Vec<v1::Notice>), Failure> {
                let parent = self.names.parent()?;
                let request = v1::$request {
                    $target: format!("{parent}{under}"),
                    ..request
                };
                let globals = request.caller_globals().cloned().unwrap_or_default();
                let flags = library_application::Flags {
                    degraded: globals.degraded,
                    no_companions: globals.no_companions,
                    config_set: globals.config_set.clone(),
                    trusted: false,
                };
                let outcome = library_api::reads::$method(self.library, &flags, &parent, &request);
                let notices = outcome
                    .notices
                    .iter()
                    .map(library_api::reads::notice_of)
                    .collect();
                Ok((outcome.result.map_err(failed)?, notices))
            }
        )*
    };
}
use served_by_library_api;

/// The daemon's writes in the tests: each verb's use case run over the
/// library itself (it writes as it goes: no intent, the operation the use
/// case's own), answered by its response as the daemon answers it.
macro_rules! written_by_library_api {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident, $verb:literal;)*) => {
        $(
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, Failure> {
                let parent = self.names.parent()?;
                let mut request = request;
                Targeted::target(&mut request, format!("{parent}{under}"));
                let globals = request.caller_globals().cloned().unwrap_or_default();
                let flags = library_application::Flags {
                    degraded: globals.degraded,
                    no_companions: globals.no_companions,
                    config_set: globals.config_set.clone(),
                    trusted: false,
                };
                // A preview writes nothing: its use case over the recording
                // library, as the daemon plans it; an apply that plans no
                // change is applied as nothing, as the daemon answers it.
                let (previewing, recorder) = self.library.clone().recording();
                let mut apply = !request.validate_only;
                if apply {
                    let planned = library_api::served_writes::$method::plan(
                        &previewing, &flags, &request, apply,
                    );
                    apply = planned.result.is_err() || !recorder.take().is_empty();
                }
                let library = if apply { self.library } else { &previewing };
                let outcome =
                    library_api::served_writes::$method::plan(library, &flags, &request, apply);
                let notices = outcome
                    .notices
                    .iter()
                    .map(library_api::reads::notice_of)
                    .collect();
                let answer = outcome.result.map_err(failed)?;
                Ok(library_api::served_writes::$method::respond(
                    &request,
                    library_api::served_writes::Wrote {
                        answer: Some(answer),
                        applied: apply,
                        etag: String::new(),
                        at: String::new(),
                        parent,
                        notices,
                        source: String::new(),
                    },
                ))
            }
        )*
    };
}
use written_by_library_api;

/// A write request, writing what its target names (the daemon's client
/// sets it so).
trait Targeted {
    fn target(&mut self, target: String);
}

macro_rules! targeted {
    ($($request:ident => |$r:ident, $t:ident| $set:expr;)*) => {
        $(
            impl Targeted for v1::$request {
                fn target(&mut self, $t: String) {
                    let $r = self;
                    $set;
                }
            }
        )*
    };
}

targeted! {
    CreateBundleRequest => |r, t| r.parent = t;
    ImportBundleRequest => |r, t| r.parent = t;
    UpdateBundleRequest => |r, t| r.bundle.get_or_insert_with(Default::default).name = t;
    CreateFragmentRequest => |r, t| r.parent = t;
    CreateCommandRequest => |r, t| r.parent = t;
    UpdateFragmentRequest => |r, t| r.fragment.get_or_insert_with(Default::default).name = t;
    UpdateCommandRequest => |r, t| r.command.get_or_insert_with(Default::default).name = t;
    UpdateConfigRequest => |r, t| r.name = t;
    EditConfigRequest => |r, t| r.name = t;
    DeleteBundleRequest => |r, t| r.name = t;
    DeleteFragmentRequest => |r, t| r.name = t;
    DeleteCommandRequest => |r, t| r.name = t;
    DeleteSkillRequest => |r, t| r.name = t;
    DeleteProfileRequest => |r, t| r.name = t;
    DeleteAgentRequest => |r, t| r.name = t;
    DeleteCompanionRequest => |r, t| r.name = t;
    EnableCompanionRequest => |r, t| r.name = t;
    DisableCompanionRequest => |r, t| r.name = t;
    CreateSkillRequest => |r, t| r.parent = t;
    UpdateMcpServerRequest => |r, t| r.mcp_server.get_or_insert_with(Default::default).name = t;
    ExportBundleRequest => |r, t| r.name = t;
    MoveBundleRequest => |r, t| r.name = t;
    CreateProfileRequest => |r, t| r.parent = t;
    UpdateProfileRequest => |r, t| r.profile.get_or_insert_with(Default::default).name = t;
    CreateCompanionRequest => |r, t| r.parent = t;
    SetDefaultAgentRequest => |r, t| r.parent = t;
    CreateAgentRequest => |r, t| r.parent = t;
    UpdateAgentRequest => |r, t| r.agent.get_or_insert_with(Default::default).name = t;
}

/// What one invocation left behind.
pub(crate) struct Ran {
    pub(crate) exit: Result<u8, String>,
    pub(crate) out: String,
    pub(crate) err: String,
}

impl Ran {
    pub(crate) fn code(&self) -> u8 {
        match &self.exit {
            Ok(code) => *code,
            Err(failure) => panic!("failed: {failure}"),
        }
    }

    pub(crate) fn refused(&self) -> &str {
        match &self.exit {
            Ok(code) => panic!("exit {code}, not a failure: {}", self.out),
            Err(failure) => failure,
        }
    }

    pub(crate) fn json(&self) -> serde_json::Value {
        serde_json::from_str(&self.out).unwrap_or_else(|e| panic!("{e}: {}", self.out))
    }
}

/// How an invocation is made.
#[derive(Clone, Copy)]
pub(crate) struct How {
    pub(crate) explicit: Option<Format>,
    pub(crate) terminal: bool,
    pub(crate) degraded: bool,
}

pub(crate) const JSON: How = How {
    explicit: Some(Format::Json),
    terminal: false,
    degraded: false,
};

pub(crate) const TEXT: How = How {
    explicit: Some(Format::Text),
    terminal: false,
    degraded: false,
};

/// The names of a project the daemon calls `calm-otter` (OD-40).
pub(crate) struct Project;

impl Names for Project {
    fn parent(&self) -> Result<String, Failure> {
        Ok("projects/calm-otter".to_string())
    }
}

/// Outside a project (OQ-3).
pub(crate) struct Home;

impl Names for Home {
    fn parent(&self) -> Result<String, Failure> {
        Ok("users/me".to_string())
    }
}

/// A ctxloom project nothing has named, asked by a command that may not
/// name it (a dry run, owner ruling 2026-11-14).
pub(crate) struct Unnamed;

impl Names for Unnamed {
    fn parent(&self) -> Result<String, Failure> {
        panic!("an unnamed project's command asks whether it is named")
    }

    fn named_parent(&self) -> Result<Option<String>, Failure> {
        Ok(None)
    }
}

/// No daemon to name the project: the call fails, as one unreached does.
pub(crate) struct Unreachable;

/// A write's refusal as stale, as the daemon answers one (its code alone).
#[derive(Debug)]
pub(crate) struct Stale;

impl std::fmt::Display for Stale {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("the library changed since you looked; look again")
    }
}

impl Diagnostic for Stale {
    fn code(&self) -> &str {
        "library-tree-plan-stale"
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        Vec::new()
    }
}

#[derive(Debug)]
pub(crate) struct NoDaemon;

impl std::fmt::Display for NoDaemon {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("no daemon")
    }
}

impl Diagnostic for NoDaemon {
    fn code(&self) -> &str {
        "daemon-api-unreached"
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        Vec::new()
    }
}

impl Names for Unreachable {
    fn parent(&self) -> Result<String, Failure> {
        Err(Failure::new(NoDaemon, Exit::FAILED))
    }
}

pub(crate) fn go(line: &[&str], how: How, layers: &[&str]) -> Ran {
    go_in(line, how, layers, &Project)
}

pub(crate) fn go_named(line: &[&str], how: How, names: &dyn Names) -> Ran {
    go_in(line, how, &[ENGINE_HOME, ENGINE_PROJECT], names)
}

pub(crate) fn go_in(line: &[&str], how: How, layers: &[&str], names: &dyn Names) -> Ran {
    go_served(line, how, layers, names, None)
}

/// What a command asked the daemon: each read's target and its request,
/// kept as it was built; every read is then answered unreached.
#[derive(Default)]
pub(crate) struct Asks {
    pub(crate) asked: std::cell::RefCell<Vec<(String, Box<dyn std::any::Any>)>>,
    /// Answers given in turn, each to the first ask of its type; an ask
    /// with none queued is answered unreached.
    answers: std::cell::RefCell<std::collections::VecDeque<Box<dyn std::any::Any>>>,
    /// What the person at the terminal answers a Confirm need; none: no
    /// terminal.
    confirms: Option<bool>,
    /// That the command is not at a terminal (stdin and stdout), whatever
    /// `confirms` says: [`Served::at_terminal`] answers false.
    off_terminal: bool,
}

impl Asks {
    /// This fake, not at a terminal.
    pub(crate) fn off_terminal(mut self) -> Asks {
        self.off_terminal = true;
        self
    }

    /// The fake that answers `answers` in turn, and `confirms` at its
    /// terminal.
    pub(crate) fn answering(answers: Vec<Box<dyn std::any::Any>>, confirms: Option<bool>) -> Asks {
        Asks {
            answers: std::cell::RefCell::new(answers.into()),
            confirms,
            ..Asks::default()
        }
    }

    fn ask<R: 'static, M: 'static>(&self, under: &str, request: R) -> Result<M, Failure> {
        self.asked
            .borrow_mut()
            .push((under.to_string(), Box::new(request)));
        let mut answers = self.answers.borrow_mut();
        if answers.front().is_some_and(|answer| answer.is::<Stale>()) {
            answers.pop_front();
            return Err(Failure::new(Stale, Exit::REFUSED));
        }
        if answers.front().is_some_and(|answer| answer.is::<M>()) {
            if let Some(answer) = answers.pop_front() {
                if let Ok(answer) = answer.downcast::<M>() {
                    return Ok(*answer);
                }
            }
        }
        Err(Failure::new(NoDaemon, Exit::FAILED))
    }

    /// The requests of type `R` it was asked, in order.
    pub(crate) fn requests<R: Clone + 'static>(&self) -> Vec<R> {
        self.asked
            .borrow()
            .iter()
            .filter_map(|(_, request)| request.downcast_ref::<R>().cloned())
            .collect()
    }
}

/// [`Asks`]'s reads, each kept.
macro_rules! asked_of_the_daemon {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<(v1::$response, Vec<v1::Notice>), Failure> {
                self.ask(under, request)
            }
        )*
    };
}

/// [`Asks`]'s writes, each kept.
macro_rules! written_to_the_daemon {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, Failure> {
                self.ask(under, request)
            }
        )*
    };
}

/// That the command asked to hear its stops ([`Served::hear_stops`]).
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct HearStops;

impl Served for Asks {
    fn in_project(&self) -> bool {
        true
    }

    crate::served_reads!(asked_of_the_daemon);

    crate::served_writes!(written_to_the_daemon);

    fn at_terminal(&self) -> bool {
        !self.off_terminal
    }

    fn edit_document(&self, edit: &v1::Edit) -> Result<String, Failure> {
        self.ask("", edit.clone())
    }

    fn follow_run(&self, run: &str) -> RunFollowed {
        RunFollowed {
            notices: Vec::new(),
            ended: self.ask(
                "",
                v1::WatchRunRequest {
                    name: run.to_string(),
                },
            ),
        }
    }

    /// Recorded among the requests, as [`HearStops`], so its order is seen.
    fn hear_stops(&self) {
        self.asked
            .borrow_mut()
            .push((String::new(), Box::new(HearStops)));
    }

    fn confirm(&self, _: &str) -> Option<bool> {
        self.confirms
    }

    fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, Failure> {
        self.ask("", request)
    }

    fn own_loadout(
        &self,
        request: v1::GetLoadoutRequest,
    ) -> Result<(v1::Loadout, Vec<v1::Notice>), Failure> {
        self.ask("", request)
    }

    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Failure> {
        self.ask("", request)
    }
}

/// The global flags a degraded command's reads carry.
pub(crate) const DEGRADED: How = How {
    degraded: true,
    ..JSON
};

/// The requests of type `R` `line` asked the daemon (run degraded, so
/// its globals are told from none), each with its target.
pub(crate) fn asked<R: Clone + 'static>(line: &[&str]) -> Vec<(String, R)> {
    let asks = Asks::default();
    go_served(line, DEGRADED, BARE, &Project, Some(&asks));
    let asked = asks.asked.into_inner();
    asked
        .iter()
        .filter_map(|(under, request)| {
            request
                .downcast_ref::<R>()
                .map(|request| (under.clone(), request.clone()))
        })
        .collect()
}

/// The one request of type `R` `line` asked the daemon, with its target.
pub(crate) fn asked_once<R: Clone + 'static>(line: &[&str]) -> (String, R) {
    let mut asked = asked::<R>(line);
    assert_eq!(
        asked.len(),
        1,
        "{line:?} asks one {}",
        std::any::type_name::<R>()
    );
    asked.remove(0)
}

/// The globals a [`DEGRADED`] command's request carries.
pub(crate) fn degraded() -> Option<v1::Globals> {
    Some(v1::Globals {
        degraded: true,
        ..v1::Globals::default()
    })
}

pub(crate) fn go_served(
    line: &[&str],
    how: How,
    layers: &[&str],
    names: &dyn Names,
    served: Option<&dyn Served>,
) -> Ran {
    let setup = Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers,
        ..Setup::default()
    };
    let program = Command::new("ctxloom")
        .subcommand_required(true)
        .subcommands(nouns());
    let mut argv = vec!["ctxloom"];
    argv.extend_from_slice(line);
    let matches = program.try_get_matches_from(argv).unwrap_or_else(|e| {
        panic!("{line:?} does not parse: {e}");
    });
    let noun = matches.subcommand_name().unwrap().to_string();
    let call = Call {
        matches: &matches,
        explicit_format: how.explicit,
        default_format: Format::Json,
        stdout_is_terminal: how.terminal,
        globals: library_application::Flags {
            degraded: how.degraded,
            no_companions: false,
            config_set: Vec::new(),
            trusted: false,
        },
    };
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let library = setup.library();
    let by_library = ServedBy {
        library: &library,
        names,
    };
    let exit = run(
        &library,
        names,
        served.unwrap_or(&by_library),
        &noun,
        &call,
        &catalog,
        &mut out,
        &mut err,
    )
    .map(|exit| exit.code(true))
    .map_err(|f| format!("{}:{}", f.cause.code(), f.exit.code(true)));
    Ran {
        exit,
        out: String::from_utf8(out).unwrap(),
        err: String::from_utf8(err).unwrap(),
    }
}

// The engine noun's layers (`go_named`'s, and noun/engine.test.rs's).
pub(crate) const ENGINE_HOME: &str = "schema_version: 9\nruntime: container-rootless\nengines:\n  claude-code:\n    command: /opt/claude\n    env: [HTTPS_PROXY]\n    auth: none\n  opencode:\n    session_home: host\n";

pub(crate) const ENGINE_PROJECT: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n    runtime: host\n    profiles: [dev]\n  strict:\n    engine: claude-code\n    runtime: container-rootful\n    profiles: [dev]\n  same:\n    engine: claude-code\n    runtime: container-rootless\n  plain:\n    engine: opencode\n";

pub(crate) const BARE: &[&str] = &[AGENTS];

/// A success's json: ok, or a dry run's would-launch (ruling 153), which its
/// own test holds to its code.
pub(crate) fn json_of(line: &[&str]) -> serde_json::Value {
    let ran = go(line, JSON, BARE);
    assert!((16..32).contains(&ran.code()), "{line:?}: a success");
    ran.json()
}

pub(crate) fn text_of(line: &[&str]) -> String {
    let ran = go(line, TEXT, BARE);
    assert!((16..32).contains(&ran.code()), "{line:?}: a success");
    ran.out
}

// clidiag.Warn: a notice is one json line under json, words and a fix line
// under text.
#[test]
fn test_notices_are_said_as_a_json_line_or_as_words_with_their_fix() {
    let ghost = ["schema_version: 9\nagents:\n  ghost: {}\n  real:\n    engine: claude-code\n"];
    let json = go(&["agent", "list"], JSON, &ghost);
    assert_eq!(json.code(), 16);
    let line: serde_json::Value = serde_json::from_str(json.err.trim_end()).unwrap();
    assert_eq!(line["prog"], "ctxloom");
    // The line names the notice's own code (rust-support d7e783a).
    assert!(
        line["code"].as_str().is_some_and(|code| !code.is_empty()),
        "{line}"
    );
    assert!(line["warning"]
        .as_str()
        .unwrap()
        .starts_with("agents.ghost in /p/.ctxloom/layer-0.yaml declares neither"));
    assert!(line.get("remedy").is_none());
    let text = go(&["agent", "list"], TEXT, &ghost);
    assert!(text.err.starts_with("ctxloom: warning: agents.ghost in "));
    assert!(!text.err.contains("fix:"));
    let broken = go(
        &["run", "--dry-run", "-p", "broken"],
        How {
            degraded: true,
            ..TEXT
        },
        BARE,
    );
    assert!(broken
        .err
        .starts_with("ctxloom: warning: failed to load bundle \"does-not-exist\""));
    assert!(broken
        .err
        .contains("\n  fix: run `ctxloom deps pull` or fix the bundle ref, or pass --degraded\n"));
    let by_json = go(
        &["run", "--dry-run", "-p", "broken"],
        How {
            degraded: true,
            ..JSON
        },
        BARE,
    );
    assert!(by_json.err.contains("\"remedy\":\"run `ctxloom deps pull`"));
}

// inert_render.go: a body altered for the terminal is said so on the error
// stream, in text; the json form is the raw bytes.
#[test]
fn test_a_body_rendered_inert_is_noted_and_json_is_raw() {
    let text = go(&["bundle", "view", "demo#fragments/ctl"], TEXT, BARE);
    assert_eq!(text.out, "a\u{27e8}U+0007\u{27e9}b\n");
    assert_eq!(
        text.err,
        "ctxloom: warning: demo#fragments/ctl is publisher-authored and was rendered inert for this terminal (1 control character(s) escaped); use --format json for the raw bytes\n"
    );
    let json = go(&["bundle", "view", "demo#fragments/ctl"], JSON, BARE);
    assert_eq!(json.json()["body"], "a\u{7}b\n");
    assert!(json.err.is_empty());
}

// The format: asked for, a terminal's text, the configuration's
// `non_tty_format`, else the program's default.
#[test]
fn test_the_format_is_asked_for_a_terminals_or_the_configurations() {
    let line = ["companion", "list"];
    let asked_text = go(&line, TEXT, BARE);
    assert!(asked_text.out.starts_with("acme"));
    let default = How {
        explicit: None,
        terminal: false,
        degraded: false,
    };
    // OD-40: json is the companions' `ListCompanionsResponse`.
    assert!(go(&line, default, BARE).out.starts_with('{'));
    let terminal = How {
        terminal: true,
        ..default
    };
    assert!(go(&line, terminal, BARE).out.starts_with("acme"));
    let text_config = ["schema_version: 9\ncompanions:\n  acme: {}\nnon_tty_format: text\n"];
    assert!(go(&line, default, &text_config).out.starts_with("acme"));
    let json_config = ["schema_version: 9\ncompanions:\n  acme: {}\nnon_tty_format: json\n"];
    assert!(go(&line, default, &json_config).out.starts_with('{'));
    assert!(go(&line, JSON, &text_config).out.starts_with('{'));
}

// A noun the matches do not hold is not run.
#[test]
fn test_running_a_noun_the_line_does_not_name_is_refused() {
    let program = Command::new("ctxloom").subcommands(nouns());
    let matches = program.get_matches_from(["ctxloom", "companion", "list"]);
    let call = Call {
        matches: &matches,
        explicit_format: None,
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: library_application::Flags {
            degraded: false,
            no_companions: false,
            config_set: Vec::new(),
            trusted: false,
        },
    };
    let library = Setup::default().library();
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let served = ServedBy {
        library: &library,
        names: &Project,
    };
    let wrong = run(
        &library, &Project, &served, "bundle", &call, &catalog, &mut out, &mut err,
    )
    .unwrap_err();
    assert_eq!(wrong.cause.code(), "library-application-search-nothing");
    assert!(out.is_empty());
}

/// A stream that cannot be written.
pub(crate) struct Broken;

impl Write for Broken {
    fn write(&mut self, _: &[u8]) -> io::Result<usize> {
        Err(io::Error::other("pipe closed"))
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// An answer that cannot be written is a failure with its own code and exit 32.
#[test]
fn test_an_answer_that_cannot_be_written_fails_with_its_cause() {
    let program = Command::new("ctxloom").subcommands(nouns());
    let matches = program.get_matches_from(["ctxloom", "companion", "list"]);
    let call = Call {
        matches: &matches,
        explicit_format: Some(Format::Json),
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: library_application::Flags {
            degraded: false,
            no_companions: false,
            config_set: Vec::new(),
            trusted: false,
        },
    };
    let library = Setup::default().library();
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let failure = run(
        &library,
        &Project,
        &ServedBy {
            library: &library,
            names: &Project,
        },
        "companion",
        &call,
        &catalog,
        &mut Broken,
        &mut Vec::new(),
    )
    .unwrap_err();
    assert_eq!(failure.exit, Exit::FAILED);
    assert_eq!(failure.cause.code(), "ctxloom-cli-output");
    let args = failure.cause.args();
    assert_eq!(args.len(), 1);
    assert_eq!(args[0].0, "ext_err_txt");
    assert_eq!(
        format!("{:?}", args[0].1),
        format!("{:?}", angzarr_diagnostic::Arg::from("pipe closed"))
    );
    assert!(failure.cause.to_string().contains("ctxloom-cli-output"));
}

#[path = "write.test.rs"]
pub(crate) mod write_verbs;

/// A removal's preview, its Confirm need and its etag, as the daemon
/// answers one (step 10b U3).
fn previewed_removal() -> v1::DeleteBundleResponse {
    v1::DeleteBundleResponse {
        applied: Some(false),
        detail: vec!["1 fragment(s)".to_string()],
        apply_command: "ctxloom bundle remove notes --yes".to_string(),
        needs: vec![v1::Need {
            optional: false,
            id: "confirm".to_string(),
            kind: Some(v1::need::Kind::Confirm(v1::Confirm {
                text: "Remove bundle \"notes\"?".to_string(),
                code: String::new(),
                args: vec![v1::NeedArg {
                    name: "target".to_string(),
                    value: "bundle \"notes\"".to_string(),
                }],
            })),
        }],
        etag: "7".to_string(),
        ..v1::DeleteBundleResponse::default()
    }
}

fn removed_bundle() -> v1::DeleteBundleResponse {
    v1::DeleteBundleResponse {
        applied: Some(true),
        path: "/work/bundles/notes".to_string(),
        ..v1::DeleteBundleResponse::default()
    }
}

// Step 10b U3: at her terminal, a removal is previewed, its Confirm need
// asked there, and her yes applied with the answer and the preview's etag.
#[test]
fn test_a_removal_confirmed_at_the_terminal_is_applied_with_the_answer_and_the_etag() {
    let asks = Asks::answering(
        vec![Box::new(previewed_removal()), Box::new(removed_bundle())],
        Some(true),
    );
    let ran = go_served(
        &["bundle", "remove", "notes"],
        TEXT,
        BARE,
        &Project,
        Some(&asks),
    );
    assert_eq!(ran.out, "Removed bundle: /work/bundles/notes\n");
    assert_eq!(ran.code(), 16);
    let sent = asks.requests::<v1::DeleteBundleRequest>();
    assert_eq!(sent.len(), 2);
    assert!(sent[0].validate_only);
    assert_eq!(sent[0].etag, "");
    assert!(!sent[1].validate_only);
    assert_eq!(sent[1].etag, "7");
    assert_eq!(
        sent[1].answers,
        [v1::Answer {
            need_id: "confirm".to_string(),
            answer: Some(v1::answer::Answer::Confirmed(true)),
        }]
    );
}

// A no at the terminal, or no terminal at all, prints the preview in its
// own words and applies nothing.
#[test]
fn test_a_removal_declined_or_off_a_terminal_is_its_preview_alone() {
    for confirms in [Some(false), None] {
        let asks = Asks::answering(
            vec![Box::new(previewed_removal()), Box::new(removed_bundle())],
            confirms,
        );
        let ran = go_served(
            &["bundle", "remove", "notes"],
            TEXT,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(
            ran.out,
            "Would remove bundle \"notes\":\n  1 fragment(s)\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom bundle remove notes --yes\n",
            "{confirms:?}"
        );
        assert_eq!(ran.exit, Ok(17), "{confirms:?}");
        assert_eq!(asks.requests::<v1::DeleteBundleRequest>().len(), 1);
    }
}

// `--yes` is one call that plans and applies: nothing is previewed or
// asked.
#[test]
fn test_a_removal_with_yes_is_one_call_that_applies() {
    let asks = Asks::answering(vec![Box::new(removed_bundle())], Some(false));
    let ran = go_served(
        &["bundle", "remove", "notes", "--yes"],
        TEXT,
        BARE,
        &Project,
        Some(&asks),
    );
    assert_eq!(ran.out, "Removed bundle: /work/bundles/notes\n");
    let sent = asks.requests::<v1::DeleteBundleRequest>();
    assert_eq!(sent.len(), 1);
    assert!(!sent[0].validate_only);
}

/// `line`'s noun and whether its verb is the daemon's read.
fn is_served(line: &[&str]) -> bool {
    let program = clap::Command::new("ctxloom").subcommands(crate::nouns());
    let matches = program
        .try_get_matches_from(std::iter::once("ctxloom").chain(line.iter().copied()))
        .unwrap_or_else(|e| panic!("{line:?}: {e}"));
    let (noun, sub) = matches.subcommand().expect("a noun");
    crate::is_served_read(noun, sub)
}

// Step 10a U2: the daemon's reads take its home step, not the caller's;
// every other verb runs in process, and its caller takes it.
#[test]
fn test_the_daemons_reads_are_told_from_the_verbs_in_process() {
    for line in [
        &["bundle"][..],
        &["bundle", "ls"],
        &["bundle", "show", "demo"],
        &["bundle", "view", "demo#fragments/x"],
        &["fragment", "show", "demo#fragments/x"],
        &["fragment", "premises"],
        &["command", "list"],
        &["skill", "show", "demo#skills/x"],
        &["profile"],
        &["agent", "show", "dev"],
        &["engine", "list"],
        &["companion", "list"],
        &["config"],
        &["config", "show"],
        &["context"],
        &["context", "assemble"],
        &["mcp", "server", "list"],
        &["mcp", "server", "show", "x"],
        &["search", "go"],
        &["loadout"],
        &["run", "--dry-run", "hello"],
    ] {
        assert!(is_served(line), "{line:?} is served");
    }
    for line in [
        &["bundle", "create", "demo"][..],
        &["profile", "create", "dev"],
        &["agent", "default"],
        &["companion", "add", "acme"],
        &["config", "edit"],
        &["run", "hello"],
    ] {
        assert!(!is_served(line), "{line:?} runs in process");
    }
    // Every verb the table names is a verb of its noun.
    for (noun, verbs) in crate::SERVED_VERBS {
        let command = crate::nouns()
            .into_iter()
            .find(|c| c.get_name() == *noun)
            .unwrap_or_else(|| panic!("{noun}"));
        for verb in *verbs {
            assert!(command.find_subcommand(verb).is_some(), "{noun} {verb}");
        }
    }
}

/// Whether `line`'s verb is a write the daemon serves.
fn is_served_writing(line: &[&str]) -> bool {
    let program = clap::Command::new("ctxloom").subcommands(crate::nouns());
    let matches = program
        .try_get_matches_from(std::iter::once("ctxloom").chain(line.iter().copied()))
        .unwrap_or_else(|e| panic!("{line:?}: {e}"));
    let (noun, sub) = matches.subcommand().expect("a noun");
    crate::is_served_write(noun, sub)
}

// Step 10b U3: the writes the daemon serves take its home step; `mcp`'s
// verbs are its `server`'s.
#[test]
fn test_the_daemons_writes_are_told_from_the_verbs_in_process() {
    for line in [
        &["bundle", "create", "demo"][..],
        &["bundle", "edit", "demo", "--version", "2.0.0"],
        &["config", "set", "a.b", "c"],
        &["mcp", "server", "set", "demo#mcp/x", "--command", "echo"],
        &["mcp", "server", "edit", "demo#mcp/x"],
        &["profile", "create", "dev", "--include", "demo"],
        &["profile", "edit", "dev"],
        &["profile", "modify", "dev", "--add-bundle", "demo"],
    ] {
        assert!(is_served_writing(line), "{line:?} is served");
    }
    for line in [
        &["mcp", "server", "list"][..],
        &["mcp", "server", "show", "x"],
        &["profile", "export", "dev", "out"],
    ] {
        assert!(!is_served_writing(line), "{line:?} is no served write");
    }
}

/// A removal preview's Confirm need of `target`, as its json says it.
pub(crate) fn confirm_need(target: &str) -> serde_json::Value {
    serde_json::json!({"id": "confirm", "confirm": {
        "text": format!("Remove {target}?"),
        "args": [{"name": "target", "value": target}],
    }})
}

// Step 10b U3: what she confirmed changed before her answer reached the
// daemon, so its apply is refused stale; she is shown the removal again,
// as it is now, and nothing is applied.
#[test]
fn test_a_confirmed_removal_refused_stale_is_previewed_again() {
    let asks = Asks::answering(
        vec![
            Box::new(previewed_removal()),
            Box::new(Stale),
            Box::new(previewed_removal()),
        ],
        Some(true),
    );
    let ran = go_served(
        &["bundle", "remove", "notes"],
        TEXT,
        BARE,
        &Project,
        Some(&asks),
    );
    assert!(
        matches!(&ran.exit, Err(why) if why.starts_with("library-tree-plan-stale:48")),
        "{:?}",
        ran.exit
    );
    assert!(
        ran.out.starts_with("Would remove bundle \"notes\":"),
        "{}",
        ran.out
    );
    let sent = asks.requests::<v1::DeleteBundleRequest>();
    assert_eq!(
        sent.iter().map(|r| r.validate_only).collect::<Vec<_>>(),
        [true, false, true]
    );
}
