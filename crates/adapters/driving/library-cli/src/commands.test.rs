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
fn test_an_agent_is_written_with_engine_model_and_effort() {
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
        // Owner ruling 138: `--permissions` is gone, an unknown flag.
        let argv = ["agent", verb, "dev", "--permissions", "plan"];
        assert!(parse(&argv).is_err(), "{verb} --permissions still parses");
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

// owner ruling of 2026-11-13 (superseding 44(b) for this key): an agent
// says its session_home again; the retired engine_home is no flag
#[test]
fn test_an_agent_takes_a_session_home_and_no_engine_home() {
    assert!(parse(&["agent", "edit", "dev", "--engine-home", "host"]).is_err());
    assert!(parse(&["agent", "edit", "dev", "--session-home", "host"]).is_ok());
    assert!(parse(&["agent", "create", "dev", "--session-home", "session"]).is_ok());
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

// Each item noun's create names the reference form of its own kind.
#[test]
fn test_each_item_create_names_its_own_reference_form() {
    let help = |noun: &str| -> String {
        let noun = nouns()
            .into_iter()
            .find(|n| n.get_name() == noun)
            .expect("the noun");
        let create = noun.find_subcommand("create").expect("create").clone();
        let text = create
            .get_arguments()
            .find(|a| a.get_id() == "bundle")
            .and_then(|a| a.get_help().map(ToString::to_string))
            .expect("the bundle argument's help");
        text
    };
    assert!(
        help("fragment").starts_with("bundle#fragments/name"),
        "{}",
        help("fragment")
    );
    assert!(
        help("command").starts_with("bundle#commands/name"),
        "{}",
        help("command")
    );
}
