//! The setup use cases, wired.

// doctor.feature: "A bundle in the old metadata layout is a warning naming
// the git mv" (step 10c U5, D15): each old layout is said in the library's
// words, a project's own with the move that migrates it.
#[test]
fn an_old_layout_is_said_in_the_librarys_words_with_its_move() {
    let said = super::said_old_layouts(&[library::BundleError::OldLayout {
        bundle: "demo".to_string(),
        kind: "skill".to_string(),
        old: ".ctxloom/a/skills/.review.meta.yaml".to_string(),
        new: ".ctxloom/a/skills/review/.meta.yaml".to_string(),
        remote: String::new(),
    }]);
    assert_eq!(said.len(), 1);
    assert!(
        said[0]
            .0
            .starts_with("demo keeps its skill metadata at .ctxloom/a/skills/.review.meta.yaml"),
        "{said:?}"
    );
    assert_eq!(
        said[0].1,
        "git mv .ctxloom/a/skills/.review.meta.yaml .ctxloom/a/skills/review/.meta.yaml"
    );
}

// Claim C (owner's principle): doctor sets up and evaluates with the machinery
// of an actual run. An agent whose profile names a bundle installed from a
// remote runs (the daemon's child plan and a hosted run read the remotes), so
// doctor must not report it unresolvable.
#[test]
fn doctor_resolves_an_agent_naming_a_remote_installed_bundle_as_a_run_does() {
    let (root, dir) = crate::fixtures::project_with_a_remote_bundle();
    let home = root.root().join("home");
    let wiring = crate::daemon::caller_wiring(
        &home,
        std::path::Path::new("/tmp"),
        (&dir, Some(&dir)),
        [(
            crate::fixtures::home_var(),
            home.display().to_string().as_str(),
        )],
    );
    let uses = super::SetupUseCases::build(&wiring, "v0.0.0").expect("builds");
    let report = uses.doctor.report(false);
    let agents = report
        .checks
        .iter()
        .find(|c| c.detail.contains("orch") && c.marker.contains("AGENTS"))
        .map(|c| (c.status, c.detail.clone()));
    let rows: Vec<String> = report
        .checks
        .iter()
        .filter(|c| c.detail.contains("hub/demo"))
        .map(|c| format!("{} {:?}: {}", c.marker, c.status, c.detail))
        .collect();
    assert!(rows.is_empty(), "doctor says: {rows:?} ({agents:?})");
}
