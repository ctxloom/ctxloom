use super::*;

#[test]
fn test_the_engines_this_build_knows_are_known() {
    assert!(!known("nosuch") && known("claude-code") && known("mock-noskills"));
}

// engines/claude/surfaces.go and definition.go
#[test]
fn test_claude_offers_one_approach_per_surface() {
    for (kind, approach) in [
        ("context", "system-prompt"),
        ("mcp", "mcp-config"),
        ("settings", "settings"),
        ("hooks", "settings-hooks"),
        ("commands", "commands-dir"),
        ("skills", "skills-dir"),
    ] {
        assert_eq!(
            approaches("claude-code", kind),
            Some([approach].as_slice()),
            "{kind}"
        );
    }
    assert_eq!(approaches("claude-code", "nosuch"), None);
    // An engine whose declaration is not carried yet answers none: the caller
    // checks the shape alone.
    assert_eq!(approaches("opencode", "context"), None);
}

#[test]
fn test_the_vocabularies_are_the_ones_goes() {
    assert_eq!(ROOTS, ["session-home", "project-root", "work-dir"]);
    assert_eq!(SURFACE_KINDS.len(), 6);
}
