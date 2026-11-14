//! The configuration's typed reading and the steps of loading it.

use angzarr_yaml::{parse, Value};
use ctxloom_ports::Source;

use super::{reduced_when_unreadable, stamp, Config};

fn config(text: &str) -> Config {
    let value = parse(text).expect("a document");
    Config {
        raw: value.clone(),
        value,
        source: Source::Project,
    }
}

// config.content_form (owner ruling 59): distilled unless it says original.
#[test]
fn test_distilled_forms_are_preferred_unless_the_content_form_is_original() {
    assert!(config("editor:\n  command: vim\n").prefers_distilled());
    assert!(config("config:\n  content_form: distilled\n").prefers_distilled());
    assert!(!config("config:\n  content_form: original\n").prefers_distilled());
}

// config.statusline (ruling 59): ctxloom's unless it says none.
#[test]
fn test_ctxloom_manages_the_statusline_unless_it_says_none() {
    assert!(config("editor:\n  command: vim\n").statusline());
    assert!(config("config:\n  statusline: ctxloom\n").statusline());
    assert!(!config("config:\n  statusline: none\n").statusline());
}

// sync.on_run (rulings 59, 60): a run installs what is missing unless it says
// none.
#[test]
fn test_a_run_installs_missing_dependencies_unless_on_run_is_none() {
    use super::OnRun;
    assert_eq!(
        config("editor:\n  command: vim\n").sync_on_run(),
        OnRun::Missing
    );
    assert_eq!(
        config("sync:\n  on_run: missing\n").sync_on_run(),
        OnRun::Missing
    );
    assert_eq!(config("sync:\n  on_run: none\n").sync_on_run(), OnRun::None);
}

// config.premised (owner ruling of 2026-11-12): a premised fragment waits
// for its premise unless the config says it loads always.
#[test]
fn test_a_premised_fragment_loads_on_demand_unless_premised_says_always() {
    use library::compose::Premised;
    assert_eq!(
        config("editor:\n  command: vim\n").premised(),
        Premised::OnDemand
    );
    assert_eq!(
        config("config:\n  premised: on-demand\n").premised(),
        Premised::OnDemand
    );
    assert_eq!(
        config("config:\n  premised: always\n").premised(),
        Premised::Always
    );
}

// legacy.go (owner ruling 162): Go ctxloom is present unless the config says
// it is retired.
#[test]
fn test_go_ctxloom_is_present_unless_legacy_go_says_retired() {
    use super::LegacyGo;
    assert_eq!(
        config("editor:\n  command: vim\n").legacy_go(),
        LegacyGo::Present
    );
    assert_eq!(
        config("legacy:\n  go: present\n").legacy_go(),
        LegacyGo::Present
    );
    assert_eq!(
        config("legacy:\n  go: retired\n").legacy_go(),
        LegacyGo::Retired
    );
}

// output.dir (rulings 89, 106): `ctxloom/` unless it says otherwise, as it
// was written (the project root it is relative to is the caller's).
#[test]
fn test_the_output_directory_is_ctxloom_unless_the_config_names_one() {
    assert_eq!(config("editor:\n  command: vim\n").output_dir(), "ctxloom/");
    assert_eq!(
        config("output:\n  dir: out/sessions\n").output_dir(),
        "out/sessions"
    );
    assert_eq!(
        config("output:\n  dir: /var/out\n").output_dir(),
        "/var/out"
    );
}

// A config that fails to decode (a value of the wrong shape, in a layer or
// after the overrides) is the defaults alone. Go parity, not a defect: Go's
// Config.UnmarshalYAML (internal/core/config/config.go:449) returns the
// decode error before fromDoc, so nothing of the document is applied, and
// Builder.Decode (reading.go:64) only warns; an override's own key vanishes
// with the rest.
#[test]
fn test_an_unreadable_shape_reduces_the_config_to_nothing() {
    let sound = parse("editor:\n  command: vim\n").unwrap();
    let unsound = parse("companions: acme\n").unwrap();
    assert_eq!(
        reduced_when_unreadable(sound.clone(), false),
        sound,
        "a sound config is kept"
    );
    assert_eq!(
        reduced_when_unreadable(sound, true),
        Value::Map(Vec::new()),
        "a layer's shape failure reduces it"
    );
    assert_eq!(
        reduced_when_unreadable(unsound, false),
        Value::Map(Vec::new()),
        "a merged shape failure reduces it"
    );
}

// The document says the generation this build writes, whatever it was.
#[test]
fn test_the_loaded_document_is_stamped_with_the_current_generation() {
    let mut map = parse("editor:\n  command: vim\n").unwrap();
    stamp(&mut map);
    assert_eq!(map.get("schema_version").and_then(Value::as_str), Some("9"));
    let mut nothing = Value::Null;
    stamp(&mut nothing);
    assert_eq!(
        nothing.get("schema_version").and_then(Value::as_str),
        Some("9")
    );
    assert!(matches!(nothing, Value::Map(_)));
}

// ---------------------------------------------------------------- characterization
//
// Pinned before the config intake may move (A0): what loading says of the
// overrides it was given, in which words, and which of them stop it.

mod loading {
    use super::super::{load, Flags};
    use crate::error::Notice;
    use crate::fakes::Setup;
    use ctxloom_ports::ConfigFiles;

    const STAMPED: &str = "schema_version: 9\neditor:\n  command: vim\n";

    fn loaded(
        layers: &[&str],
        environ: &[(&str, &str)],
        flags: &Flags,
    ) -> (angzarr_yaml::Value, Vec<String>) {
        let (_, mem) = Setup {
            layers,
            ..Setup::default()
        }
        .fixture();
        let environ: Vec<(String, String)> = environ
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        let mut notices = Vec::new();
        let files: &dyn ConfigFiles = mem.as_ref();
        let config = load(files, &environ, flags, &mut notices).expect("loaded");
        let said = notices
            .into_iter()
            .map(|n| match n {
                Notice::Finding { text, remedy } => format!("{text} | {remedy}"),
                other => panic!("unexpected {other:?}"),
            })
            .collect();
        (config.raw, said)
    }

    fn flags_with(sets: &[&str]) -> Flags {
        Flags {
            config_set: sets.iter().map(ToString::to_string).collect(),
            ..Flags::default()
        }
    }

    #[test]
    fn test_a_blank_config_set_is_said_and_the_rest_still_apply() {
        let (value, said) = loaded(
            &[STAMPED],
            &[],
            &flags_with(&["=blank", "editor.command=nano", "bare"]),
        );
        assert_eq!(
            said,
            [
                "config override resolution: --config-set \"=blank\": empty path before \"=\" | ",
                "config override resolution: --config-set \"bare\": expected <path>=<value> | ",
            ]
        );
        assert_eq!(
            value
                .get("editor")
                .and_then(|e| e.get("command"))
                .and_then(angzarr_yaml::Value::as_str),
            Some("nano")
        );
    }

    /// What loading `flags` over `layers` refused with, as its code and
    /// violations.
    fn refused_by(layers: &[&str], flags: &Flags) -> (String, String) {
        use angzarr_diagnostic::Diagnostic as _;
        let (_, mem) = Setup {
            layers,
            ..Setup::default()
        }
        .fixture();
        let files: &dyn ConfigFiles = mem.as_ref();
        let error = load(files, &[], flags, &mut Vec::new()).expect_err("refused");
        let violations = match &error {
            crate::error::AppError::ConfigUnknownKey { violations }
            | crate::error::AppError::ConfigBreaksSchema { violations } => violations.clone(),
            other => panic!("not a schema refusal: {other:?}"),
        };
        (error.code().to_string(), violations)
    }

    // hand-test 2026-11-10: a `--config-set` the schema refuses is refused
    // as a file saying it is, named `--config-set`; it only warned.
    #[test]
    fn test_a_config_set_of_an_unknown_key_is_refused_as_a_file_is() {
        let (code, violations) = refused_by(&[STAMPED], &flags_with(&["sync.auto_sync=true"]));
        assert_eq!(code, "library-application-config-unknown-key");
        assert!(
            violations.contains("[config] unknown key `sync.auto_sync` in --config-set"),
            "{violations}"
        );
    }

    #[test]
    fn test_a_config_set_of_a_word_the_schema_refuses_is_refused_as_a_file_is() {
        let (code, violations) = refused_by(&[STAMPED], &flags_with(&["sync.on_run=true"]));
        assert_eq!(code, "library-application-config-breaks-schema");
        assert!(
            violations.contains("[config] --config-set: `sync.on_run` breaks the schema"),
            "{violations}"
        );
    }

    // hand-test 2 2026-11-11: a refused `--config-set` was said twice, as a
    // warning and again in the refusal; the refusal alone says it.
    #[test]
    fn test_a_refused_config_set_is_said_by_the_refusal_alone() {
        let (_, mem) = Setup {
            layers: &[STAMPED],
            ..Setup::default()
        }
        .fixture();
        let files: &dyn ConfigFiles = mem.as_ref();
        for set in ["sync.on_run=true", "nosuch=1"] {
            let mut notices = Vec::new();
            assert!(load(files, &[], &flags_with(&[set]), &mut notices).is_err());
            assert!(notices.is_empty(), "{set}: {notices:?}");
        }
    }

    // Owner ruling 162: `legacy.go` is the home config's alone; a project's
    // config that says it is refused, naming the key and the home config.
    #[test]
    fn test_a_project_config_saying_legacy_go_is_refused() {
        let project = "schema_version: 9\nlegacy:\n  go: retired\n";
        let (code, violations) = refused_by(&[STAMPED, project], &Flags::default());
        assert_eq!(code, "library-application-config-breaks-schema");
        assert!(violations.contains("`legacy.go`"), "{violations}");
        assert!(violations.contains("home config"), "{violations}");
    }

    // The home config may say it, and its words are present and retired.
    #[test]
    fn test_the_home_config_says_legacy_go_in_words() {
        let home = "schema_version: 9\nlegacy:\n  go: retired\n";
        let (value, _) = loaded(&[home, STAMPED], &[], &Flags::default());
        assert_eq!(
            value
                .get("legacy")
                .and_then(|l| l.get("go"))
                .and_then(angzarr_yaml::Value::as_str),
            Some("retired")
        );
        for word in ["true", "yes", "gone"] {
            let home = format!("schema_version: 9\nlegacy:\n  go: {word}\n");
            let (_, violations) = refused_by(&[home.as_str(), STAMPED], &Flags::default());
            assert!(
                violations.contains("`legacy.go` breaks the schema"),
                "{word}: {violations}"
            );
        }
    }

    // Under `--degraded` a project's `legacy.go` is said and dropped, never
    // read: a project cannot retire Go even then.
    #[test]
    fn test_degraded_drops_a_projects_legacy_go() {
        let project = "schema_version: 9\nlegacy:\n  go: retired\n";
        let flags = Flags {
            degraded: true,
            ..Flags::default()
        };
        let (value, said) = loaded(&[STAMPED, project], &[], &flags);
        assert_eq!(value.get("legacy").and_then(|l| l.get("go")), None);
        assert!(said.iter().any(|s| s.contains("`legacy.go`")), "{said:?}");
    }

    // `--degraded` carries on past a refused `--config-set`, as past a file.
    #[test]
    fn test_degraded_carries_on_past_a_refused_config_set() {
        let flags = Flags {
            degraded: true,
            ..flags_with(&["sync.on_run=true"])
        };
        let (_, mem) = Setup {
            layers: &[STAMPED],
            ..Setup::default()
        }
        .fixture();
        let files: &dyn ConfigFiles = mem.as_ref();
        assert!(load(files, &[], &flags, &mut Vec::new()).is_ok());
    }

    #[test]
    fn test_an_unknown_override_warns_in_its_words_and_is_set_anyway() {
        let (value, said) = loaded(
            &[STAMPED],
            &[("CTXLOOM_CONFIG_NO_SUCH_KEY", "x"), ("HOME", "/h")],
            &Flags::default(),
        );
        assert_eq!(
            said,
            ["CTXLOOM_CONFIG_NO_SUCH_KEY does not match any known config key (resolved as NO.SUCH.KEY); setting it anyway | "]
        );
        assert!(value.get("NO").is_some(), "applied anyway");
    }

    #[test]
    fn test_a_value_the_schema_refuses_is_said_and_the_config_is_not_dropped() {
        let (value, said) = loaded(
            &[STAMPED],
            &[("CTXLOOM_CONFIG_NON_TTY_FORMAT", "yaml")],
            &Flags::default(),
        );
        assert_eq!(said.len(), 1, "{said:?}");
        assert!(
            said[0].starts_with(
                "config override resolution: CTXLOOM_CONFIG_NON_TTY_FORMAT: env set \"non_tty_format\""
            ),
            "{said:?}"
        );
        assert_eq!(
            value
                .get("non_tty_format")
                .and_then(angzarr_yaml::Value::as_str),
            Some("yaml"),
            "set all the same"
        );
    }

    #[test]
    fn test_no_override_leaves_the_layers_and_says_nothing() {
        let (value, said) = loaded(&[STAMPED], &[("HOME", "/h")], &Flags::default());
        assert!(said.is_empty());
        assert_eq!(
            value,
            angzarr_yaml::parse(STAMPED).expect("a document"),
            "stamped as it was"
        );
    }

    #[test]
    fn test_the_flag_beats_the_environment_beats_the_layers() {
        let (value, _) = loaded(
            &[STAMPED],
            &[("CTXLOOM_CONFIG_EDITOR_COMMAND", "emacs")],
            &Flags::default(),
        );
        let command = |v: &angzarr_yaml::Value| {
            v.get("editor")
                .and_then(|e| e.get("command"))
                .and_then(angzarr_yaml::Value::as_str)
                .map(ToString::to_string)
        };
        assert_eq!(command(&value).as_deref(), Some("emacs"));
        let (value, _) = loaded(
            &[STAMPED],
            &[("CTXLOOM_CONFIG_EDITOR_COMMAND", "emacs")],
            &flags_with(&["editor.command=nano"]),
        );
        assert_eq!(command(&value).as_deref(), Some("nano"));
    }
}

// generation 9: a companion is registered by name, with its state (OD-9,
// OD-10); a generation 8 list is migrated, each name enabled.
#[test]
fn test_companions_are_read_by_name_with_their_states() {
    use library::companion::State;
    let c = config("companions:\n  zed: {state: disabled}\n  acme: {state: enabled}\n  bare: {}\n");
    let all: Vec<(String, State, String)> = c
        .companions()
        .into_iter()
        .map(|r| (r.program(), r.state, r.name))
        .collect();
    assert_eq!(
        all,
        [
            (
                "ctxloom-companion-acme".to_string(),
                State::Enabled,
                "acme".to_string()
            ),
            (
                "ctxloom-companion-bare".to_string(),
                State::Enabled,
                "bare".to_string()
            ),
            (
                "ctxloom-companion-zed".to_string(),
                State::Disabled,
                "zed".to_string()
            ),
        ]
    );
    assert!(config("editor:\n  command: vim\n").companions().is_empty());
}

// Ruling 67, 139: each engine block is one line in flow form, and says
// whether it holds the engine's own permissions document.
#[test]
fn test_each_engine_block_is_a_line_that_says_whether_it_holds_permissions() {
    let agent = super::Agent::of(
        "a",
        &parse("engines:\n  opencode:\n    model: m\n  claude-code:\n    permissions:\n      defaultMode: plan\n")
            .expect("a document"),
    );
    assert_eq!(
        agent.row().engine_lines,
        [
            crate::EngineLine {
                engine: "claude-code".to_string(),
                line: "engines.claude-code: {permissions: {defaultMode: plan}}".to_string(),
                permissions: true,
            },
            crate::EngineLine {
                engine: "opencode".to_string(),
                line: "engines.opencode: {model: m}".to_string(),
                permissions: false,
            },
        ]
    );
    let bare = super::Agent::of("a", &parse("engine: claude-code\n").expect("a document"));
    assert!(bare.row().engine_lines.is_empty());
}
