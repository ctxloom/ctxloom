//! The write verbs as the program runs them: the command line parsed by this
//! crate's nouns, the application over in-memory ports, the answer as bytes
//! and what the ports were asked to write. The texts and shapes are the ones
//! the acceptance scenarios (`tests/library_write_*`) hold the binary to.

use std::sync::Arc;

use library_application::fakes::{Mem, Setup, CONFIG, ENVELOPE};
use library_application::Library;

use super::*;

pub(crate) const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/example.md", "EXAMPLE"),
    ("prompts/review.md", "REVIEW"),
    ("mcp/tools.yaml", "command: echo\n"),
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: d\n---\nbody\n",
    ),
];

pub(crate) const PROJECT: &[(&str, &str)] = &[ENVELOPE, ("profiles/dev.yaml", "bundles: [demo]\n")];

pub(crate) fn world() -> (Library, Arc<Mem>) {
    Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
}

pub(crate) fn run_on(library: &Library, line: &[&str], how: How) -> Ran {
    let program = Command::new("ctxloom")
        .subcommand_required(true)
        .arg(clap::Arg::new("format").long("format").global(true))
        .subcommands(nouns());
    let mut argv = vec!["ctxloom"];
    argv.extend_from_slice(line);
    let matches = program
        .try_get_matches_from(argv)
        .unwrap_or_else(|e| panic!("{line:?} does not parse: {e}"));
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
        library,
        &crate::tests::Project,
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

/// A success's text: ok, or a preview's would-change (ruling 153), which its
/// own tests hold to its code.
pub(crate) fn text(library: &Library, line: &[&str]) -> String {
    let ran = run_on(library, line, TEXT);
    assert!(ran.code() < 10, "{line:?}: a success: {}", ran.err);
    ran.out
}

pub(crate) fn json(library: &Library, line: &[&str]) -> serde_json::Value {
    let ran = run_on(library, line, JSON);
    assert!(ran.code() < 10, "{line:?}: a success: {}", ran.err);
    ran.json()
}

pub(crate) fn refused_code(library: &Library, line: &[&str], how: How) -> String {
    run_on(library, line, how).refused().to_string()
}

pub(crate) const JSON_EXPLICIT: How = JSON;

// The verbs Go answers in text only refuse an explicit json before writing.
#[test]
fn test_the_text_only_verbs_refuse_json_and_write_nothing() {
    let (library, mem) = world();
    for line in [
        vec!["config", "create"],
        vec!["config", "edit"],
        vec!["agent", "default"],
    ] {
        let before = mem.writes();
        let refused = refused_code(&library, &line, JSON);
        assert!(
            refused.contains("unsupported-format"),
            "{line:?}: {refused}"
        );
        assert_eq!(mem.writes(), before, "{line:?} wrote");
    }
}
