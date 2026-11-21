//! A trust's name and its root.

use super::*;

#[test]
fn a_projects_trust_is_named_under_its_harp() {
    let project = ProjectName::parse("projects/amber-quiet-heron").unwrap();
    assert_eq!(trust_name(&project), "projects/amber-quiet-heron/trust");
    assert_eq!(
        TrustName::of(project.clone()).to_string(),
        "projects/amber-quiet-heron/trust"
    );
}

#[test]
fn a_trust_name_reads_back_its_project_and_nothing_else_is_one() {
    let name = TrustName::parse("projects/amber-quiet-heron/trust").unwrap();
    assert_eq!(name.project().harp(), "amber-quiet-heron");
    assert_eq!(TrustName::parse("projects/amber-quiet-heron"), None);
    assert_eq!(TrustName::parse("projects/amber-quiet-heron/trusts"), None);
    assert_eq!(TrustName::parse("projects/amber-quiet-heron-trust"), None);
    assert_eq!(TrustName::parse("trusts/project-amber-quiet-heron"), None);
}

#[test]
fn a_trusts_root_is_the_uuidv5_of_its_name_as_every_ctxloom_root_is() {
    let name = "projects/amber-quiet-heron/trust";
    let expected = sessions::root_of(name);
    assert_eq!(trust_root(name).value.as_ref(), expected.as_bytes());
    assert_eq!(TrustName::parse(name).unwrap().root(), trust_root(name));
    assert_ne!(
        trust_root(name),
        trust_root("projects/brisk-copper-moth/trust")
    );
}
