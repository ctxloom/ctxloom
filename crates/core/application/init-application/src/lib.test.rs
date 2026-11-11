//! `init`. Go: `cli/init.feature` (release/0.7, f36e8dfb21).

use crate::{personal_name, prompt, SETUP_INTERVIEW};

// init.feature: "Init prompt emits the whole setup interview body": the
// headings from both ends of it.
#[test]
fn the_prompt_carries_the_phase_headings_from_both_ends() {
    let text = prompt();
    for heading in [
        "Phase 1 \u{2014} Open, orient, scan",
        "Phase 2 \u{2014} Companions",
        "Phase 4 \u{2014} Agents",
        "Phase 5 \u{2014} Close",
    ] {
        assert!(text.contains(heading), "{heading}");
    }
    assert!(text.ends_with(&format!("{SETUP_INTERVIEW}\n")));
    assert!(!text.starts_with("---"), "the front matter is dropped");
}

// Owner ruling 117: the interview is the built-in bundle's
// `ctxloom#prompts/setup-interview`, the one copy of its text in the binary.
#[test]
fn the_interview_is_the_built_in_bundle_s_prompt() {
    assert_eq!(SETUP_INTERVIEW, library::builtin::SETUP_INTERVIEW);
}

// init.feature: the personal remote is "personal"; the next are numbered.
#[test]
fn the_remotes_are_named_personal_then_numbered() {
    assert_eq!(personal_name(0), "personal");
    assert_eq!(personal_name(1), "personal-2");
    assert_eq!(personal_name(2), "personal-3");
}

mod init {
    use std::cell::{Cell, RefCell};
    use std::rc::Rc;
    use std::sync::Arc;

    use ctxloom_ports::{Failure, Put, Scaffold, UserFiles};
    use deps_application::fakes::Fixture;

    use crate::{
        Create, Init, InitError, InitRequest, Terminal, OUTPUT_DIR_QUESTION, OUTPUT_DIR_SUGGESTED,
    };

    /// The scaffold, counting how often it was asked for, and the output
    /// directories written into its config.
    #[derive(Default)]
    struct Creator {
        made: Cell<u32>,
        refuses: bool,
        outputs: RefCell<Vec<String>>,
        refuses_output: bool,
        /// The dependency use cases over the project once created; none,
        /// a fixture of its own.
        project: Option<Rc<deps_application::Deps>>,
    }

    impl Create for Rc<Creator> {
        fn create(&self) -> Result<(), Failure> {
            self.made.set(self.made.get() + 1);
            if self.refuses {
                return Err(Box::new(deps_application::AppError::DefaultNeedsName));
            }
            Ok(())
        }

        fn set_output_dir(&self, dir: &str) -> Result<(), Failure> {
            if self.refuses_output {
                return Err(Box::new(deps_application::AppError::DefaultNeedsName));
            }
            self.outputs.borrow_mut().push(dir.to_string());
            Ok(())
        }

        fn deps(&self) -> Result<Rc<deps_application::Deps>, Failure> {
            Ok(self
                .project
                .clone()
                .unwrap_or_else(|| Rc::new(Fixture::new().deps)))
        }
    }

    /// Her terminal: there or not, and the line she types (none: the input
    /// closed).
    struct Typed {
        terminal: bool,
        line: Option<&'static str>,
        asked: RefCell<Vec<(String, String)>>,
    }

    impl Typed {
        fn new(terminal: bool, line: Option<&'static str>) -> Typed {
            Typed {
                terminal,
                line,
                asked: RefCell::new(Vec::new()),
            }
        }
    }

    impl Terminal for Typed {
        fn is_terminal(&self) -> bool {
            self.terminal
        }
        fn confirm(&self, _: &str) -> bool {
            unreachable!("the output directory is asked, not confirmed")
        }
        fn ask(&self, question: &str, suggested: &str) -> Option<String> {
            self.asked
                .borrow_mut()
                .push((question.to_string(), suggested.to_string()));
            self.line.map(String::from)
        }
    }

    // Rulings 89, 106: a first init at a terminal asks where a session's
    // outputs go, suggesting `ctxloom/`, and writes the answer as
    // `output.dir`; a bare Enter or a closed input takes the suggestion, and
    // the answer is trimmed.
    #[test]
    fn a_first_init_at_a_terminal_asks_for_the_output_directory_and_writes_it() {
        for (line, want) in [
            (Some("notes/sessions\n"), "notes/sessions"),
            (Some("  /var/out  \n"), "/var/out"),
            (Some("\n"), OUTPUT_DIR_SUGGESTED),
            (None, OUTPUT_DIR_SUGGESTED),
        ] {
            let creator = Rc::new(Creator::default());
            let terminal = Typed::new(true, line);
            let chosen = init_with(Ok(false), creator.clone(), Fixture::new())
                .choose_output_dir(&terminal)
                .expect("written");
            assert_eq!(chosen.as_deref(), Some(want), "{line:?}");
            assert_eq!(*creator.outputs.borrow(), [want.to_string()]);
            assert_eq!(
                *terminal.asked.borrow(),
                [(
                    OUTPUT_DIR_QUESTION.to_string(),
                    OUTPUT_DIR_SUGGESTED.to_string()
                )]
            );
        }
        assert_eq!(OUTPUT_DIR_SUGGESTED, "ctxloom/");
        assert!(OUTPUT_DIR_QUESTION.contains("not gitignored"), "it says so");
        assert!(OUTPUT_DIR_QUESTION.contains("committed"), "it says so");
    }

    // Off a terminal nobody is asked and nothing is written: the default
    // stands without a line in the config.
    #[test]
    fn off_a_terminal_the_output_directory_is_not_asked() {
        let creator = Rc::new(Creator::default());
        let terminal = Typed::new(false, Some("notes\n"));
        let chosen = init_with(Ok(false), creator.clone(), Fixture::new())
            .choose_output_dir(&terminal)
            .expect("ok");
        assert_eq!(chosen, None);
        assert!(terminal.asked.borrow().is_empty());
        assert!(creator.outputs.borrow().is_empty());
    }

    // A write of the answer that is refused ends init with its reason.
    #[test]
    fn an_output_directory_that_cannot_be_written_ends_init() {
        let creator = Rc::new(Creator {
            refuses_output: true,
            ..Creator::default()
        });
        let error = init_with(Ok(false), creator, Fixture::new())
            .choose_output_dir(&Typed::new(true, Some("x\n")))
            .expect_err("refused");
        assert!(matches!(error, InitError::Create(_)));
    }

    struct Dir {
        present: Result<bool, &'static str>,
    }

    impl Scaffold for Dir {
        fn app_dir(&self) -> String {
            "/work/.ctxloom".to_string()
        }
        fn exists(&self, _: &str) -> Result<bool, Failure> {
            unreachable!()
        }
        fn write(&self, _: &[Put]) -> Result<(), Failure> {
            unreachable!()
        }
    }

    impl UserFiles for Dir {
        fn exists(&self, _: &str) -> Result<bool, Failure> {
            unreachable!()
        }
        fn is_dir(&self, path: &str) -> Result<bool, Failure> {
            assert_eq!(path, "/work/.ctxloom");
            self.present
                .map_err(|_| Box::new(deps_application::AppError::DefaultNeedsName) as Failure)
        }
        fn read_file(&self, _: &str) -> Result<Vec<u8>, Failure> {
            unreachable!()
        }
        fn read_tree(&self, _: &str) -> Result<Vec<Put>, Failure> {
            unreachable!()
        }
        fn write_file(&self, _: &str, _: &[u8], _: bool) -> Result<bool, Failure> {
            unreachable!()
        }
        fn write_tree(&self, _: &str, _: &[Put]) -> Result<(), Failure> {
            unreachable!()
        }
    }

    fn init(present: Result<bool, &'static str>) -> Init {
        init_with(present, Rc::new(Creator::default()), Fixture::new())
    }

    fn init_with(
        present: Result<bool, &'static str>,
        creator: Rc<Creator>,
        fixture: Fixture,
    ) -> Init {
        let dir = Arc::new(Dir { present });
        Init::new(dir.clone(), dir, Rc::new(fixture.deps), Box::new(creator))
    }

    // init.feature: a directory with no `.ctxloom` is scaffolded, and
    // nothing is pulled (owner rulings 110, 111): the seeded profile's
    // default content is built in. Even a profile that named a remote would
    // wait for `deps pull` or a run.
    #[test]
    fn a_directory_without_ctxloom_is_scaffolded_and_nothing_is_pulled() {
        let fixture = Fixture::new();
        let url = "file:///srv/origin.git";
        fixture.origin("origin", url);
        fixture.profile("dev", &[&Fixture::reference(url, "demo")]);
        let files = Arc::clone(&fixture.files);
        let creator = Rc::new(Creator::default());
        let done = init_with(Ok(false), creator.clone(), fixture)
            .init(&InitRequest::default())
            .expect("ok");
        assert_eq!(creator.made.get(), 1);
        assert!(!done.existed);
        assert_eq!(done.app_dir, "/work/.ctxloom");
        assert!(files.installed_identities().is_empty());
        assert!(files.lock_text().is_none());
        assert!(done.notices.is_empty(), "{:?}", done.notices);
    }

    // The re-run branch scaffolds nothing and pulls nothing.
    #[test]
    fn an_existing_directory_is_not_scaffolded_again() {
        let creator = Rc::new(Creator::default());
        let done = init_with(Ok(true), creator.clone(), Fixture::new())
            .init(&InitRequest::default())
            .expect("ok");
        assert_eq!(creator.made.get(), 0);
        assert!(done.existed);
    }

    // A scaffold that is refused ends init with the scaffold's own reason.
    #[test]
    fn a_scaffold_that_is_refused_ends_init() {
        let creator = Rc::new(Creator {
            refuses: true,
            ..Creator::default()
        });
        let error = init_with(Ok(false), creator, Fixture::new())
            .init(&InitRequest::default())
            .expect_err("refused");
        assert!(matches!(error, InitError::Create(_)));
    }

    // Hand-test 2026-11-10: a fresh `init --remote` registers the remote in
    // the project it has just created, never through the use cases built
    // before the project was there (which wrote `~/.ctxloom/remotes.yaml`).
    #[test]
    fn a_fresh_init_registers_its_remotes_in_the_project_it_created() {
        let project = Fixture::new();
        let registry = project.files.clone();
        let wired = Fixture::new();
        let before = wired.files.clone();
        let creator = Rc::new(Creator {
            project: Some(Rc::new(project.deps)),
            ..Creator::default()
        });
        let done = init_with(Ok(false), creator, wired)
            .init(&InitRequest {
                remotes: vec!["file:///srv/mine.git".to_string()],
                forge: "git".to_string(),
            })
            .expect("ok");
        assert_eq!(done.remotes.len(), 1, "{:?}", done.notices);
        let text = registry.registry_text().expect("the project's registry");
        assert!(
            text.contains("personal") && text.contains("mine.git"),
            "{text}"
        );
        assert!(
            before.registry_text().is_none(),
            "nothing reached the registry the use cases saw before the project: {:?}",
            before.registry_text()
        );
    }

    // A re-run's project was there all along: its remotes go through the
    // use cases it was given.
    #[test]
    fn a_rerun_registers_its_remotes_through_the_use_cases_it_was_given() {
        let wired = Fixture::new();
        let registry = wired.files.clone();
        let done = init_with(Ok(true), Rc::new(Creator::default()), wired)
            .init(&InitRequest {
                remotes: vec!["file:///srv/mine.git".to_string()],
                forge: "git".to_string(),
            })
            .expect("ok");
        assert_eq!(done.remotes.len(), 1, "{:?}", done.notices);
        assert!(registry
            .registry_text()
            .is_some_and(|t| t.contains("personal")));
    }

    #[test]
    fn an_existing_directory_is_reported_as_existing() {
        let done = init(Ok(true)).init(&InitRequest::default()).expect("ok");
        assert!(done.existed);
        assert_eq!(done.app_dir, "/work/.ctxloom");
        assert!(done.remotes.is_empty());
    }

    #[test]
    fn a_directory_that_cannot_be_inspected_is_the_adapters_failure() {
        let error = init(Err("denied"))
            .init(&InitRequest::default())
            .expect_err("fails");
        assert!(matches!(error, InitError::Files(_)));
    }
}

mod error {
    use angzarr_diagnostic::Diagnostic;

    use crate::InitError;

    #[test]
    fn each_variant_is_its_cause_as_itself() {
        let cause = || Box::new(deps_application::AppError::DefaultNeedsName);
        let want = cause();
        for error in [InitError::Files(cause()), InitError::Create(cause())] {
            assert_eq!(error.code(), want.code());
            assert_eq!(error.args().len(), want.args().len());
            assert!(error.to_string().contains(want.code()));
        }
    }

    // Every variant is its cause's code and its cause's args, names and
    // values, from a cause that has some.
    #[test]
    fn each_variant_carries_its_causes_args() {
        use angzarr_diagnostic::Arg;
        let cause = || {
            Box::new(deps_application::AppError::NoRemoteNamed {
                name: "origin".to_string(),
            })
        };
        let want = cause();
        assert_eq!(want.args().len(), 1, "the cause has an arg to carry");
        for error in [InitError::Files(cause()), InitError::Create(cause())] {
            assert_eq!(error.code(), want.code());
            let args = error.args();
            assert_eq!(args.len(), 1);
            assert_eq!(args[0].0, want.args()[0].0);
            assert!(
                matches!(&args[0].1, Arg::Str(v) if v == "origin"),
                "{:?}",
                args[0].1
            );
        }
    }
}
