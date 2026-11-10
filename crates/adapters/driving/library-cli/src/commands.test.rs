//! RED (awaiting review): the command tree after the engines schema (owner
//! rulings 40-44): `engine` is a read-only noun, an agent is written with
//! `--engine --model --effort --permissions`, and the `llm` noun and every
//! `--llm` are gone.

use clap::Command;

use super::nouns;

fn parse(line: &[&str]) -> Result<clap::ArgMatches, clap::Error> {
    Command::new("ctxloom")
        .subcommands(nouns())
        .try_get_matches_from(std::iter::once("ctxloom").chain(line.iter().copied()))
}

fn noun_names() -> Vec<String> {
    nouns().iter().map(|n| n.get_name().to_string()).collect()
}

#[test]
fn test_engine_is_a_read_only_noun_with_list_and_show() {
    let engine = nouns()
        .into_iter()
        .find(|n| n.get_name() == "engine")
        .expect("the engine noun");
    let verbs: Vec<&str> = engine.get_subcommands().map(Command::get_name).collect();
    assert_eq!(
        verbs,
        ["list", "show"],
        "read-only: no create, edit or remove"
    );
    assert!(parse(&["engine", "show", "claude-code"]).is_ok());
    assert!(parse(&["engine", "list"]).is_ok());
}

#[test]
fn test_the_llm_noun_is_gone() {
    assert!(
        !noun_names().contains(&"llm".to_string()),
        "{:?}",
        noun_names()
    );
    assert!(parse(&["llm", "list"]).is_err());
}

#[test]
fn test_an_agent_is_written_with_engine_model_effort_and_permissions() {
    for verb in ["create", "edit"] {
        let matches = parse(&[
            "agent",
            verb,
            "dev",
            "--engine",
            "claude-code",
            "--model",
            "haiku",
            "--effort",
            "low",
            "--permissions",
            "plan",
        ])
        .unwrap_or_else(|e| panic!("{verb}: {e}"));
        let (_, agent) = matches.subcommand().unwrap();
        let (_, sub) = agent.subcommand().unwrap();
        assert_eq!(
            sub.get_one::<String>("engine").map(String::as_str),
            Some("claude-code")
        );
        assert_eq!(
            sub.get_one::<String>("model").map(String::as_str),
            Some("haiku")
        );
        assert_eq!(
            sub.get_one::<String>("effort").map(String::as_str),
            Some("low")
        );
        assert_eq!(
            sub.get_one::<String>("permissions").map(String::as_str),
            Some("plan")
        );
    }
}

#[test]
fn test_no_command_takes_llm_any_more() {
    for line in [
        &["agent", "create", "dev", "--llm", "x"][..],
        &["agent", "edit", "dev", "--llm", "x"],
        &["profile", "create", "dev", "--llm", "x"],
        &["profile", "edit", "dev", "--llm", "x"],
        &["run", "--llm", "x"],
        &["run", "-l", "x"],
    ] {
        assert!(parse(line).is_err(), "{line:?} still parses");
    }
}

// the engine's home moved to the engine block: an agent no longer carries it
#[test]
fn test_an_agent_no_longer_takes_an_engine_home() {
    assert!(parse(&["agent", "edit", "dev", "--engine-home", "host"]).is_err());
    assert!(parse(&["agent", "edit", "dev", "--session-home", "host"]).is_err());
}

// the run names its engine the way an agent does
#[test]
fn test_run_names_its_engine_with_engine() {
    assert!(parse(&["run", "--dry-run", "--engine", "claude-code"]).is_ok());
}

// owner ruling (env_host removed): the engine environment is the curated
// allowlist plus the names in --env; there is no whole-environment mode
#[test]
fn test_an_agent_takes_no_env_host() {
    for verb in ["create", "edit"] {
        assert!(parse(&["agent", verb, "dev", "--env-host"]).is_err());
        assert!(parse(&["agent", verb, "dev", "--env-host=false"]).is_err());
        assert!(parse(&["agent", verb, "dev", "--env", "A"]).is_ok());
    }
}
