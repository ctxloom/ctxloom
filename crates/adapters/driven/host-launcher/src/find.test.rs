//! The search, on files a test made in a filesystem of its own. Nothing is
//! run, so the Windows rules are held on every host. The host's filesystem
//! may be case-sensitive (Windows' is not), so a file is named as the
//! candidate is spelled: `claude.EXE` for cmd.exe's default PATHEXT.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use file_system::{Fs, Perms, Rooted};
use os::{Flavor, Mode};
use run_ports::{ProgramNotFound, TurnCommand};

use super::find;

/// A filesystem of the test's own, with the directories `dirs` in it.
fn machine(dirs: &[&str]) -> (Rooted, Vec<PathBuf>) {
    let fs = Rooted::temp().unwrap();
    let dirs = dirs
        .iter()
        .map(|dir| {
            let dir = os::fixture(dir);
            fs.create_dir_all(&dir, Mode::DIR).unwrap();
            dir
        })
        .collect();
    (fs, dirs)
}

/// An empty file at `dir/name`, of the mode a plain file is made with: no
/// execute bit, which Windows does not ask for.
fn file(fs: &Rooted, dir: &Path, name: &str) -> String {
    let path = dir.join(name);
    fs.write_atomic(&path, b"", Perms::Exact(Mode::FILE))
        .unwrap();
    path.display().to_string()
}

/// `program`'s command with `env`.
fn command(program: &str, env: &[(&str, String)]) -> TurnCommand {
    let mut command = TurnCommand::new(program);
    command.env = env
        .iter()
        .map(|(key, value)| ((*key).to_string(), value.clone()))
        .collect::<BTreeMap<_, _>>();
    command
}

/// A Windows PATH of `dirs`.
fn windows_path(dirs: &[PathBuf]) -> String {
    dirs.iter()
        .map(|dir| dir.display().to_string())
        .collect::<Vec<_>>()
        .join(";")
}

// The owner's report (2026-11-12): claude installed by npm is `claude.cmd`,
// which std's spawn, trying `claude.exe` alone, called "program not found".
#[test]
fn an_npm_shim_is_found_through_pathext_on_windows() {
    let (fs, dirs) = machine(&["/npm", "/bin"]);
    let shim = file(&fs, &dirs[0], "claude.CMD");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Ok(shim)
    );
}

#[test]
fn an_executable_is_preferred_to_a_batch_file_earlier_on_the_path() {
    let (fs, dirs) = machine(&["/npm", "/native"]);
    file(&fs, &dirs[0], "claude.CMD");
    let native = file(&fs, &dirs[1], "claude.EXE");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Ok(native)
    );
}

#[test]
fn among_executables_the_path_decides_then_pathext() {
    let (fs, dirs) = machine(&["/first", "/second"]);
    let first = file(&fs, &dirs[0], "claude.EXE");
    file(&fs, &dirs[1], "claude.COM");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Ok(first)
    );
    let (fs, dirs) = machine(&["/one"]);
    let com = file(&fs, &dirs[0], "claude.COM");
    file(&fs, &dirs[0], "claude.EXE");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Ok(com),
        "cmd.exe's default PATHEXT tries .COM before .EXE"
    );
}

#[test]
fn the_commands_own_pathext_is_the_one_tried() {
    let (fs, dirs) = machine(&["/bin"]);
    file(&fs, &dirs[0], "claude.EXE");
    let shim = file(&fs, &dirs[0], "claude.cmd");
    let env = [
        ("PATH", windows_path(&dirs)),
        ("PATHEXT", ".cmd".to_string()),
    ];
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &env)),
        Ok(shim)
    );
}

#[test]
fn a_windows_path_is_read_whatever_its_names_spelling() {
    let (fs, dirs) = machine(&["/bin"]);
    let exe = file(&fs, &dirs[0], "claude.exe");
    let env = [
        ("Path", windows_path(&dirs)),
        ("PathExt", ".exe".to_string()),
    ];
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &env)),
        Ok(exe)
    );
}

#[test]
fn a_program_on_no_directory_names_what_was_looked_for_and_where() {
    let (fs, dirs) = machine(&["/a", "/b"]);
    file(&fs, &dirs[0], "other.EXE");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Err(ProgramNotFound {
            program: "claude".to_string(),
            names: ["claude.COM", "claude.EXE", "claude.BAT", "claude.CMD"]
                .map(String::from)
                .to_vec(),
            dirs: dirs.iter().map(|d| d.display().to_string()).collect(),
        })
    );
}

#[test]
fn a_command_with_no_path_searches_no_directory() {
    let (fs, _) = machine(&[]);
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[])),
        Err(ProgramNotFound {
            program: "claude".to_string(),
            names: ["claude.COM", "claude.EXE", "claude.BAT", "claude.CMD"]
                .map(String::from)
                .to_vec(),
            dirs: Vec::new(),
        })
    );
}

#[test]
fn a_directory_on_the_path_is_not_a_program() {
    let (fs, dirs) = machine(&["/a", "/b"]);
    fs.create_dir_all(&dirs[0].join("claude.EXE"), Mode::DIR)
        .unwrap();
    let exe = file(&fs, &dirs[1], "claude.EXE");
    let path = ("PATH", windows_path(&dirs));
    assert_eq!(
        find(&fs, Flavor::Windows, &command("claude", &[path])),
        Ok(exe)
    );
}

#[test]
fn a_program_named_with_its_directory_is_that_file_with_pathext_tried() {
    let (fs, dirs) = machine(&["/opt"]);
    let exe = file(&fs, &dirs[0], "claude.EXE");
    let named = dirs[0].join("claude").display().to_string();
    assert_eq!(find(&fs, Flavor::Windows, &command(&named, &[])), Ok(exe));
    let missing = dirs[0].join("absent").display().to_string();
    assert_eq!(
        find(&fs, Flavor::Windows, &command(&missing, &[])),
        Ok(missing.clone()),
        "one that is not there is spawned as named, for the spawn to say so"
    );
}

// Unix: the name itself, on the PATH's directories, a file with an execute
// bit. A file without one is passed over, as the shell passes it over.
#[test]
fn on_unix_the_first_executable_file_of_the_name_is_found() {
    if !crate::tests::runs_scripts() {
        return;
    }
    let (fs, dirs) = machine(&["/plain", "/bin"]);
    file(&fs, &dirs[0], "claude");
    fixture_program::install_program(&fs, &dirs[1].join("claude"), "exit 0\n").unwrap();
    // The caller's path, as the search names it (the filesystem's own).
    let program = dirs[1].join("claude").display().to_string();
    let path = (
        "PATH",
        dirs.iter()
            .map(|dir| dir.display().to_string())
            .collect::<Vec<_>>()
            .join(":"),
    );
    assert_eq!(
        find(
            &fs,
            Flavor::Unix,
            &command("claude", std::slice::from_ref(&path))
        ),
        Ok(program.clone())
    );
    assert_eq!(
        find(&fs, Flavor::Unix, &command(&program, &[path])),
        Ok(program),
        "a program named with its directory is that file"
    );
}

// The launcher searches through the filesystem it was given, by its OS's
// rules; given none, it spawns the program as named.
#[test]
fn the_launcher_finds_through_its_filesystem_and_without_one_searches_nothing() {
    use run_ports::RunnerLauncher;
    use std::sync::Arc;

    let (fs, dirs) = machine(&["/bin"]);
    let flavor = os::host().flavor();
    let name = flavor.program_file("claude");
    let found = file(&fs, &dirs[0], &name);
    let search = match flavor {
        Flavor::Unix => format!("{}", dirs[0].display()),
        Flavor::Windows => windows_path(&dirs),
    };
    let asked = command("claude", &[("PATH", search)]);
    let fs: Arc<dyn Fs> = Arc::new(fs);
    let launcher = crate::HostLauncher::default().searching(Arc::clone(&fs));
    match flavor {
        // A plain file has no execute bit: on unix it is no program.
        Flavor::Unix => assert!(launcher.find_program(&asked).is_err()),
        Flavor::Windows => assert_eq!(launcher.find_program(&asked), Ok(found)),
    }
    assert_eq!(
        crate::HostLauncher::default().find_program(&asked),
        Ok("claude".to_string())
    );
}
