//! The mounted `session` and `hook` nouns.

use cli_support::Exit;
use std::sync::Arc;

use clap::Command;
use ctxloom_cli::{Call, Extension};
use presentation::{Catalog, Format};

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

// Step 8 U1: `trust` is a noun of its own, shown.
#[test]
fn test_the_nouns_include_trust_with_its_list_verb() {
    let nouns = commands().nouns();
    let trust = nouns
        .iter()
        .find(|n| n.get_name() == super::TRUST_NOUN)
        .expect("trust");
    assert!(!trust.is_hide_set());
    assert!(trust.get_subcommands().any(|v| v.get_name() == "list"));
}

// A trust is given only on an explicit yes: a bare Enter is a no.
#[test]
fn test_only_y_or_yes_is_a_yes_to_trust() {
    for yes in ["y\n", "Y", " yes \n", "YES"] {
        assert!(super::said_yes(yes), "{yes:?}");
    }
    for no in ["", "\n", "n", "no", "yess", "sure"] {
        assert!(!super::said_yes(no), "{no:?}");
    }
}

/// An environment of `vars` alone.
fn env_of(vars: &[(&str, &str)]) -> process_env::EnvSnapshot {
    let base = crate::tests::host_snapshot();
    process_env::EnvSnapshot::from_parts(
        base.flavor(),
        Ok(base.temp_dir().to_path_buf()),
        base.temp_dir().to_path_buf(),
        vars.iter()
            .map(|(n, v)| (std::ffi::OsString::from(n), std::ffi::OsString::from(v)))
            .collect::<Vec<_>>(),
    )
}

// A trust's actor is the login the environment says: USER, else LOGNAME,
// else Windows' USERNAME; none said, empty.
#[test]
fn test_the_users_login_is_read_from_user_then_logname_then_username() {
    let login = |vars: &[(&str, &str)]| super::user_login(&env_of(vars));
    assert_eq!(
        login(&[("USER", "alice"), ("LOGNAME", "bob"), ("USERNAME", "carol")]),
        "alice"
    );
    assert_eq!(login(&[("LOGNAME", "bob"), ("USERNAME", "carol")]), "bob");
    assert_eq!(login(&[("USERNAME", "carol")]), "carol");
    assert_eq!(login(&[("USER", ""), ("LOGNAME", "bob")]), "bob");
    assert_eq!(login(&[]), "");
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
    assert_eq!(code, 16);
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
        assert_eq!((code, out.as_str()), (16, "{}\n"), "{native}");
    }
}

// A session start in a process with no harp binds nothing and says nothing.
#[test]
fn test_a_session_start_with_no_harp_is_silent() {
    let mut world = World::new("session-start-no-harp");
    world.set("CTXLOOM_SESSION", "");
    let (code, out, _) = run_with(
        &mut world.commands(),
        "{}",
        &["hook", "claude-code", "execute", "SessionStart"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (16, ""));
}

#[test]
fn test_a_hook_for_an_engine_nobody_knows_is_refused_with_exit_20() {
    let failure = run(&mut commands(), &["hook", "nosuch", "execute", "Stop"])
        .err()
        .unwrap();
    assert_eq!(failure.exit, Exit::REFUSED);
}

// `session transcript list` runs on the sessions use cases over the world's
// home (`list`, `search` and `show` are the daemon's, and its client's
// acceptance tests are the binary's): no session is recorded there, so it
// answers an empty list and exits 0.
#[test]
fn test_a_session_command_is_run_on_the_sessions_use_cases() {
    let world = World::new("session-list");
    let (code, out, err) = run(&mut world.commands(), &["session", "transcript", "list"])
        .ok()
        .unwrap();
    assert_eq!(code, 16, "{err}");
    assert!(err.is_empty(), "{err}");
    assert_eq!(out, "(no sessions)\n");
}

// The session-start hook answers the engine with the harp the process names
// (`CTXLOOM_SESSION`, the session's resource name: s3-Q3) on stdout, and
// nothing on stderr. (A start that names a conversation also binds it,
// delivered as capture is: `capture.test.rs`.)
#[test]
fn test_the_session_start_hook_answers_with_the_harp_the_environment_names() {
    let mut world = World::new("session-bind");
    world.set("CTXLOOM_SESSION", NAMED);
    let payload = "{}";
    let (code, out, err) = run_with(
        &mut world.commands(),
        payload,
        &["hook", "claude-code", "execute", "SessionStart"],
    )
    .ok()
    .unwrap();
    assert_eq!(code, 16);
    assert!(out.contains("jolly-mute-magma"), "{out}");
    assert!(err.is_empty(), "{err}");
}

/// A session's resource name, as `CTXLOOM_SESSION` carries it (s3-Q3).
const NAMED: &str = "projects/amber-quiet-heron/sessions/jolly-mute-magma";

// The statusline hook shows the harp of the session the environment names.
#[test]
fn test_the_hud_hook_shows_the_harp_the_environment_names() {
    let mut world = World::new("hud-harp");
    world.set("CTXLOOM_SESSION", NAMED);
    let (_, out, _) = run_with(
        &mut world.commands(),
        r#"{"model":{"display_name":"M"}}"#,
        &["hook", "claude-code", "execute", "statusline"],
    )
    .ok()
    .unwrap();
    assert!(out.contains("jolly-mute-magma"), "{out}");
}

// Hook capture: a hook that observes something it cannot deliver (no
// daemon serves, and none could be started) answers the engine exactly as
// before, exits 0, holds what it observed (s4-Q14, H-Q2) and says so on
// stderr, once per observation.
#[test]
fn test_a_hook_that_cannot_record_still_answers_and_says_so_on_stderr() {
    let mut world = World::new("hook-not-recorded");
    world.set("CTXLOOM_SESSION", NAMED);
    let payload = r#"{"session_id":"s1","prompt_id":"p1","prompt":"echo hi"}"#;
    let (code, out, err) = run_with(
        &mut world.commands(),
        payload,
        &["hook", "claude-code", "execute", "UserPromptSubmit"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (16, "{}\n"));
    assert_eq!(err.matches("hook-cli-held").count(), 1, "{err}");
    assert!(
        err.contains("jolly-mute-magma") && err.contains("hook:prompt:"),
        "{err}"
    );
}

// What observes nothing records nothing and says nothing.
#[test]
fn test_a_hook_that_observes_nothing_says_nothing_on_stderr() {
    let mut world = World::new("hook-observes-nothing");
    world.set("CTXLOOM_SESSION", NAMED);
    let (code, out, err) = run_with(
        &mut world.commands(),
        r#"{"skill":"x"}"#,
        &["hook", "claude-code", "execute", "PostToolUse"],
    )
    .ok()
    .unwrap();
    assert_eq!((code, out.as_str()), (16, "{}\n"));
    assert!(err.is_empty(), "{err}");
}
