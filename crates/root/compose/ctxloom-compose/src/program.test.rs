use std::path::Path;

use super::Program;

#[test]
fn test_the_program_is_the_executables_directory_and_file_name() {
    assert_eq!(
        Program::at(
            Some(Path::new("/home/a/.local/lib/ctxloom-rs/ctxloom-rs")),
            ""
        ),
        Program {
            dir: "/home/a/.local/lib/ctxloom-rs".to_string(),
            name: "ctxloom-rs".to_string(),
        }
    );
}

#[test]
fn test_an_unknown_executable_is_the_authored_name_and_no_directory() {
    assert_eq!(
        Program::at(None, ""),
        Program {
            dir: String::new(),
            name: "ctxloom".to_string(),
        }
    );
    assert_eq!(Program::at(Some(Path::new("/")), "").name, "ctxloom");
}

#[test]
fn test_the_running_program_is_this_test_executable() {
    let exe = std::env::current_exe().unwrap();
    let running = super::running();
    let file = exe.file_name().unwrap().to_string_lossy().into_owned();
    let bare = file
        .strip_suffix(std::env::consts::EXE_SUFFIX)
        .unwrap_or(&file);
    assert_eq!(running.name, bare, "{running:?}");
}

// Owner ruling 2026-11-12 (163 and 164 amended): the platform's executable
// suffix is no part of the name, so every callback is `ctxloom hook ...` on
// every OS.
#[test]
fn test_the_platforms_executable_suffix_is_no_part_of_the_name() {
    let windows = Program::at(Some(Path::new("C:/Users/a/bin/ctxloom.exe")), ".exe");
    assert_eq!(windows.name, "ctxloom");
    assert_eq!(
        Program::at(Some(Path::new("C:/bin/ctxloom.EXE")), ".exe").name,
        "ctxloom"
    );
    // Only the suffix the platform gives, and only at the end.
    assert_eq!(
        Program::at(Some(Path::new("/bin/ctxloom.exe")), "").name,
        "ctxloom.exe"
    );
    assert_eq!(
        Program::at(Some(Path::new("/bin/ctxloom.exe.rs")), ".exe").name,
        "ctxloom.exe.rs"
    );
    // A name that is the suffix alone is no name: the authored one.
    assert_eq!(
        Program::at(Some(Path::new("/bin/.exe")), ".exe").name,
        "ctxloom"
    );
}
