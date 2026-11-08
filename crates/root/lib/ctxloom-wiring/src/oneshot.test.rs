use ports::{Project, ProjectIdentity};

use super::Told;

fn project() -> Project {
    Project {
        id: "calm-otter".to_string(),
        root: Some("/work/proj".to_string()),
        store: "/h/.ctxloom/projects/calm-otter/ctxloom.db".into(),
    }
}

// The project is told once, before the store is opened, and the use case is
// then given the answer it already has.
#[test]
fn a_project_already_told_is_told_again_as_it_was() {
    let mut told = Told(project());
    assert_eq!(told.for_write(None).unwrap(), project());
    assert_eq!(told.for_read(None).unwrap(), Some(project()));
    assert_eq!(told.every_project().unwrap(), vec![project()]);
    assert_eq!(told.rootless_directory().unwrap(), None);
}

// Step 1b-ii, and the journey's promise: Alice leaves by deleting `.ctxloom`
// and `~/.ctxloom`, so a project that is told for a run is not marked at its
// root (the registry's `.project-id` marker is a write outside `.ctxloom`).
// The older marker, inside `.ctxloom`, is what a ctxloom-only project keeps.
mod told {
    use std::collections::VecDeque;
    use std::path::Path;
    use std::sync::Arc;

    use file_system::{Fs, Rooted};
    use os::Mode;
    use ports::Names;
    use project_registry::Environment;

    use super::super::tell_project;

    struct Scripted(VecDeque<&'static str>);

    impl Names for Scripted {
        fn task_id(&mut self) -> String {
            unreachable!("a project is named with three words")
        }
        fn task_id_for(&mut self, _: &str, _: u64) -> String {
            unreachable!("a project is named with three words")
        }
        fn project_id(&mut self) -> String {
            self.0.pop_front().expect("a name was scripted").to_string()
        }
    }

    const PROJECT: &str = "/work/proj";

    fn world() -> (Arc<Rooted>, Environment) {
        let fs = Arc::new(Rooted::temp_with_cwd(Path::new(PROJECT)).unwrap());
        fs.create_dir_all(Path::new(PROJECT), Mode::from_bits(0o755))
            .unwrap();
        fs.create_dir_all(Path::new("/home/u"), Mode::from_bits(0o755))
            .unwrap();
        let env = Environment {
            home: Some("/home/u".into()),
            root: None,
            project_id: None,
        };
        (fs, env)
    }

    fn text(fs: &Rooted, path: &str) -> Option<String> {
        fs.read_to_string(Path::new(path)).ok()
    }

    #[test]
    fn a_new_project_is_named_and_marked_inside_ctxloom_alone() {
        let (fs, env) = world();
        let mut names = Scripted(["jolly-mute-magma"].into());
        let project = tell_project(fs.clone(), env, &mut names).unwrap();
        assert_eq!(project.id, "jolly-mute-magma");
        assert_eq!(project.root.as_deref(), Some(PROJECT));
        assert_eq!(
            text(&fs, "/work/proj/.ctxloom/project-id").as_deref(),
            Some("jolly-mute-magma\n")
        );
        assert!(!fs.exists(Path::new("/work/proj/.project-id")).unwrap());
    }

    #[test]
    fn the_same_project_is_told_again_as_it_was() {
        let (fs, env) = world();
        let mut names = Scripted(["jolly-mute-magma"].into());
        tell_project(fs.clone(), env.clone(), &mut names).unwrap();
        // No second name is scripted: none may be drawn.
        let again = tell_project(fs.clone(), env, &mut names).unwrap();
        assert_eq!(again.id, "jolly-mute-magma");
        assert!(!fs.exists(Path::new("/work/proj/.project-id")).unwrap());
    }

    #[test]
    fn a_project_the_registry_already_marks_is_used_and_nothing_is_written() {
        let (fs, env) = world();
        fs.write_atomic(
            Path::new("/work/proj/.project-id"),
            b"calm-otter\n",
            file_system::Perms::Exact(Mode::from_bits(0o644)),
        )
        .unwrap();
        let mut names = Scripted(VecDeque::new());
        let project = tell_project(fs.clone(), env, &mut names).unwrap();
        assert_eq!(project.id, "calm-otter");
        assert!(!fs.exists(Path::new("/work/proj/.ctxloom")).unwrap());
    }

    #[test]
    fn a_name_a_project_already_has_a_directory_for_is_passed_over() {
        let (fs, env) = world();
        fs.create_dir_all(
            Path::new("/home/u/.ctxloom/projects/taken-name-here"),
            Mode::from_bits(0o755),
        )
        .unwrap();
        let mut names = Scripted(["taken-name-here", "fresh-name-here"].into());
        let project = tell_project(fs, env, &mut names).unwrap();
        assert_eq!(project.id, "fresh-name-here");
    }
}
