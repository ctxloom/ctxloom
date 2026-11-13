use super::*;

fn command(program: &str, args: &[&str]) -> TurnCommand {
    let mut command = TurnCommand::new(program);
    command.args = args.iter().map(ToString::to_string).collect();
    command
}

#[test]
fn a_batch_file_is_one_whose_name_ends_in_bat_or_cmd_in_any_case() {
    for batch in ["claude.cmd", r"C:\npm\claude.CMD", "x.bat", "x.Bat"] {
        assert!(is_batch_file(batch), "{batch}");
    }
    for not in ["claude", "claude.exe", "claude.cmdx", "cmd", "x.bat.exe"] {
        assert!(!is_batch_file(not), "{not}");
    }
}

#[test]
fn a_batch_files_argument_with_a_line_feed_or_carriage_return_is_refused_on_windows() {
    for arg in ["one\ntwo", "one\rtwo", "\r\n", "nul\0"] {
        let refused =
            check_batch_arguments("windows", &command(r"C:\npm\claude.cmd", &["-p", arg]));
        assert_eq!(
            refused,
            Err(BatchArguments {
                program: r"C:\npm\claude.cmd".to_string()
            }),
            "{arg:?}"
        );
    }
}

#[test]
fn a_batch_files_one_line_arguments_pass() {
    let one_line = command(r"C:\npm\claude.bat", &["--settings", "a b", "100% \"q\""]);
    assert_eq!(check_batch_arguments("windows", &one_line), Ok(()));
}

#[test]
fn an_executables_arguments_and_any_other_os_are_not_judged() {
    let exe = command(r"C:\bin\claude.exe", &["one\ntwo"]);
    assert_eq!(check_batch_arguments("windows", &exe), Ok(()));
    let cmd = command("/bin/claude.cmd", &["one\ntwo"]);
    assert_eq!(check_batch_arguments("linux", &cmd), Ok(()));
    assert_eq!(check_batch_arguments("macos", &cmd), Ok(()));
}

#[test]
fn a_program_not_found_names_what_was_looked_for_and_where() {
    let missing = ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude.exe".to_string(), "claude.cmd".to_string()],
        dirs: vec![r"C:\a".to_string(), r"C:\b".to_string()],
    };
    assert_eq!(missing.code(), "run-ports-program-not-found");
    let args: Vec<&str> = missing.args().iter().map(|(name, _)| *name).collect();
    assert_eq!(args, ["program", "names", "dirs"]);
    assert_eq!(
        format!("{:?}", missing.args()),
        format!(
            "{:?}",
            vec![
                ("program", Arg::from("claude")),
                ("names", Arg::from("claude.exe, claude.cmd")),
                ("dirs", Arg::from(r"C:\a; C:\b")),
            ]
        )
    );
}

#[test]
fn a_program_not_found_on_an_empty_path_says_the_path_is_empty() {
    let missing = ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude".to_string()],
        dirs: Vec::new(),
    };
    assert_eq!(missing.code(), "run-ports-program-not-found-no-path");
    let args: Vec<&str> = missing.args().iter().map(|(name, _)| *name).collect();
    assert_eq!(args, ["program", "names"]);
}

#[test]
fn a_batch_arguments_refusal_names_the_batch_file() {
    let refused = BatchArguments {
        program: "claude.cmd".to_string(),
    };
    assert_eq!(refused.code(), "run-ports-batch-arguments");
    assert_eq!(
        format!("{:?}", refused.args()),
        format!("{:?}", vec![("program", Arg::from("claude.cmd"))])
    );
}

// A diagnostic displays as its code and args (the catalog renders the words).
#[test]
fn each_displays_as_its_code_and_args() {
    let missing = ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude".to_string()],
        dirs: Vec::new(),
    };
    assert_eq!(
        missing.to_string(),
        "run-ports-program-not-found-no-path program=claude names=claude"
    );
    let refused = BatchArguments {
        program: "claude.cmd".to_string(),
    };
    assert_eq!(
        refused.to_string(),
        "run-ports-batch-arguments program=claude.cmd"
    );
}
