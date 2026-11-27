//! The setup reads, answered where the caller stands (step 10c U5, U6).

use daemon_api::{SetupAnswer, SetupRead, SetupReads};

use super::SetupReaders;
use ctxloom_wiring::fixtures::World;

fn readers(world: &World) -> SetupReaders {
    let temp = world.dir().join("tmp");
    crate::fixtures::mkdirs(&temp);
    SetupReaders {
        home: world.home(),
        temp,
        stamp: "v9.9.9-abc".to_string(),
    }
}

/// A world whose working directory is a project.
fn project() -> World {
    let world = World::new("setup-reads");
    crate::fixtures::mkdirs(world.work().join(".ctxloom"));
    crate::fixtures::put_mode(
        world.work().join(".ctxloom/config.yaml"),
        "schema_version: 9\n",
        0o644,
    );
    world
}

/// The caller's directory: the world's project.
fn cwd(world: &World) -> String {
    world.work().display().to_string()
}

/// The caller's environment, as its request carries it.
fn pairs(world: &World) -> Vec<(String, String)> {
    world
        .env()
        .vars()
        .filter_map(|(name, value)| Some((name.to_str()?.to_string(), value.to_str()?.to_string())))
        .collect()
}

// doctor is diagnosed at the caller's directory, with the daemon's stamp:
// its version row says it, and its setup row finds the caller's project.
#[test]
fn doctor_is_diagnosed_at_the_callers_directory() {
    let world = project();
    let answer = readers(&world)
        .read(
            &cwd(&world),
            &pairs(&world),
            &library_application::Flags::default(),
            SetupRead::Doctor { deps: false },
        )
        .unwrap();
    let SetupAnswer::Doctor(doctor) = answer else {
        panic!("{answer:?}")
    };
    let version = doctor
        .checks
        .iter()
        .find(|c| c.marker == "DOCTOR-CHECK-VERSION-c3")
        .unwrap();
    assert!(version.detail.contains("v9.9.9-abc"), "{version:?}");
    // `--deps`: this machine's checks alone.
    let only = readers(&world)
        .read(
            &cwd(&world),
            &pairs(&world),
            &library_application::Flags::default(),
            SetupRead::Doctor { deps: true },
        )
        .unwrap();
    let SetupAnswer::Doctor(only) = only else {
        panic!("{only:?}")
    };
    assert_eq!(only.checks.len(), 1, "{:?}", only.checks);
}

// init prompt is the setup interview of the caller's catalog, as it has
// always been printed (a line after it).
#[test]
fn init_prompt_is_the_callers_interview() {
    let world = project();
    let answer = readers(&world)
        .read(
            &cwd(&world),
            &pairs(&world),
            &library_application::Flags::default(),
            SetupRead::InitPrompt,
        )
        .unwrap();
    let SetupAnswer::InitPrompt(prompt) = answer else {
        panic!("{answer:?}")
    };
    assert!(
        prompt.text.ends_with('\n') && prompt.text.len() > 100,
        "{prompt:?}"
    );
}

// auth.feature: auth status reads the caller's environment (moved from the
// in-process command's test, step 10c U5): no token, none present; one
// exported, found by its NAME, its value in no field of the answer.
#[test]
fn auth_status_reads_the_callers_environment_and_never_its_values() {
    let world = project();
    let auth = |env: &[(String, String)]| match readers(&world)
        .read(
            &cwd(&world),
            env,
            &library_application::Flags::default(),
            SetupRead::AuthStatus,
        )
        .unwrap()
    {
        SetupAnswer::AuthStatus(auth) => auth,
        other => panic!("{other:?}"),
    };
    let none = auth(&pairs(&world));
    let claude = none
        .rows
        .iter()
        .find(|r| r.engine == "claude-code")
        .unwrap();
    assert_eq!(claude.present, Some(false), "{claude:?}");
    let mut env = pairs(&world);
    env.push((
        "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
        "sk-ant-oat01-fixture-not-real".to_string(), // gitleaks:allow
    ));
    let found = auth(&env);
    let claude = found
        .rows
        .iter()
        .find(|r| r.engine == "claude-code")
        .unwrap();
    assert_eq!(claude.present, Some(true), "{claude:?}");
    assert_eq!(claude.via, "CLAUDE_CODE_OAUTH_TOKEN", "{claude:?}");
    assert!(!format!("{found:?}").contains("fixture-not-real"));
}

// doctor.feature: the credentials row is read from the caller's
// environment (moved from the in-process command's test): a warn without
// the token (the primary signs in), ok with it.
#[test]
fn doctors_credentials_row_reads_the_callers_environment() {
    let world = project();
    let row = |env: &[(String, String)]| match readers(&world)
        .read(
            &cwd(&world),
            env,
            &library_application::Flags::default(),
            SetupRead::Doctor { deps: false },
        )
        .unwrap()
    {
        SetupAnswer::Doctor(doctor) => doctor
            .checks
            .into_iter()
            .find(|c| c.marker == "DOCTOR-CHECK-CREDENTIALS-m5")
            .unwrap(),
        other => panic!("{other:?}"),
    };
    // The built-in source: the primary signs in, only a child is refused
    // (owner ruling 2026-11-19).
    assert_eq!(row(&pairs(&world)).status, "warn");
    let mut env = pairs(&world);
    env.push(("CLAUDE_CODE_OAUTH_TOKEN".to_string(), "x".to_string()));
    assert_eq!(row(&env).status, "ok");
}

// manage check reads the caller's project: its work directory is there.
#[test]
fn manage_check_reads_the_callers_project() {
    let world = project();
    let answer = readers(&world)
        .read(
            &cwd(&world),
            &pairs(&world),
            &library_application::Flags::default(),
            SetupRead::CheckManage,
        )
        .unwrap();
    let SetupAnswer::CheckManage(check) = answer else {
        panic!("{answer:?}")
    };
    assert!(check.work_dir.ends_with("work"), "{check:?}");
    assert!(!check.backends.is_empty());
}
