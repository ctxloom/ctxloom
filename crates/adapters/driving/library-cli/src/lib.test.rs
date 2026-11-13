//! The library commands as the program runs them: the command line parsed by
//! this crate's own nouns, the application over in-memory ports, the answer
//! as bytes. Each test names the Go command (`internal/adapters/cli`) or the
//! Gherkin feature it holds the adapter to.

use angzarr_presentation::{Catalog, Format};
use clap::Command;
use library_application::fakes::{Setup, ENVELOPE};

use super::*;

pub(crate) const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "fragments/golang.md",
        "---\ndescription: when editing Go code\ntags: [go]\n---\nGO-RULES\n",
    ),
    ("fragments/testing.md", "# testing\n\nTEST-BODY\n"),
    ("fragments/ctl.md", "a\u{7}b\n"),
    (
        "fragments/dist.md",
        "---\ntags: [distilled]\n---\nlong form\n",
    ),
    ("fragments/dist.distilled.md", "short form\n"),
    (
        "prompts/review.md",
        "---\ndescription: Review code\n---\nREVIEW-BODY\n",
    ),
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: Reviews code\n---\nSKILL-BODY\n",
    ),
    (
        "mcp/tree-sitter.yaml",
        "command: tree-sitter\nargs: [--stdio]\n",
    ),
];

pub(crate) const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    ("profiles/broken.yaml", "bundles: [does-not-exist]\n"),
    (
        "profiles/dev.yaml",
        "description: dev profile\nbundles: [demo]\ntags: [team]\n",
    ),
];

pub(crate) const AGENTS: &str = "schema_version: 9\ncompanions:\n  acme: {}\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\n    runtime: host\ndefault_agent: dev\n";

/// What one invocation left behind.
pub(crate) struct Ran {
    pub(crate) exit: Result<u8, String>,
    pub(crate) out: String,
    pub(crate) err: String,
}

impl Ran {
    pub(crate) fn code(&self) -> u8 {
        match &self.exit {
            Ok(code) => *code,
            Err(failure) => panic!("failed: {failure}"),
        }
    }

    pub(crate) fn refused(&self) -> &str {
        match &self.exit {
            Ok(code) => panic!("exit {code}, not a failure: {}", self.out),
            Err(failure) => failure,
        }
    }

    pub(crate) fn json(&self) -> serde_json::Value {
        serde_json::from_str(&self.out).unwrap_or_else(|e| panic!("{e}: {}", self.out))
    }
}

/// How an invocation is made.
#[derive(Clone, Copy)]
pub(crate) struct How {
    pub(crate) explicit: Option<Format>,
    pub(crate) terminal: bool,
    pub(crate) degraded: bool,
}

pub(crate) const JSON: How = How {
    explicit: Some(Format::Json),
    terminal: false,
    degraded: false,
};

pub(crate) const TEXT: How = How {
    explicit: Some(Format::Text),
    terminal: false,
    degraded: false,
};

/// The names of a project the daemon calls `calm-otter` (OD-40).
pub(crate) struct Project;

impl Names for Project {
    fn parent(&self) -> Result<String, Failure> {
        Ok("projects/calm-otter".to_string())
    }
}

/// Outside a project (OQ-3).
pub(crate) struct Home;

impl Names for Home {
    fn parent(&self) -> Result<String, Failure> {
        Ok("users/me".to_string())
    }
}

/// No daemon to name the project: the call fails, as one unreached does.
pub(crate) struct Unreachable;

#[derive(Debug)]
pub(crate) struct NoDaemon;

impl std::fmt::Display for NoDaemon {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("no daemon")
    }
}

impl Diagnostic for NoDaemon {
    fn code(&self) -> &str {
        "daemon-api-unreached"
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        Vec::new()
    }
}

impl Names for Unreachable {
    fn parent(&self) -> Result<String, Failure> {
        Err(Failure::new(NoDaemon, Exit::FAILED))
    }
}

pub(crate) fn go(line: &[&str], how: How, layers: &[&str]) -> Ran {
    go_in(line, how, layers, &Project)
}

pub(crate) fn go_named(line: &[&str], how: How, names: &dyn Names) -> Ran {
    go_in(line, how, &[ENGINE_HOME, ENGINE_PROJECT], names)
}

pub(crate) fn go_in(line: &[&str], how: How, layers: &[&str], names: &dyn Names) -> Ran {
    let setup = Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers,
        ..Setup::default()
    };
    let program = Command::new("ctxloom")
        .subcommand_required(true)
        .subcommands(nouns());
    let mut argv = vec!["ctxloom"];
    argv.extend_from_slice(line);
    let matches = program.try_get_matches_from(argv).unwrap_or_else(|e| {
        panic!("{line:?} does not parse: {e}");
    });
    let noun = matches.subcommand_name().unwrap().to_string();
    let call = Call {
        matches: &matches,
        explicit_format: how.explicit,
        default_format: Format::Json,
        stdout_is_terminal: how.terminal,
        globals: library_application::Flags {
            degraded: how.degraded,
            no_companions: false,
            config_set: Vec::new(),
        },
    };
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let exit = run(
        &setup.library(),
        names,
        &noun,
        &call,
        &catalog,
        &mut out,
        &mut err,
    )
    .map(|exit| exit.code(true))
    .map_err(|f| format!("{}:{}", f.cause.code(), f.exit.code(true)));
    Ran {
        exit,
        out: String::from_utf8(out).unwrap(),
        err: String::from_utf8(err).unwrap(),
    }
}

// The engine noun's layers (`go_named`'s, and noun/engine.test.rs's).
pub(crate) const ENGINE_HOME: &str = "schema_version: 9\nruntime: container-rootless\nengines:\n  claude-code:\n    command: /opt/claude\n    env: [HTTPS_PROXY]\n    auth: none\n  opencode:\n    session_home: host\n";

pub(crate) const ENGINE_PROJECT: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n    runtime: host\n    profiles: [dev]\n  strict:\n    engine: claude-code\n    runtime: container-rootful\n    profiles: [dev]\n  same:\n    engine: claude-code\n    runtime: container-rootless\n  plain:\n    engine: opencode\n";

pub(crate) const BARE: &[&str] = &[AGENTS];

/// A success's json: ok, or a dry run's would-launch (ruling 153), which its
/// own test holds to its code.
pub(crate) fn json_of(line: &[&str]) -> serde_json::Value {
    let ran = go(line, JSON, BARE);
    assert!(ran.code() < 10, "{line:?}: a success");
    ran.json()
}

pub(crate) fn text_of(line: &[&str]) -> String {
    let ran = go(line, TEXT, BARE);
    assert!(ran.code() < 10, "{line:?}: a success");
    ran.out
}

// clidiag.Warn: a notice is one json line under json, words and a fix line
// under text.
#[test]
fn test_notices_are_said_as_a_json_line_or_as_words_with_their_fix() {
    let ghost = ["schema_version: 9\nagents:\n  ghost: {}\n  real:\n    engine: claude-code\n"];
    let json = go(&["agent", "list"], JSON, &ghost);
    assert_eq!(json.code(), 0);
    let line: serde_json::Value = serde_json::from_str(json.err.trim_end()).unwrap();
    assert_eq!(line["prog"], "ctxloom");
    // The line names the notice's own code (ctxloom-lib d7e783a).
    assert!(
        line["code"].as_str().is_some_and(|code| !code.is_empty()),
        "{line}"
    );
    assert!(line["warning"]
        .as_str()
        .unwrap()
        .starts_with("agents.ghost in /p/.ctxloom/layer-0.yaml declares neither"));
    assert!(line.get("remedy").is_none());
    let text = go(&["agent", "list"], TEXT, &ghost);
    assert!(text.err.starts_with("ctxloom: warning: agents.ghost in "));
    assert!(!text.err.contains("fix:"));
    let broken = go(
        &["run", "--dry-run", "-p", "broken"],
        How {
            degraded: true,
            ..TEXT
        },
        BARE,
    );
    assert!(broken
        .err
        .starts_with("ctxloom: warning: failed to load bundle \"does-not-exist\""));
    assert!(broken
        .err
        .contains("\n  fix: run `ctxloom deps pull` or fix the bundle ref, or pass --degraded\n"));
    let by_json = go(
        &["run", "--dry-run", "-p", "broken"],
        How {
            degraded: true,
            ..JSON
        },
        BARE,
    );
    assert!(by_json.err.contains("\"remedy\":\"run `ctxloom deps pull`"));
}

// inert_render.go: a body altered for the terminal is said so on the error
// stream, in text; the json form is the raw bytes.
#[test]
fn test_a_body_rendered_inert_is_noted_and_json_is_raw() {
    let text = go(&["bundle", "view", "demo#fragments/ctl"], TEXT, BARE);
    assert_eq!(text.out, "a\u{27e8}U+0007\u{27e9}b\n");
    assert_eq!(
        text.err,
        "ctxloom: warning: demo#fragments/ctl is publisher-authored and was rendered inert for this terminal (1 control character(s) escaped); use --format json for the raw bytes\n"
    );
    let json = go(&["bundle", "view", "demo#fragments/ctl"], JSON, BARE);
    assert_eq!(json.json()["body"], "a\u{7}b\n");
    assert!(json.err.is_empty());
}

// The format: asked for, a terminal's text, the configuration's
// `non_tty_format`, else the program's default.
#[test]
fn test_the_format_is_asked_for_a_terminals_or_the_configurations() {
    let line = ["companion", "list"];
    let asked_text = go(&line, TEXT, BARE);
    assert!(asked_text.out.starts_with("acme"));
    let default = How {
        explicit: None,
        terminal: false,
        degraded: false,
    };
    // OD-40: json is the companions' `ListCompanionsResponse`.
    assert!(go(&line, default, BARE).out.starts_with('{'));
    let terminal = How {
        terminal: true,
        ..default
    };
    assert!(go(&line, terminal, BARE).out.starts_with("acme"));
    let text_config = ["schema_version: 9\ncompanions:\n  acme: {}\nnon_tty_format: text\n"];
    assert!(go(&line, default, &text_config).out.starts_with("acme"));
    let json_config = ["schema_version: 9\ncompanions:\n  acme: {}\nnon_tty_format: json\n"];
    assert!(go(&line, default, &json_config).out.starts_with('{'));
    assert!(go(&line, JSON, &text_config).out.starts_with('{'));
}

// A noun the matches do not hold is not run.
#[test]
fn test_running_a_noun_the_line_does_not_name_is_refused() {
    let program = Command::new("ctxloom").subcommands(nouns());
    let matches = program.get_matches_from(["ctxloom", "companion", "list"]);
    let call = Call {
        matches: &matches,
        explicit_format: None,
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: library_application::Flags {
            degraded: false,
            no_companions: false,
            config_set: Vec::new(),
        },
    };
    let library = Setup::default().library();
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let wrong = run(
        &library, &Project, "bundle", &call, &catalog, &mut out, &mut err,
    )
    .unwrap_err();
    assert_eq!(wrong.cause.code(), "library-application-search-nothing");
    assert!(out.is_empty());
}

/// A stream that cannot be written.
pub(crate) struct Broken;

impl Write for Broken {
    fn write(&mut self, _: &[u8]) -> io::Result<usize> {
        Err(io::Error::other("pipe closed"))
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// An answer that cannot be written is a failure with its own code and exit 10.
#[test]
fn test_an_answer_that_cannot_be_written_fails_with_its_cause() {
    let program = Command::new("ctxloom").subcommands(nouns());
    let matches = program.get_matches_from(["ctxloom", "companion", "list"]);
    let call = Call {
        matches: &matches,
        explicit_format: Some(Format::Json),
        default_format: Format::Json,
        stdout_is_terminal: false,
        globals: library_application::Flags {
            degraded: false,
            no_companions: false,
            config_set: Vec::new(),
        },
    };
    let library = Setup::default().library();
    let catalog = Catalog::en_us(&[library_application::EN_US]).unwrap();
    let failure = run(
        &library,
        &Project,
        "companion",
        &call,
        &catalog,
        &mut Broken,
        &mut Vec::new(),
    )
    .unwrap_err();
    assert_eq!(failure.exit, Exit::FAILED);
    assert_eq!(failure.cause.code(), "ctxloom-cli-output");
    let args = failure.cause.args();
    assert_eq!(args.len(), 1);
    assert_eq!(args[0].0, "ext_err_txt");
    assert_eq!(
        format!("{:?}", args[0].1),
        format!("{:?}", angzarr_diagnostic::Arg::from("pipe closed"))
    );
    assert!(failure.cause.to_string().contains("ctxloom-cli-output"));
}

#[path = "write.test.rs"]
pub(crate) mod write_verbs;
