//! The one launch's order (T10): set up, the engine's command, the permits,
//! the program found, the batch check, the length check; one test a step,
//! each refusing at its step and proving the step before it was taken.

use std::collections::BTreeMap;
use std::sync::Mutex;

use run_ports::{
    ChildEngine, ChildError, ChildSpec, Ignored, LaunchAt, LaunchKind, ProgramNotFound,
    StartedChild, Stdio, TurnCommand,
};

use crate::fakes::{worktree_checkout, MemoryRecords, ScriptedEngine, ScriptedWorkspaces, HARP};
use crate::launch::{launch_of, LaunchEngines, LaunchRequest, NotLaunched};
use crate::{Axis, HomeSetup, LaunchSetup, Workspace};

const PERMIT: &str = "CTXLOOM_PERMIT_ACME_TOKEN";

fn request(kind: LaunchKind) -> LaunchRequest {
    LaunchRequest {
        at: LaunchAt {
            project_id: "calm-otter".to_string(),
            session: "5f0c7a52-1111-5111-8111-000000000001".to_string(),
            harp: HARP.to_string(),
            run: "run-1".to_string(),
            incarnation: 2,
        },
        kind,
        setup: LaunchSetup {
            home: HomeSetup {
                harp: HARP.to_string(),
                context: "THE-CONTEXT".to_string(),
                interactive: kind == LaunchKind::Terminal,
                cwd: Some("/work/proj".to_string()),
                ..HomeSetup::default()
            },
            workspace: Workspace::default(),
            env: BTreeMap::from([
                ("PATH".to_string(), "/bin".to_string()),
                (PERMIT.to_string(), "acme".to_string()),
                ("CTXLOOM_PERMIT_PATH".to_string(), "/evil".to_string()),
            ]),
            owned: vec!["PATH".to_string()],
        },
        prompt: "THE-PROMPT".to_string(),
        resume: None,
        session_id: None,
        user_home: None,
        model: String::new(),
        effort: String::new(),
        settings: Default::default(),
        child_permissions: Default::default(),
        session_lock: "/work/proj/.ctxloom/sessions/amber-swift-owl/session.lock".to_string(),
    }
}

/// A finder that keeps every command it was asked to find and answers as
/// it was told.
struct Finder {
    asked: Mutex<Vec<TurnCommand>>,
    answer: Result<String, ProgramNotFound>,
}

impl Finder {
    fn finding(program: &str) -> Finder {
        Finder {
            asked: Mutex::new(Vec::new()),
            answer: Ok(program.to_string()),
        }
    }

    fn missing() -> Finder {
        Finder {
            asked: Mutex::new(Vec::new()),
            answer: Err(ProgramNotFound {
                program: "fake".to_string(),
                names: vec!["fake".to_string()],
                dirs: vec!["/bin".to_string()],
            }),
        }
    }

    fn find(&self, command: &TurnCommand) -> Result<String, ProgramNotFound> {
        self.asked.lock().unwrap().push(command.clone());
        self.answer.clone()
    }

    fn asked(&self) -> Vec<TurnCommand> {
        self.asked.lock().unwrap().clone()
    }
}

/// A child engine whose command is its spec's environment as it is, under
/// its configured program.
struct Child;

impl ChildEngine for Child {
    fn command(&self, spec: &ChildSpec) -> Result<TurnCommand, ChildError> {
        let mut command = TurnCommand::new("fake-child");
        command.args = vec![spec.context_path.clone()];
        command.env = spec.env.clone();
        command.cwd = spec.cwd.clone();
        Ok(command)
    }

    fn stdio(&self) -> Stdio {
        Stdio::StreamJson
    }

    fn start(&self, _: &ChildSpec, _: &run_ports::Launch) -> Result<StartedChild, ChildError> {
        unreachable!("launch_of starts nothing")
    }
}

fn engines(engine: &ScriptedEngine) -> LaunchEngines<'_> {
    LaunchEngines {
        engine,
        child: Some(&Child),
    }
}

// 1. Setup is first: a home that cannot be written fails the launch before
// the engine is asked for a command (the engine below would refuse to
// drive, and that is not what is said) and before any program is looked for.
#[test]
fn setup_is_first_and_a_home_that_cannot_be_written_asks_the_engine_nothing() {
    let mut records = MemoryRecords::default();
    *records.refuses.lock().unwrap() = Some("home/".to_string());
    let engine = ScriptedEngine {
        refuses_to_drive: true,
        ..ScriptedEngine::answering("", None)
    };
    let finder = Finder::finding("/found/fake");
    let refused = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut records,
        None,
        &request(LaunchKind::OneShot),
        "linux",
    )
    .unwrap_err();
    let NotLaunched::Failed(detail) = &refused.why else {
        panic!("{refused:?}");
    };
    assert!(detail.contains("permission denied"), "{detail}");
    assert!(finder.asked().is_empty());
}

// 2. The engine's command, after setup: one it will not build fails the
// launch with the home already written, and no program is looked for.
#[test]
fn the_engines_command_follows_setup_and_one_it_will_not_build_is_looked_for_nowhere() {
    let mut records = MemoryRecords::default();
    let engine = ScriptedEngine {
        refuses_to_drive: true,
        ..ScriptedEngine::answering("", None)
    };
    let finder = Finder::finding("/found/fake");
    let refused = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut records,
        None,
        &request(LaunchKind::OneShot),
        "linux",
    )
    .unwrap_err();
    let NotLaunched::Failed(detail) = &refused.why else {
        panic!("{refused:?}");
    };
    assert!(detail.contains("drive_turn"), "{detail}");
    assert_eq!(
        records.text(HARP, "home/ctx.md").as_deref(),
        Some("THE-CONTEXT"),
        "the home was set up first"
    );
    assert!(finder.asked().is_empty());
}

// 2'. Each kind's command is its own: the terminal's, a one-shot's turn
// (owner ruling s4-Q8), a child's; an editor is no engine's.
#[test]
fn each_kind_is_driven_by_its_own_command() {
    let engine = ScriptedEngine::answering("", None);
    let finder = |c: &TurnCommand| Ok(c.program.clone());
    let made = |kind| {
        launch_of(
            engines(&engine),
            &finder,
            &mut MemoryRecords::default(),
            None,
            &request(kind),
            "linux",
        )
    };
    let terminal = made(LaunchKind::Terminal).unwrap().launch;
    assert_eq!(terminal.program, "fake-terminal");
    assert!(terminal.args.contains(&"THE-PROMPT".to_string()));
    assert_eq!(terminal.stdio, Stdio::Inherit);
    let turn = made(LaunchKind::OneShot).unwrap().launch;
    assert_eq!(turn.program, "fake");
    assert_eq!(turn.stdin, b"THE-PROMPT".to_vec());
    assert_eq!(turn.stdio, Stdio::Piped);
    let child = made(LaunchKind::Child).unwrap();
    assert_eq!(child.launch.program, "fake-child");
    assert_eq!(child.launch.stdio, Stdio::StreamJson);
    assert!(child.launch.stdin.is_empty(), "a child is sent messages");
    let spec = child.child.expect("a child's spec");
    assert_eq!(
        spec.home,
        "/work/proj/.ctxloom/sessions/amber-swift-owl/home"
    );
    assert_eq!(child.launch.args, vec![spec.context_path.clone()]);
    assert!(matches!(
        made(LaunchKind::Editor).unwrap_err().why,
        NotLaunched::Failed(_)
    ));
    let no_child = launch_of(
        LaunchEngines {
            engine: &engine,
            child: None,
        },
        &finder,
        &mut MemoryRecords::default(),
        None,
        &request(LaunchKind::Child),
        "linux",
    );
    assert!(matches!(no_child.unwrap_err().why, NotLaunched::Failed(_)));
}

// Step 8 (D7): a turn is offered ctxloom's approval prompt where ctxloom's
// server rides the launch, that is every launch with context; one with none
// (OD-38) carries no server, and is offered no prompt.
#[test]
fn a_turn_is_offered_the_approval_prompt_unless_it_carries_no_context() {
    let engine = ScriptedEngine::answering("", None);
    let finder = |c: &TurnCommand| Ok(c.program.clone());
    let made = |request: &LaunchRequest| {
        launch_of(
            engines(&engine),
            &finder,
            &mut MemoryRecords::default(),
            None,
            request,
            "linux",
        )
        .unwrap()
        .launch
    };
    let offered = made(&request(LaunchKind::OneShot));
    assert!(
        !offered.args.contains(&"--no-approval-prompt".to_string()),
        "{:?}",
        offered.args
    );
    let mut bare = request(LaunchKind::OneShot);
    bare.setup.home.surfaces.0.no_context = true;
    let bare = made(&bare);
    assert!(
        bare.args.contains(&"--no-approval-prompt".to_string()),
        "{:?}",
        bare.args
    );
}

// 3. The permits, over the engine's command and before the program is looked
// for: the finder is asked for the command with the permit applied, and a
// permit for a name the run set is ignored and said.
#[test]
fn the_permits_are_applied_before_the_program_is_looked_for() {
    let engine = ScriptedEngine::answering("", None);
    let finder = Finder::finding("/found/fake");
    let made = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &request(LaunchKind::OneShot),
        "linux",
    )
    .unwrap();
    let [asked] = finder.asked().try_into().unwrap();
    assert_eq!(
        asked.env.get("ACME_TOKEN").map(String::as_str),
        Some("acme")
    );
    assert!(!asked.env.contains_key(PERMIT));
    assert_eq!(asked.env.get("PATH").map(String::as_str), Some("/bin"));
    assert_eq!(made.launch.env, asked.env);
    assert_eq!(
        made.launch.ignored,
        vec![Ignored::Owned {
            permit: "CTXLOOM_PERMIT_PATH".to_string()
        }]
    );
}

// 4. The program found is the launch's; one found nowhere is checked no
// further (it would be a batch file with a line break, and too long).
#[test]
fn the_program_found_is_the_launchs_and_one_found_nowhere_is_checked_no_further() {
    let engine = ScriptedEngine::answering("", None);
    let finder = Finder::finding("/found/fake");
    let made = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &request(LaunchKind::OneShot),
        "linux",
    )
    .unwrap();
    assert_eq!(made.launch.program, "/found/fake");

    let mut asked = request(LaunchKind::Terminal);
    asked.prompt = format!("a\nb{}", "x".repeat(40_000));
    let finder = Finder::missing();
    let refused = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &asked,
        run_ports::WINDOWS,
    )
    .unwrap_err();
    assert!(
        matches!(refused.why, NotLaunched::ProgramNotFound(_)),
        "{refused:?}"
    );
}

// 5. The batch check, on the program FOUND (the engine named `fake-terminal`;
// the host found a `.cmd`), before the length: a line break in an argument
// is refused though the command is also too long.
#[test]
fn a_batch_file_found_is_checked_before_the_commands_length() {
    let engine = ScriptedEngine::answering("", None);
    let mut asked = request(LaunchKind::Terminal);
    asked.prompt = format!("a\nb{}", "x".repeat(40_000));
    let finder = Finder::finding(r"C:\npm\fake-terminal.cmd");
    let refused = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &asked,
        run_ports::WINDOWS,
    )
    .unwrap_err();
    let NotLaunched::BatchArguments(refusal) = &refused.why else {
        panic!("{refused:?}");
    };
    assert_eq!(refusal.program, r"C:\npm\fake-terminal.cmd");
    assert_eq!(refused.ignored.len(), 1, "the permits were applied");
}

// 6. The length, last, of the COMPLETE command: a permit's value (applied at
// step 3) is what makes it too long, and what the steps before said is kept.
#[test]
fn the_complete_command_is_measured_last() {
    let engine = ScriptedEngine::answering("", None).leaving_out();
    let mut asked = request(LaunchKind::OneShot);
    asked
        .setup
        .env
        .insert(PERMIT.to_string(), "x".repeat(run_ports::MAX_ARG_STRLEN));
    let finder = Finder::finding("/found/fake");
    let refused = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &asked,
        "linux",
    )
    .unwrap_err();
    assert!(
        matches!(refused.why, NotLaunched::TooLong(_)),
        "{refused:?}"
    );
    assert_eq!(finder.asked().len(), 1, "found before it was measured");
    assert_eq!(refused.left_out.len(), 1);
    assert_eq!(refused.ignored.len(), 1);
    // Nothing is measured for an OS with no limit of its own.
    assert!(launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &asked,
        "",
    )
    .is_ok());
}

// A child's launch (T10): its program found and its permits applied by the
// one launch, never by its engine; it is stopped by its input first.
#[test]
fn a_childs_launch_has_its_program_found_and_its_permits_applied() {
    let engine = ScriptedEngine::answering("", None);
    let finder = Finder::finding("/found/fake-child");
    let made = launch_of(
        engines(&engine),
        &|c| finder.find(c),
        &mut MemoryRecords::default(),
        None,
        &request(LaunchKind::Child),
        "linux",
    )
    .unwrap();
    let launch = made.launch;
    assert_eq!(launch.kind, LaunchKind::Child);
    assert_eq!(launch.program, "/found/fake-child");
    assert_eq!(
        launch.env.get("ACME_TOKEN").map(String::as_str),
        Some("acme")
    );
    assert_eq!(launch.env.get("PATH").map(String::as_str), Some("/bin"));
    assert!(launch.env.keys().all(|n| !n.starts_with("CTXLOOM_PERMIT_")));
    assert_eq!(launch.ignored.len(), 1);
    assert!(launch.stop.close_stdin_first);
    assert_eq!(
        launch.env.get(run_ports::ROOT_VARIABLE).map(String::as_str),
        Some("/work/proj")
    );
}

// What a launch says of the run it starts: its run, its engine, its home,
// its lock, where it works (a checkout placed), and what was left out.
#[test]
fn a_launch_names_its_run_home_lock_and_checkout() {
    let engine = ScriptedEngine::answering("", None).leaving_out();
    let workspaces = ScriptedWorkspaces::new(worktree_checkout);
    let mut asked = request(LaunchKind::OneShot);
    asked.setup.workspace = Workspace {
        axis: Axis::Worktree,
        work_dir: crate::fakes::WORK_DIR.to_string(),
        agent: "dev".to_string(),
    };
    let made = launch_of(
        engines(&engine),
        &|c| Ok(c.program.clone()),
        &mut MemoryRecords::default(),
        Some(&workspaces),
        &asked,
        "linux",
    )
    .unwrap();
    let launch = &made.launch;
    assert_eq!(launch.at, asked.at);
    assert_eq!(launch.engine, "fake");
    assert_eq!(
        launch.session_home,
        "/work/proj/.ctxloom/sessions/amber-swift-owl/home"
    );
    assert_eq!(launch.session_lock, asked.session_lock);
    let placed = made.placed.expect("placed");
    assert_eq!(launch.cwd.as_deref(), Some(placed.cwd.as_str()));
    assert!(placed.checkout.is_some());
    assert_eq!(launch.left_out.len(), 1);
    assert!(!launch.stop.close_stdin_first);
}
