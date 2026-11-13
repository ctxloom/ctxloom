use std::collections::BTreeMap;

use super::*;

#[test]
fn test_an_mcp_server_renders_its_environment_and_headers_by_key_alone() {
    let server = SurfaceMcp {
        name: "ledger".to_string(),
        command: "ledger-server".to_string(),
        env: BTreeMap::from([("LEDGER_TOKEN".to_string(), "s3cret-value".to_string())]),
        headers: BTreeMap::from([("Authorization".to_string(), "Bearer xyz-value".to_string())]),
        ..SurfaceMcp::default()
    };
    let shown = format!("{server:?}");
    assert!(
        shown.contains("LEDGER_TOKEN") && shown.contains("Authorization"),
        "{shown}"
    );
    assert!(shown.contains("ledger-server"), "{shown}");
    assert!(
        !shown.contains("s3cret-value") && !shown.contains("xyz-value"),
        "{shown}"
    );
}

#[test]
fn test_surfaces_are_empty_only_when_they_offer_nothing_of_any_kind() {
    assert!(SessionSurfaces::default().is_empty());
    let one = |f: fn(&mut SessionSurfaces)| {
        let mut s = SessionSurfaces::default();
        f(&mut s);
        s
    };
    for offered in [
        one(|s| s.commands.push(SurfaceCommand::default())),
        one(|s| s.skills.push(SurfaceSkill::default())),
        one(|s| s.mcp.push(SurfaceMcp::default())),
        one(|s| s.hooks.push(SurfaceHook::default())),
    ] {
        assert!(!offered.is_empty(), "{offered:?}");
    }
}
