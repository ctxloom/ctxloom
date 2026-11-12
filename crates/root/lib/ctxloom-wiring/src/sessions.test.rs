//! The mounted `session` and `hook` nouns.

use cli_support::Exit;
use std::sync::Arc;

use angzarr_presentation::{Catalog, Format};
use clap::Command;
use ctxloom_cli::{Call, Extension};

use crate::commands::tests::World;
use crate::Commands;

fn commands() -> Commands {
    Commands::new(
        Arc::new(crate::tests::host_snapshot()),
        crate::tests::VERSION,
    )
}

/// Run `line` as the command line would: the nouns parsed, then handed over.
fn run(
    commands: &mut Commands,
    line: &[&str],
) -> Result<(u8, String, String), ctxloom_cli::Failure> {
    run_with(commands, "", line)
}

/// As [`run`], with `input` as the hook's payload (stdin, in the binary).
fn run_with(
    commands: &mut Commands,
    input: &str,
    line: &[&str],
) -> Result<(u8, String, String), ctxloom_cli::Failure> {
    let program = Command::new("ctxloom")
        .subcommand_required(true)
        .subcommands(commands.nouns());
    let matches = program
        .try_get_matches_from(std::iter::once("ctxloom").chain(line.iter().copied()))
        .unwrap();
    let noun = matches.subcommand_name().unwrap().to_string();
    let call = Call::with_globals(
        &matches,
        Some(Format::Text),
        Format::Text,
        library_application::Flags::default(),
    );
    let catalog = Catalog::en_us(&[crate::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = if noun == "hook" {
        commands.hook(input, &call, &catalog, &mut out, &mut err)?
    } else {
        commands.run(&noun, &call, &catalog, &mut out, &mut err)?
    };
    Ok((
        code.code(true),
        String::from_utf8(out).unwrap(),
        String::from_utf8(err).unwrap(),
    ))
}

#[test]
fn test_the_nouns_include_session_and_a_hidden_hook() {
    let nouns = commands().nouns();
    let find = |name: &str| nouns.iter().find(|n| n.get_name() == name).expect(name);
    assert!(!find("session").is_hide_set());
    assert!(find("hook").is_hide_set());
    assert_eq!(
        nouns.iter().filter(|n| n.get_name() == "session").count(),
        1
    );
}

#[test]
fn test_a_bare_session_is_its_default_verb_not_its_usage() {
    let noun = commands()
        .nouns()
        .into_iter()
        .find(|n| n.get_name() == "session")
        .unwrap();
    assert!(!noun.is_arg_required_else_help_set());
    assert!(noun.is_disable_help_subcommand_set());
    let hook = commands()
        .nouns()
        .into_iter()
        .find(|n| n.get_name() == "hook")
        .unwrap();
    assert!(hook.is_arg_required_else_help_set());
}

#[test]
fn test_an_unknown_session_verb_is_refused_by_the_parser_with_a_suggestion() {
    let program = Command::new("ctxloom").subcommands(commands().nouns());
    let refused = program
        .try_get_matches_from(["ctxloom", "session", "lisst"])
        .unwrap_err();
    assert_eq!(refused.kind(), clap::error::ErrorKind::InvalidSubcommand);
    let hook = Command::new("ctxloom")
        .subcommands(commands().nouns())
        .try_get_matches_from(["ctxloom", "hook", "claude-code", "nosuch"])
        .unwrap_err();
    // `execute` is the only verb of an engine's hook.
    assert_eq!(hook.kind(), clap::error::ErrorKind::UnknownArgument);
}

#[test]
fn test_the_hud_hook_writes_its_statusline_to_stdout_only() {
    // The payload is read from stdin, which is not a document here: the
    // statusline hook answers with the bare name.
    let (code, out, err) = run(
        &mut commands(),
        &["hook", "claude-code", "execute", "statusline"],
    )
    .ok()
    .unwrap();
    assert_eq!(code, 0);
    assert!(out.contains("ctxloom"), "{out}");
    assert!(err.is_empty());
}

#[test]
fn test_each_session_hook_answers_through_the_engines_codec() {
    for native in ["PostToolUse", "Stop"] {
        let line = ["hook", "claude-code", "execute", native];
        let (code, out, _) = run_with(&mut commands(), r#"{"skill":"x"}"#, &line)
            .ok()
            .unwrap();
        assert_eq!((code, out.as_str()), (0, "{}\n"), "{native}");
    }
}

// A session start in a process with no harp binds nothing and says nothing.
#[test]
fn test_a_session_start_with_no_harp_is_silent() {
    let mut world = World::new("session-start-no-harp");
    world.set("CTXLOOM_SESSION_HARP", "");
    let (code, out, _) = run_with(
        &mut world.commands(),
        "{}",
        &["hook", "claude-code", "execute", "SessionStart"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (0, ""));
}

#[test]
fn test_a_hook_for_an_engine_nobody_knows_is_refused_with_exit_20() {
    let failure = run(&mut commands(), &["hook", "nosuch", "execute", "Stop"])
        .err()
        .unwrap();
    assert_eq!(failure.exit, Exit::REFUSED);
}

// `session list` runs on the sessions use cases over the world's home: no
// session is recorded there, so it answers an empty list and exits 0.
#[test]
fn test_a_session_command_is_run_on_the_sessions_use_cases() {
    let world = World::new("session-list");
    let (code, out, err) = run(&mut world.commands(), &["session", "list"])
        .ok()
        .unwrap();
    assert_eq!(code, 0, "{err}");
    assert!(err.is_empty(), "{err}");
    assert_eq!(out, "(no sessions)\n");
}

// The hook that binds a session answers the engine with the harp the process
// names (`CTXLOOM_SESSION_HARP`) on stdout, and nothing on stderr.
#[test]
fn test_the_bind_hook_answers_with_the_harp_the_environment_names() {
    let mut world = World::new("session-bind");
    world.set("CTXLOOM_SESSION_HARP", "jolly-mute-magma");
    let payload = r#"{"session_id":"s1","transcript_path":"/tmp/t.jsonl"}"#;
    let (code, out, err) = run_with(
        &mut world.commands(),
        payload,
        &["hook", "claude-code", "execute", "SessionStart"],
    )
    .ok()
    .unwrap();
    assert_eq!(code, 0);
    assert!(out.contains("jolly-mute-magma"), "{out}");
    assert!(err.is_empty(), "{err}");
}

// The statusline hook shows the harp the environment names.
#[test]
fn test_the_hud_hook_shows_the_harp_the_environment_names() {
    let mut world = World::new("hud-harp");
    world.set("CTXLOOM_SESSION_HARP", "jolly-mute-magma");
    let (_, out, _) = run_with(
        &mut world.commands(),
        r#"{"model":{"display_name":"M"}}"#,
        &["hook", "claude-code", "execute", "statusline"],
    )
    .ok()
    .unwrap();
    assert!(out.contains("jolly-mute-magma"), "{out}");
}

// Hook capture, fail open: a hook that observes something it cannot deliver
// (no listener yet) answers the engine exactly as before, exits 0, and says
// on stderr, once per observation, what it did not record.
#[test]
fn test_a_hook_that_cannot_record_still_answers_and_says_so_on_stderr() {
    let mut world = World::new("hook-not-recorded");
    world.set("CTXLOOM_SESSION_HARP", "jolly-mute-magma");
    let payload = r#"{"session_id":"s1","prompt_id":"p1","prompt":"echo hi"}"#;
    let (code, out, err) = run_with(
        &mut world.commands(),
        payload,
        &["hook", "claude-code", "execute", "UserPromptSubmit"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (0, "{}\n"));
    assert_eq!(err.matches("hook-cli-not-recorded").count(), 1, "{err}");
    assert!(
        err.contains("no session for jolly-mute-magma: CTXLOOM_SESSION names none"),
        "{err}"
    );
}

// What observes nothing records nothing and says nothing.
#[test]
fn test_a_hook_that_observes_nothing_says_nothing_on_stderr() {
    let mut world = World::new("hook-observes-nothing");
    world.set("CTXLOOM_SESSION_HARP", "jolly-mute-magma");
    let (code, out, err) = run_with(
        &mut world.commands(),
        r#"{"skill":"x"}"#,
        &["hook", "claude-code", "execute", "PostToolUse"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (0, "{}\n"));
    assert!(err.is_empty(), "{err}");
}

// ------------------------------------------------ closed, not dropped (ruling 131)
//
// A session command's stores are closed before the command answers. Seen from
// the file while the use cases are still alive: a store held open keeps its
// log beside it, a closed one leaves nothing; a drop, which these replace,
// would leave it to the driver's thread.

fn project(id: &str) -> ports::Project {
    ports::Project {
        id: id.to_string(),
        root: None,
        store: std::path::PathBuf::from(format!("/unused/{id}/state.db")),
    }
}

/// What is beside project `id`'s database under `home`.
fn beside(home: &std::path::Path, id: &str) -> Vec<&'static str> {
    let dir = home.join(".ctxloom/projects").join(id);
    ["ctxloom.db-wal", "ctxloom.db-shm"]
        .into_iter()
        .filter(|name| crate::fixtures::exists(dir.join(name)))
        .collect()
}

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
}

/// Both use cases' stores held open to write: the log beside each.
fn held<'a>(commands: &'a mut Commands, home: &std::path::Path) -> &'a mut crate::Sessions {
    use session_ports::ProjectStores;
    let sessions = commands.sessions().ok().unwrap();
    let runtime = runtime();
    runtime
        .block_on(ProjectStores::open(
            &mut sessions.use_cases.stores,
            &project("p"),
        ))
        .unwrap();
    runtime
        .block_on(ProjectStores::open(
            &mut sessions.importer.stores,
            &project("q"),
        ))
        .unwrap();
    for id in ["p", "q"] {
        assert!(
            beside(home, id).contains(&"ctxloom.db-wal"),
            "{id}: held open, its log beside it"
        );
    }
    sessions
}

#[test]
fn test_closing_the_sessions_closes_both_use_cases_stores() {
    let world = World::new("sessions-close");
    let home = world.home();
    let mut commands = world.commands();
    let sessions = held(&mut commands, &home);
    runtime().block_on(sessions.close()).unwrap();
    // The use cases are still alive: nothing was dropped.
    for id in ["p", "q"] {
        assert_eq!(beside(&home, id), Vec::<&str>::new(), "{id}: closed");
    }
}

#[test]
fn test_a_command_s_answer_stands_once_its_stores_are_closed() {
    let world = World::new("closing");
    let home = world.home();
    let mut commands = world.commands();
    held(&mut commands, &home);
    let catalog = Catalog::en_us(&[crate::EN_US]).unwrap();
    let mut err = Vec::new();
    let answer = commands.closing(Ok(Exit::WOULD_CHANGE), Format::Text, &catalog, &mut err);
    assert_eq!(
        answer.ok(),
        Some(Exit::WOULD_CHANGE),
        "the command's own answer"
    );
    assert!(commands.sessions.is_none(), "the use cases are let go");
    for id in ["p", "q"] {
        assert_eq!(beside(&home, id), Vec::<&str>::new(), "{id}: closed");
    }
    assert!(err.is_empty(), "{}", String::from_utf8_lossy(&err));
}
