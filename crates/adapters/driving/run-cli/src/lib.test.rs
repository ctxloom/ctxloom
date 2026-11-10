//! `run --one-shot` as the program runs it: the line parsed by the library's
//! `run` command with the one-shot flags added, the use case a double that
//! remembers what it was asked.

use angzarr_presentation::{Catalog, Format};
use clap::{Arg, ArgAction, Command};
use library_application::{AppError, Flags, Notice};
use run_application::{Answer, Done, Ended, OneShot, RunError, RunService};
use runs::RunRejection;

use super::{is_one_shot, run, session, with_one_shot, Call};

/// The library's `run`, as far as this crate reads it.
fn library_run() -> Command {
    Command::new("run")
        .arg(Arg::new("prompt_words").num_args(0..))
        .arg(Arg::new("prompt").long("prompt"))
        .arg(Arg::new("command").short('r').long("command"))
        .arg(Arg::new("agent").long("agent"))
        .arg(Arg::new("profile").short('p').long("profile"))
        .arg(
            Arg::new("fragment")
                .short('f')
                .long("fragment")
                .value_delimiter(',')
                .action(ArgAction::Append),
        )
        .arg(
            Arg::new("tag")
                .short('t')
                .long("tag")
                .value_delimiter(',')
                .action(ArgAction::Append),
        )
        .arg(Arg::new("workspace").long("workspace"))
}

struct Double {
    ended: Option<Result<Ended, RunError>>,
    asked: Vec<(Flags, OneShot)>,
    reply: Option<Result<Answer, RunError>>,
}

impl Double {
    fn answering(answer: &str) -> Double {
        Double {
            ended: None,
            asked: Vec::new(),
            reply: Some(Ok(Answer {
                harp: "amber-swift-owl".to_string(),
                session: "5f0c7a52-1111-5111-8111-000000000001".to_string(),
                engine: "claude-code".to_string(),
                answer: answer.to_string(),
            })),
        }
    }

    fn failing(error: RunError) -> Double {
        Double {
            ended: None,
            asked: Vec::new(),
            reply: Some(Err(error)),
        }
    }
}

impl RunService for Double {
    fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended> {
        self.asked.push((flags.clone(), request.clone()));
        Done {
            result: self.ended.take().expect("asked once"),
            notices: vec![Notice::text("a notice".to_string())],
        }
    }

    fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        self.asked.push((flags.clone(), request.clone()));
        Done {
            result: self.reply.take().expect("asked once"),
            notices: vec![Notice::text("a notice".to_string())],
        }
    }
}

struct Ran {
    exit: Result<u8, (String, u8)>,
    /// The codes of the refusals the failure handed on.
    refusals: Vec<String>,
    out: String,
    err: String,
}

fn go(line: &[&str], format: Option<Format>, piped: Option<&str>, service: &mut Double) -> Ran {
    let program = Command::new("ctxloom").subcommand(with_one_shot(library_run()));
    let mut argv = vec!["ctxloom", "run"];
    argv.extend_from_slice(line);
    let matches = program.try_get_matches_from(argv).unwrap();
    let run_matches = matches.subcommand_matches("run").unwrap();
    assert!(is_one_shot(run_matches) || !line.contains(&"--one-shot"));
    let call = Call {
        matches: &matches,
        explicit_format: format,
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: Flags {
            degraded: true,
            no_companions: false,
            config_set: Vec::new(),
        },
    };
    let catalog = Catalog::en_us(&[library_application::EN_US, crate::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let mut stdin = || piped.map(String::from);
    let ran = run(
        service,
        run_matches,
        &call,
        &catalog,
        &mut stdin,
        &mut out,
        &mut err,
    );
    let refusals = ran.as_ref().err().map_or_else(Vec::new, |f| {
        f.refusals.iter().map(|r| r.code().to_string()).collect()
    });
    let exit = ran.map_err(|f| (f.cause.code().to_string(), f.exit));
    Ran {
        exit,
        refusals,
        out: String::from_utf8(out).unwrap(),
        err: String::from_utf8(err).unwrap(),
    }
}

#[test]
fn the_one_shot_flag_is_read_from_the_line() {
    let program = Command::new("ctxloom").subcommand(with_one_shot(library_run()));
    let yes = program
        .clone()
        .try_get_matches_from(["ctxloom", "run", "--one-shot", "hi"])
        .unwrap();
    assert!(is_one_shot(yes.subcommand_matches("run").unwrap()));
    let no = program
        .try_get_matches_from(["ctxloom", "run", "hi"])
        .unwrap();
    assert!(!is_one_shot(no.subcommand_matches("run").unwrap()));
}

// run.feature: the mode has one spelling.
#[test]
fn a_flag_that_is_not_the_modes_name_is_still_unknown() {
    let program = Command::new("ctxloom").subcommand(with_one_shot(library_run()));
    assert!(program
        .try_get_matches_from(["ctxloom", "run", "--print", "hello"])
        .is_err());
}

#[test]
fn the_prompt_words_are_the_prompt_and_the_reply_is_written_as_text() {
    let mut service = Double::answering("THE-REPLY\n\n");
    let ran = go(
        &["--one-shot", "what", "does", "this", "expect?"],
        Some(Format::Text),
        None,
        &mut service,
    );
    assert_eq!(ran.exit, Ok(0));
    assert_eq!(ran.out, "THE-REPLY\n");
    assert_eq!(service.asked[0].1.run.prompt, "what does this expect?");
    assert!(service.asked[0].0.degraded, "the global flags ride along");
}

#[test]
fn the_reply_as_json_names_the_session_that_made_it() {
    let mut service = Double::answering("THE-REPLY");
    let ran = go(
        &["--one-shot", "hi"],
        Some(Format::Json),
        None,
        &mut service,
    );
    let reply: serde_json::Value = serde_json::from_str(&ran.out).unwrap();
    assert_eq!(
        reply,
        serde_json::json!({
            "harp": "amber-swift-owl",
            "session": "5f0c7a52-1111-5111-8111-000000000001",
            "engine": "claude-code",
            "answer": "THE-REPLY",
        })
    );
}

#[test]
fn the_flag_and_the_words_both_give_a_prompt_and_the_flag_wins() {
    let mut service = Double::answering("A");
    go(
        &["--one-shot", "--prompt", "from the flag", "from", "words"],
        Some(Format::Text),
        None,
        &mut service,
    );
    assert_eq!(service.asked[0].1.run.prompt, "from the flag");
}

#[test]
fn a_prompt_piped_in_is_read_only_when_nothing_else_gave_one() {
    let mut service = Double::answering("A");
    go(
        &["--one-shot", "--profile", "dev"],
        Some(Format::Text),
        Some("PIPED-PROMPT"),
        &mut service,
    );
    assert_eq!(service.asked[0].1.run.prompt, "PIPED-PROMPT");

    let mut service = Double::answering("A");
    go(
        &["--one-shot", "given"],
        Some(Format::Text),
        Some("PIPED-PROMPT"),
        &mut service,
    );
    assert_eq!(service.asked[0].1.run.prompt, "given");

    // A saved command is the prompt: standard input is not read for one.
    let mut service = Double::answering("A");
    go(
        &["--one-shot", "-r", "review"],
        Some(Format::Text),
        Some("PIPED-PROMPT"),
        &mut service,
    );
    assert_eq!(service.asked[0].1.run.prompt, "");
    assert_eq!(service.asked[0].1.run.saved_command, "review");
}

#[test]
fn every_selection_flag_reaches_the_request() {
    let mut service = Double::answering("A");
    go(
        &[
            "--one-shot",
            "--agent",
            "dev",
            "--profile",
            "p",
            "-f",
            "a,b",
            "-t",
            "go",
            "--workspace",
            "worktree",
            "--engine",
            "claude-code",
            "hi",
        ],
        Some(Format::Text),
        None,
        &mut service,
    );
    let asked = &service.asked[0].1;
    assert_eq!(
        asked.run.agent.as_str(),
        "dev",
        "the agent conflict is the library's to refuse"
    );
    assert_eq!(asked.run.profile, "p");
    assert_eq!(asked.run.fragments, ["a", "b"]);
    assert_eq!(asked.run.tags, ["go"]);
    assert_eq!(asked.run.workspace, "worktree");
    assert_eq!(asked.engine, "claude-code");
    // Ruling 57: the plan is asked for the engine too, so it resolves the
    // agent's block for it.
    assert_eq!(asked.run.engine, "claude-code");
}

#[test]
fn a_refusal_ends_the_command_with_its_code_and_exit_and_writes_no_reply() {
    let mut service = Double::failing(RunError::Refused(RunRejection::NoPrompt));
    let ran = go(&["--one-shot"], Some(Format::Text), None, &mut service);
    assert_eq!(ran.exit, Err(("runs-no-prompt".to_string(), 2)));
    assert_eq!(ran.out, "");
    assert!(ran.refusals.is_empty(), "a single refusal lists nothing");
}

// Owner rulings 73a, 73b: an aggregate refusal ends the command with its own
// code and exit, and hands on each refusal it holds, in check order, for the
// command line to list.
#[test]
fn an_aggregate_refusal_hands_on_each_refusal_in_check_order() {
    for (plan_aborted, exit) in [(false, 2), (true, 3)] {
        let mut service = Double::failing(RunError::Refused(RunRejection::LaunchRefused {
            refusals: vec![
                RunRejection::NoPrompt,
                RunRejection::NoCredential {
                    engine: "claude-code".to_string(),
                    variables: "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
                },
            ],
            plan_aborted,
        }));
        let ran = go(&["--one-shot"], Some(Format::Json), None, &mut service);
        assert_eq!(ran.exit, Err(("runs-launch-refused".to_string(), exit)));
        assert_eq!(ran.refusals, ["runs-no-prompt", "runs-no-credential"]);
    }
}

#[test]
fn a_plan_the_library_refused_keeps_its_exit() {
    let mut service = Double::failing(RunError::Plan(Box::new(AppError::AgentExclusive {
        other: "profile".to_string(),
    })));
    let ran = go(
        &["--one-shot", "hi"],
        Some(Format::Text),
        None,
        &mut service,
    );
    assert_eq!(
        ran.exit,
        Err(("library-application-agent-exclusive".to_string(), 2))
    );
}

#[test]
fn what_the_plan_noticed_is_said_on_the_error_stream_not_the_reply() {
    let mut service = Double::answering("THE-REPLY");
    let ran = go(
        &["--one-shot", "hi"],
        Some(Format::Text),
        None,
        &mut service,
    );
    assert!(ran.err.contains("a notice"), "{}", ran.err);
    assert!(!ran.out.contains("a notice"));
}

#[test]
fn every_diagnostic_has_a_message_using_exactly_its_args() {
    for sample in super::diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("run-cli-"), "{code}");
        let line = crate::EN_US
            .1
            .lines()
            .find(|l| l.starts_with(&format!("{code} =")))
            .unwrap_or_else(|| panic!("{code} has no message"));
        assert!(line.contains("$ext_err_txt"));
    }
}

fn go_session(line: &[&str], service: &mut Double) -> (Result<u8, (String, u8)>, String, String) {
    let program = Command::new("ctxloom").subcommand(with_one_shot(library_run()));
    let mut argv = vec!["ctxloom", "run"];
    argv.extend_from_slice(line);
    let matches = program.try_get_matches_from(argv).unwrap();
    let call = Call {
        matches: &matches,
        explicit_format: None,
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: Flags::default(),
    };
    let catalog = Catalog::en_us(&[library_application::EN_US, crate::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let exit = session(
        service,
        matches.subcommand_matches("run").unwrap(),
        &call,
        &catalog,
        &mut out,
        &mut err,
    )
    .map_err(|f| (f.cause.code().to_string(), f.exit));
    (
        exit,
        String::from_utf8(out).unwrap(),
        String::from_utf8(err).unwrap(),
    )
}

fn ended(code: Option<i32>) -> Ended {
    Ended {
        harp: "amber-swift-owl".to_string(),
        session: "s".to_string(),
        engine: "claude-code".to_string(),
        code,
    }
}

#[test]
fn a_session_asks_with_the_line_prints_nothing_and_exits_as_the_engine_did() {
    let mut double = Double::failing(RunError::EmptyAnswer);
    double.ended = Some(Ok(ended(Some(3))));
    let (exit, out, err) = go_session(&["--profile", "dev", "hello", "there"], &mut double);
    assert_eq!(exit, Ok(3));
    assert_eq!(out, "", "the engine had the terminal; nothing more is said");
    assert!(err.contains("a notice"));
    let (_, asked) = &double.asked[0];
    assert_eq!(asked.run.profile, "dev");
    assert_eq!(asked.run.prompt, "hello there");
}

#[test]
fn a_session_ended_by_a_signal_exits_one() {
    let mut double = Double::failing(RunError::EmptyAnswer);
    double.ended = Some(Ok(ended(None)));
    assert_eq!(go_session(&[], &mut double).0, Ok(1));
}

#[test]
fn a_session_that_could_not_start_fails_with_the_errors_exit() {
    let mut double = Double::failing(RunError::EmptyAnswer);
    double.ended = Some(Err(RunError::NotStarted {
        detail: "no claude".to_string(),
    }));
    assert_eq!(
        go_session(&[], &mut double).0,
        Err(("run-application-not-started".to_string(), 1))
    );
}

/// A standard output that refuses every write.
struct Broken;

impl std::io::Write for Broken {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        Err(std::io::Error::other("pipe closed"))
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

// A reply that cannot be written ends the command: exit 1, the code, and the
// system's words as `ext_err_txt`.
#[test]
fn a_reply_that_cannot_be_written_is_that_failure() {
    let program = Command::new("ctxloom").subcommand(with_one_shot(library_run()));
    let matches = program
        .try_get_matches_from(["ctxloom", "run", "--one-shot", "hi"])
        .unwrap();
    let call = Call {
        matches: &matches,
        explicit_format: Some(Format::Json),
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: Flags::default(),
    };
    let catalog = Catalog::en_us(&[library_application::EN_US, crate::EN_US]).unwrap();
    let mut err = Vec::new();
    let failure = run(
        &mut Double::answering("A"),
        matches.subcommand_matches("run").unwrap(),
        &call,
        &catalog,
        &mut || None,
        &mut Broken,
        &mut err,
    )
    .unwrap_err();
    assert_eq!(failure.exit, 1);
    assert_eq!(failure.cause.code(), "run-cli-output");
    assert_eq!(
        failure.cause.to_string(),
        "run-cli-output ext_err_txt=\"pipe closed\""
    );
    let args: Vec<_> = failure.cause.args().into_iter().map(|(n, _)| n).collect();
    assert_eq!(args, ["ext_err_txt"]);
}

// The samples hold the one code this crate has.
#[test]
fn the_samples_hold_the_output_failure() {
    let samples = super::diagnostic_samples();
    assert_eq!(samples.len(), 1);
    assert_eq!(samples[0].code(), "run-cli-output");
}
