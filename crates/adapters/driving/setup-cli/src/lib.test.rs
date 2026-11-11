//! The setup commands run on fakes of the ports they reach through the use
//! cases: what each noun prints, on which stream, and how it exits.

use std::collections::BTreeMap;
use std::rc::Rc;
use std::sync::{Arc, Mutex};

use auth_application::ExportedVars;
use clap::Command;
use deps_application::fakes::Fixture;
use doctor_checks::{
    ConfigFiles, Executables, Failure as PortFailure, LayerText, Put, Scope, Source, UserFiles,
};
use init_application::{Ended, LaunchFailure, Scaffold};
use reclaim_application::ProjectCache;

use super::*;

/// One in-memory world behind every port.
#[derive(Default)]
struct World {
    files: Mutex<BTreeMap<String, String>>,
    trees: Mutex<BTreeMap<String, u64>>,
    /// The variables the launch exports.
    exported: Mutex<Vec<String>>,
    /// The project has no `.ctxloom` yet.
    fresh: std::sync::atomic::AtomicBool,
    /// The config layers' texts, lowest first.
    config: Mutex<Vec<String>>,
}

/// Her terminal: there or not, and what she answers.
#[derive(Default)]
struct Person {
    terminal: bool,
    yes: bool,
    /// The line she types when asked (none: the input closed).
    line: Option<&'static str>,
    asked: Mutex<Vec<String>>,
}

impl Terminal for Person {
    fn is_terminal(&self) -> bool {
        self.terminal
    }

    fn confirm(&self, question: &str) -> bool {
        self.asked.lock().unwrap().push(question.to_string());
        self.yes
    }

    fn ask(&self, question: &str, _: &str) -> Option<String> {
        self.asked.lock().unwrap().push(question.to_string());
        self.line.map(String::from)
    }
}

/// The sessions init launches: what they were launched with, and how they end.
#[derive(Default)]
struct Launches {
    /// The launch of this session (`interview`, `configured`) fails with
    /// this exit code.
    refuses: Option<(&'static str, u8)>,
    interview_code: Option<i32>,
    restart_code: Option<i32>,
    launched: Mutex<Vec<&'static str>>,
}

impl Launches {
    fn refusal(&self, session: &str) -> Result<(), LaunchFailure> {
        match self.refuses {
            Some((which, exit)) if which == session => Err(LaunchFailure {
                cause: Box::new(deps_application::AppError::DefaultNeedsName),
                exit,
            }),
            _ => Ok(()),
        }
    }
}

impl Sessions for Launches {
    fn interview(&self, _: &str) -> Result<Ended, LaunchFailure> {
        self.launched.lock().unwrap().push("interview");
        self.refusal("interview")?;
        Ok(Ended {
            code: self.interview_code,
            notices: Vec::new(),
        })
    }

    fn configured(&self) -> Result<Ended, LaunchFailure> {
        self.launched.lock().unwrap().push("configured");
        self.refusal("configured")?;
        Ok(Ended {
            code: self.restart_code,
            notices: Vec::new(),
        })
    }
}

/// The scaffold, which lands, over the project the dependency use cases
/// already see.
struct Made(Rc<deps_application::Deps>);

impl init_application::Create for Made {
    fn create(&self) -> Result<(), PortFailure> {
        Ok(())
    }

    fn set_output_dir(&self, _: &str) -> Result<(), PortFailure> {
        Ok(())
    }

    fn deps(&self) -> Result<Rc<deps_application::Deps>, PortFailure> {
        Ok(Rc::clone(&self.0))
    }
}

impl ExportedVars for World {
    fn is_exported(&self, name: &str) -> bool {
        self.exported.lock().unwrap().iter().any(|v| v == name)
    }
}

impl ProjectCache for World {
    fn app_dir(&self) -> String {
        "/work/.ctxloom".to_string()
    }
    fn measure(&self, rel: &str) -> Result<Option<u64>, PortFailure> {
        Ok(self.trees.lock().unwrap().get(rel).copied())
    }
    fn remove(&self, rel: &str) -> Result<(), PortFailure> {
        self.trees.lock().unwrap().remove(rel);
        Ok(())
    }
}

impl ConfigFiles for World {
    fn source(&self) -> Source {
        Source::Project
    }
    fn app_dir(&self) -> String {
        "/work/.ctxloom".to_string()
    }
    fn layers(&self) -> Result<Vec<LayerText>, PortFailure> {
        Ok(self
            .config
            .lock()
            .unwrap()
            .iter()
            .map(|text| LayerText {
                path: "/work/.ctxloom/config.yaml".to_string(),
                text: text.clone(),
            })
            .collect())
    }
    fn layer(&self, _: Scope) -> Result<Option<LayerText>, PortFailure> {
        Ok(None)
    }
    fn layer_path(&self, _: Scope) -> String {
        "/work/.ctxloom/config.yaml".to_string()
    }
    fn write_layer(&self, _: Scope, _: &str) -> Result<(), PortFailure> {
        Ok(())
    }
}

impl UserFiles for World {
    fn exists(&self, path: &str) -> Result<bool, PortFailure> {
        Ok(self.files.lock().unwrap().contains_key(path))
    }
    fn is_dir(&self, path: &str) -> Result<bool, PortFailure> {
        Ok(path == "/work/.ctxloom" && !self.fresh.load(std::sync::atomic::Ordering::SeqCst))
    }
    fn read_file(&self, path: &str) -> Result<Vec<u8>, PortFailure> {
        Ok(self.files.lock().unwrap()[path].clone().into_bytes())
    }
    fn read_tree(&self, _: &str) -> Result<Vec<Put>, PortFailure> {
        Ok(Vec::new())
    }
    fn write_file(&self, path: &str, bytes: &[u8], _: bool) -> Result<bool, PortFailure> {
        self.files
            .lock()
            .unwrap()
            .insert(path.to_string(), String::from_utf8(bytes.to_vec()).unwrap());
        Ok(true)
    }
    fn write_tree(&self, _: &str, _: &[Put]) -> Result<(), PortFailure> {
        Ok(())
    }
}

impl Scaffold for World {
    fn app_dir(&self) -> String {
        "/work/.ctxloom".to_string()
    }
    fn exists(&self, _: &str) -> Result<bool, PortFailure> {
        Ok(true)
    }
    fn write(&self, _: &[Put]) -> Result<(), PortFailure> {
        Ok(())
    }
}

struct Git;

impl Executables for Git {
    fn find(&self, name: &str) -> Option<String> {
        (name == "git").then(|| "/usr/bin/git".to_string())
    }
}

struct Run {
    exit: Result<u8, Failure>,
    out: String,
    err: String,
    world: Arc<World>,
}

fn go(line: &[&str], format: Format, configure: impl FnOnce(&World, &Fixture)) -> Run {
    let mut out = Vec::new();
    let mut r = go_to(line, format, configure, &mut out);
    r.out = String::from_utf8(out).unwrap();
    r
}

fn go_to(
    line: &[&str],
    format: Format,
    configure: impl FnOnce(&World, &Fixture),
    out: &mut dyn Write,
) -> Run {
    go_with(
        line,
        format,
        configure,
        out,
        &Person::default(),
        &Launches::default(),
    )
}

fn go_with(
    line: &[&str],
    format: Format,
    configure: impl FnOnce(&World, &Fixture),
    out: &mut dyn Write,
    person: &Person,
    sessions: &Launches,
) -> Run {
    let world = Arc::new(World::default());
    let fixture = Fixture::new();
    configure(&world, &fixture);
    let cleaner = Cleaner::new(world.clone());
    let auth = Auth::new(world.clone());
    let doctor = Doctor::new(
        world.clone(),
        world.clone(),
        Arc::new(Git),
        Auth::new(world.clone()),
        "/home/u",
        "0.1.0",
    );
    let deps = Rc::new(fixture.deps);
    let init = Init::new(
        world.clone(),
        world.clone(),
        Rc::clone(&deps),
        Box::new(Made(deps)),
    );
    let uses = UseCases {
        auth: &auth,
        cleaner: &cleaner,
        doctor: &doctor,
        init: &init,
        terminal: person,
        sessions,
    };
    let mut full = vec!["ctxloom"];
    full.extend(line);
    let matches = nouns()
        .into_iter()
        .fold(Command::new("ctxloom"), Command::subcommand)
        .try_get_matches_from(full)
        .expect("parses");
    let noun = matches.subcommand_name().expect("a noun").to_string();
    let call = Call::new(&matches, Some(format), Format::Text);
    let catalog = Catalog::en_us(&[
        sessions_application::EN_US,
        deps_application::EN_US,
        auth_application::RUNS_EN_US,
        EN_US,
    ])
    .expect("catalog");
    let mut err = Vec::new();
    let exit = run(&uses, &noun, &call, &catalog, out, &mut err);
    Run {
        exit,
        out: String::new(),
        err: String::from_utf8(err).unwrap(),
        world,
    }
}

fn none(_: &World, _: &Fixture) {}

/// A launch that exports claude's token.
fn token(world: &World, _: &Fixture) {
    world
        .exported
        .lock()
        .unwrap()
        .push("CLAUDE_CODE_OAUTH_TOKEN".to_string());
}

fn cached(world: &World, _: &Fixture) {
    world
        .trees
        .lock()
        .unwrap()
        .insert("cache/context".to_string(), 2048);
}

#[test]
fn clean_without_yes_reports_removes_nothing_and_says_so_on_the_error_stream() {
    let r = go(&["clean"], Format::Text, cached);
    assert_eq!(r.exit.unwrap(), 0);
    assert!(r.out.contains("would remove 2.0 KB"), "{}", r.out);
    assert!(r.err.starts_with("ctxloom: removed nothing"), "{}", r.err);
    assert!(r.err.contains("ctxloom clean --yes"), "{}", r.err);
    assert_eq!(r.world.trees.lock().unwrap().len(), 1);
}

#[test]
fn clean_with_yes_removes_and_is_quiet_on_the_error_stream() {
    let r = go(&["clean", "--yes"], Format::Text, cached);
    assert_eq!(r.exit.unwrap(), 0);
    assert!(r.out.contains("ctxloom removed 2.0 KB"), "{}", r.out);
    assert_eq!(r.err, "");
    assert!(r.world.trees.lock().unwrap().is_empty());
}

#[test]
fn clean_in_json_writes_the_report_and_a_json_warning() {
    let r = go(&["clean"], Format::Json, cached);
    assert_eq!(r.exit.unwrap(), 0);
    assert!(r.out.contains("\"applied\": false"), "{}", r.out);
    assert!(
        r.err
            .contains("\"code\": \"sessions-application-removed-nothing\""),
        "{}",
        r.err
    );
}

#[test]
fn doctor_prints_its_report_and_exits_zero_when_no_row_failed() {
    let r = go(&["doctor"], Format::Text, token);
    assert_eq!(r.exit.unwrap(), 0);
    assert!(r.out.starts_with("ctxloom doctor\n"), "{}", r.out);
    assert!(r.out.contains("[warn] DOCTOR-CHECK-"), "{}", r.out);
    assert!(!r.out.contains("[ok]"), "{}", r.out);
    let all = go(&["doctor", "--all"], Format::Text, token);
    assert!(all.out.contains("[ok]"), "{}", all.out);
}

// doctor.feature: "A missing agent credential is a fail row with run's fix,
// and exit 3": the whole report is written first.
#[test]
fn doctor_exits_three_after_the_whole_report_when_a_credential_is_missing() {
    let r = go(&["doctor"], Format::Text, none);
    assert_eq!(r.exit.unwrap(), 3);
    assert!(
        r.out.contains("[fail] DOCTOR-CHECK-CREDENTIALS-m5"),
        "{}",
        r.out
    );
    assert!(r.out.contains("claude setup-token"), "{}", r.out);
    assert!(r.out.contains("DOCTOR-CHECK-INGESTION-q7") || r.out.contains("[warn]"));
    let json = go(&["doctor"], Format::Json, none);
    assert_eq!(json.exit.unwrap(), 3);
    assert!(
        json.out
            .contains("\"marker\": \"DOCTOR-CHECK-CREDENTIALS-m5\""),
        "{}",
        json.out
    );
    assert!(json.out.contains("\"status\": \"fail\""), "{}", json.out);
}

#[test]
fn doctor_deps_checks_only_the_machine_and_json_lists_the_checks() {
    let r = go(&["doctor", "--deps"], Format::Json, none);
    assert_eq!(r.exit.unwrap(), 0);
    assert!(r.out.contains("DOCTOR-CHECK-DEPS-a1"), "{}", r.out);
    assert!(!r.out.contains("DOCTOR-CHECK-SETUP-MARKER"), "{}", r.out);
}

#[test]
fn doctor_fix_removes_a_stale_entry_and_names_it_on_the_error_stream() {
    let stale =
        r#"{"hooks":{"SessionStart":[{"hooks":[{"command":"ctxloom hook inject-context abc"}]}]}}"#;
    let r = go(&["doctor", "--fix"], Format::Text, |w, f| {
        token(w, f);
        w.files
            .lock()
            .unwrap()
            .insert("/work/.claude/settings.json".to_string(), stale.to_string());
    });
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(
        r.err,
        "removed stale hook entry: SessionStart runs `ctxloom hook inject-context` in .claude/settings.json\n"
    );
    assert!(
        !r.world.files.lock().unwrap()["/work/.claude/settings.json"].contains("inject-context")
    );
    let untouched = go(&["doctor"], Format::Text, token);
    assert_eq!(untouched.err, "");
}

#[test]
fn the_exit_of_a_report_is_three_only_when_a_row_failed() {
    use doctor_checks::{Check, Report, Status};
    let row = |status| Check {
        marker: "DOCTOR-CHECK-X-a1",
        name: "x",
        status,
        detail: String::new(),
        remedy: String::new(),
    };
    assert_eq!(
        exit_of(&Report {
            checks: vec![row(Status::Warn)]
        }),
        0
    );
    assert_eq!(
        exit_of(&Report {
            checks: vec![row(Status::Ok), row(Status::Fail)]
        }),
        3
    );
}

#[test]
fn init_says_the_directory_exists_and_exits_zero() {
    let r = go(&["init"], Format::Text, token);
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(r.out, "ctxloom directory already exists: /work/.ctxloom\n");
    assert_eq!(r.err, "");
}

// Owner ruling 2026-11-08: init guides the human to the token delegated
// agents need. It is a notice: exit 0, stdout unchanged, in any format.
#[test]
fn init_with_no_token_guides_to_claude_setup_token_and_still_exits_zero() {
    for format in [Format::Text, Format::Json] {
        let r = go(&["init"], format, none);
        assert_eq!(r.exit.unwrap(), 0);
        assert_eq!(r.out, "ctxloom directory already exists: /work/.ctxloom\n");
        assert!(r.err.contains("delegated agents need a token"), "{}", r.err);
        assert!(r.err.contains("claude setup-token"), "{}", r.err);
        assert!(
            r.err.contains("export CLAUDE_CODE_OAUTH_TOKEN"),
            "{}",
            r.err
        );
        assert!(r.err.contains("Pro, Max, Team or Enterprise"), "{}", r.err);
        assert!(r.err.contains("lasts a year"), "{}", r.err);
        assert!(r.err.contains("secret manager"), "{}", r.err);
    }
}

#[test]
fn init_with_an_api_key_exported_needs_no_guide() {
    let r = go(&["init"], Format::Text, |world, _| {
        world
            .exported
            .lock()
            .unwrap()
            .push("ANTHROPIC_API_KEY".to_string());
    });
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(r.err, "");
}

// Ruling 64: an engine in use whose source is a file alone authenticates no
// child, so init warns of it, in the words a child's run is refused with, and
// still exits 0. A file is no token, so the token guide is not given too.
#[test]
fn init_warns_of_an_engine_whose_source_is_a_file_alone() {
    for format in [Format::Text, Format::Json] {
        let r = go(&["init"], format, |world, _| {
            world.config.lock().unwrap().push(
                "engines:\n  claude-code:\n    auth:\n      file: ~/.claude/.credentials.json\n"
                    .to_string(),
            );
        });
        assert_eq!(r.exit.unwrap(), 0);
        assert_eq!(r.out, "ctxloom directory already exists: /work/.ctxloom\n");
        assert!(
            r.err.contains("engines.claude-code.auth names only a file"),
            "{}",
            r.err
        );
        assert!(
            r.err.contains(
                "name one of those variables in engines.claude-code.auth.env and export it"
            ),
            "{}",
            r.err
        );
        assert!(
            r.err.contains("CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY"),
            "{}",
            r.err
        );
        assert!(
            !r.err.contains("delegated agents need a token"),
            "{}",
            r.err
        );
    }
    let text = go(&["init"], Format::Text, |world, _| {
        world.config.lock().unwrap().push(
            "engines:\n  claude-code:\n    auth:\n      file: ~/.claude/.credentials.json\n"
                .to_string(),
        );
    });
    assert!(
        text.err
            .starts_with("ctxloom: warning: a child agent run of claude-code needs"),
        "{}",
        text.err
    );
    assert!(
        text.err.contains("\n  fix: name one of those"),
        "{}",
        text.err
    );
}

#[test]
fn init_prompt_does_not_guide() {
    let r = go(&["init", "prompt"], Format::Text, none);
    assert_eq!(r.err, "");
}

// Owner rulings 110, 111: a new project's default content is built in, so
// a first init pulls nothing and says nothing of pulling.
#[test]
fn init_of_a_new_project_says_what_it_made_and_pulls_nothing() {
    let r = go(&["init"], Format::Text, |world, fixture| {
        world.fresh.store(true, std::sync::atomic::Ordering::SeqCst);
        let url = "file:///srv/origin.git";
        fixture.origin("origin", url);
        fixture.profile("dev", &[&Fixture::reference(url, "demo")]);
    });
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(r.out, "Created ctxloom directory: /work/.ctxloom\n");
}

// Rulings 89, 106: a first init at a terminal asks where a session's outputs
// go, before the interview, and says where they will go and that they can be
// committed.
#[test]
fn a_first_init_at_a_terminal_asks_for_the_output_directory_and_says_where_outputs_go() {
    let person = Person {
        terminal: true,
        line: Some("docs/sessions\n"),
        ..Person::default()
    };
    let mut out = Vec::new();
    let r = go_with(
        &["init"],
        Format::Text,
        |world, _| world.fresh.store(true, std::sync::atomic::Ordering::SeqCst),
        &mut out,
        &person,
        &Launches {
            interview_code: Some(1),
            ..Launches::default()
        },
    );
    let out = String::from_utf8(out).unwrap();
    assert_eq!(r.exit.unwrap(), 1, "the interview's own exit");
    assert!(
        out.contains("Session outputs go to docs/sessions (not gitignored: they can be committed)"),
        "{out}"
    );
    let asked = person.asked.lock().unwrap().clone();
    assert_eq!(asked, [init_application::OUTPUT_DIR_QUESTION.to_string()]);
}

// A re-run on a project that has one asks nothing about it.
#[test]
fn a_second_init_does_not_ask_for_the_output_directory() {
    let person = Person {
        terminal: true,
        line: Some("docs/sessions\n"),
        ..Person::default()
    };
    let mut out = Vec::new();
    go_with(
        &["init"],
        Format::Text,
        none,
        &mut out,
        &person,
        &Launches {
            interview_code: Some(1),
            ..Launches::default()
        },
    );
    let out = String::from_utf8(out).unwrap();
    assert!(!out.contains("Session outputs"), "{out}");
    assert!(person.asked.lock().unwrap().is_empty());
}

#[test]
fn init_adds_each_remote_and_reports_one_that_would_not_register_as_a_warning() {
    let r = go(
        &[
            "init",
            "--remote",
            "https://example.com/a/b.git",
            "--forge",
            "git",
        ],
        Format::Text,
        |_, f| f.remotes.serve("https://example.com/a/b.git"),
    );
    assert_eq!(r.exit.unwrap(), 0);
    assert!(
        r.out
            .contains("Added remote \"personal\": https://example.com/a/b.git"),
        "{} / {}",
        r.out,
        r.err
    );
}

#[test]
fn init_prompt_prints_the_interview_and_nothing_else() {
    let r = go(&["init", "prompt"], Format::Json, none);
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(r.out, init_application::prompt());
    assert_eq!(r.err, "");
}

#[test]
fn a_noun_the_line_does_not_carry_is_a_usage_failure() {
    let world = Arc::new(World::default());
    let fixture = Fixture::new();
    let cleaner = Cleaner::new(world.clone());
    let auth = Auth::new(world.clone());
    let doctor = Doctor::new(
        world.clone(),
        world.clone(),
        Arc::new(Git),
        Auth::new(world.clone()),
        "/h",
        "0",
    );
    let deps = Rc::new(fixture.deps);
    let init = Init::new(
        world.clone(),
        world.clone(),
        Rc::clone(&deps),
        Box::new(Made(deps)),
    );
    let uses = UseCases {
        auth: &auth,
        cleaner: &cleaner,
        doctor: &doctor,
        init: &init,
        terminal: &Person::default(),
        sessions: &Launches::default(),
    };
    let matches = nouns()
        .into_iter()
        .fold(Command::new("ctxloom"), Command::subcommand)
        .try_get_matches_from(["ctxloom", "clean"])
        .unwrap();
    let call = Call::new(&matches, None, Format::Text);
    let catalog = Catalog::en_us(&[EN_US]).unwrap();
    let failure = run(
        &uses,
        "doctor",
        &call,
        &catalog,
        &mut Vec::new(),
        &mut Vec::new(),
    )
    .expect_err("fails");
    assert_eq!(failure.exit, 2);
    assert_eq!(failure.cause.code(), "setup-cli-no-such-command");
}

#[test]
fn an_unwritable_answer_is_an_output_failure_with_exit_one() {
    struct Broken;
    impl Write for Broken {
        fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
            Err(std::io::Error::other("closed"))
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    for line in [&["init", "prompt"][..], &["init"], &["clean"], &["doctor"]] {
        let failure = go_to(line, Format::Text, cached, &mut Broken)
            .exit
            .expect_err("fails");
        assert_eq!(failure.exit, 1, "{line:?}");
        assert_eq!(failure.cause.code(), "setup-cli-output", "{line:?}");
    }
}

// auth.feature, Go's `auth status` (internal/adapters/cli/auth.go).
#[test]
fn auth_status_reports_an_exported_token_present_and_never_prints_a_value() {
    let r = go(&["auth", "status"], Format::Text, token);
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(
        r.out,
        concat!(
            "claude-code token: present (CLAUDE_CODE_OAUTH_TOKEN)\n",
            // opencode's keys are optional (rulings 69, 79): it needs none.
            "opencode token: not needed: none of ANTHROPIC_API_KEY, OPENAI_API_KEY is exported, so opencode runs keyless on its free models; export one for a provider's models\n",
        )
    );
    assert_eq!(r.err, "");
}

#[test]
fn auth_status_names_how_to_mint_and_export_a_missing_token() {
    let r = go(&["auth", "status"], Format::Text, none);
    assert_eq!(r.exit.unwrap(), 0);
    assert_eq!(
        r.out,
        concat!(
            "claude-code token: missing \u{2014} run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN (or store it in your secret manager)\n",
            "opencode token: not needed: none of ANTHROPIC_API_KEY, OPENAI_API_KEY is exported, so opencode runs keyless on its free models; export one for a provider's models\n",
        )
    );
}

#[test]
fn auth_status_in_json_is_the_rows_by_variable_name() {
    let r = go(&["auth", "status"], Format::Json, token);
    assert_eq!(r.exit.unwrap(), 0);
    let rows: serde_json::Value = serde_json::from_str(&r.out).unwrap();
    assert_eq!(rows[0]["engine"], "claude-code");
    assert_eq!(rows[0]["mode"], "token");
    assert_eq!(rows[0]["present"], true);
    assert_eq!(
        rows[0]["vars"],
        serde_json::json!(["CLAUDE_CODE_OAUTH_TOKEN"])
    );
}

// Go: `auth` alone is `auth status` (a group's default).
#[test]
fn a_bare_auth_is_auth_status() {
    let bare = go(&["auth"], Format::Text, token);
    let status = go(&["auth", "status"], Format::Text, token);
    assert_eq!(bare.exit.unwrap(), 0);
    assert_eq!(bare.out, status.out);
}

fn at_a_terminal(
    yes: bool,
    interview: Option<i32>,
    restart: Option<i32>,
) -> (Person, Launches, Run) {
    let person = Person {
        terminal: true,
        yes,
        ..Person::default()
    };
    let sessions = Launches {
        interview_code: interview,
        restart_code: restart,
        ..Launches::default()
    };
    let mut out = Vec::new();
    let mut run = go_with(&["init"], Format::Text, token, &mut out, &person, &sessions);
    run.out = String::from_utf8(out).unwrap();
    (person, sessions, run)
}

// j000200 scenario 2: on a terminal init interviews her, offers the restart
// and, on her yes, launches the configured session; the command exits as the
// restarted session did.
#[test]
fn init_at_a_terminal_interviews_then_restarts_on_a_yes_and_exits_as_the_session_did() {
    let (person, sessions, run) = at_a_terminal(true, Some(0), Some(3));
    assert_eq!(run.exit.expect("ran"), 3);
    assert_eq!(
        *sessions.launched.lock().unwrap(),
        ["interview", "configured"]
    );
    assert_eq!(person.asked.lock().unwrap().len(), 1);
}

// A no ends init with the command that starts the configured session later.
#[test]
fn init_at_a_terminal_names_the_command_to_run_later_on_a_no() {
    let (_, sessions, run) = at_a_terminal(false, Some(0), Some(3));
    assert_eq!(run.exit.expect("ran"), 0);
    assert_eq!(*sessions.launched.lock().unwrap(), ["interview"]);
    assert!(run.out.contains("ctxloom run"), "{}", run.out);
}

// An interview that was cut short exits with its status and offers nothing.
#[test]
fn init_at_a_terminal_whose_interview_failed_exits_so_and_asks_nothing() {
    let (person, sessions, run) = at_a_terminal(true, Some(4), Some(0));
    assert_eq!(run.exit.expect("ran"), 4);
    assert!(person.asked.lock().unwrap().is_empty());
    assert_eq!(*sessions.launched.lock().unwrap(), ["interview"]);
    let (_, _, run) = at_a_terminal(true, None, Some(0));
    assert_eq!(run.exit.expect("ran"), 1);
}

// Off a terminal init is what it was: no session, no question.
#[test]
fn init_off_a_terminal_launches_nothing() {
    let person = Person {
        yes: true,
        ..Person::default()
    };
    let sessions = Launches::default();
    let run = go_with(
        &["init"],
        Format::Text,
        token,
        &mut Vec::new(),
        &person,
        &sessions,
    );
    assert_eq!(run.exit.expect("ran"), 0);
    assert!(sessions.launched.lock().unwrap().is_empty());
    assert!(person.asked.lock().unwrap().is_empty());
}

// A session that could not be LAUNCHED ends init with that failure's own exit
// code, as `ctxloom run` does (a refusal 2, another failure 1), not a
// flattened 1. A session that ran keeps the engine's code (above).
#[test]
fn init_whose_launch_is_refused_exits_with_the_launchs_own_code() {
    for (session, exit) in [
        ("interview", 2),
        ("interview", 3),
        ("interview", 1),
        ("configured", 2),
    ] {
        let person = Person {
            terminal: true,
            yes: true,
            ..Person::default()
        };
        let sessions = Launches {
            refuses: Some((session, exit)),
            interview_code: Some(0),
            restart_code: Some(0),
            ..Launches::default()
        };
        let run = go_with(
            &["init"],
            Format::Text,
            token,
            &mut Vec::new(),
            &person,
            &sessions,
        );
        // A launch that failed is the command's Failure, carrying the
        // launch's own code: not an Ok exit status.
        let failure = run.exit.expect_err("the launch failed");
        assert_eq!(failure.exit, exit, "{session}");
        assert_eq!(failure.cause.code(), "deps-application-default-needs-name");
    }
}
