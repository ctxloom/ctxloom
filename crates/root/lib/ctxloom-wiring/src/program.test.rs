use std::path::Path;

use super::Program;

#[test]
fn test_the_program_is_the_executables_directory_and_file_name() {
    assert_eq!(
        Program::at(Some(Path::new("/home/a/.local/lib/ctxloom-rs/ctxloom-rs"))),
        Program {
            dir: "/home/a/.local/lib/ctxloom-rs".to_string(),
            name: "ctxloom-rs".to_string(),
        }
    );
}

#[test]
fn test_an_unknown_executable_is_the_authored_name_and_no_directory() {
    assert_eq!(
        Program::at(None),
        Program {
            dir: String::new(),
            name: "ctxloom".to_string(),
        }
    );
    assert_eq!(Program::at(Some(Path::new("/"))).name, "ctxloom");
}

#[test]
fn test_the_running_program_is_this_test_executable() {
    let exe = std::env::current_exe().unwrap();
    let running = super::running();
    assert_eq!(
        running.name,
        exe.file_name().unwrap().to_string_lossy(),
        "{running:?}"
    );
}
