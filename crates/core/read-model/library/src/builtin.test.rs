//! Owner rulings 110, 111, narrowed by 117 and 123: ctxloom's one built-in
//! bundle holds its operational prompts, compiled in.

use super::*;

// Ruling 117: one bundle, ctxloom's own, and none of ctxloom-default's.
#[test]
fn test_the_one_built_in_bundle_is_ctxloom_s_operational_prompts() {
    assert_eq!(names(), [BUNDLE]);
    assert_eq!(BUNDLE, "ctxloom");
    assert_eq!(ORIGIN, "ctxloom/builtin");
    assert_eq!(SETUP_INTERVIEW_NAME, "setup-interview");
    assert_eq!(DISTILL_NAME, "distill");
    for gone in ["ai-developer", "ai-coding-general", "testing", "git"] {
        assert!(files(gone).is_none(), "{gone} is no longer built in");
    }
    assert_eq!(
        files(BUNDLE).expect("built in").keys().collect::<Vec<_>>(),
        [
            "bundle.yaml",
            "prompts/distill.md",
            "prompts/setup-interview.md"
        ]
    );
}

// Ruling 117: it is NOT generally injected. It ships no profile (so nothing
// inherits one of its), no fragment (so no premise index offers it) and no
// tag (so no tag selection reaches it): only the activity that names one of
// its prompts gets it.
#[test]
fn test_the_built_in_bundle_offers_itself_to_no_session() {
    let read = entries(&|_| false);
    assert_eq!(read.len(), 1);
    let bundle = &read[0].bundle;
    assert!(bundle.profiles.is_empty());
    assert!(bundle.fragments.is_empty());
    assert!(bundle.skills.is_empty());
    assert!(bundle.mcp.is_empty());
    assert!(bundle.hooks.is_empty());
    assert!(bundle.tags.is_empty());
    assert_eq!(
        bundle.commands.keys().collect::<Vec<_>>(),
        ["distill", "setup-interview"]
    );
    for command in bundle.commands.values() {
        assert!(command.body.tags.is_empty());
    }
    assert_eq!(read[0].manifest_path, "<built-in>/ctxloom/bundle.yaml");
}

// The prompts the activities use are the bundle's, as they are read from it.
#[test]
fn test_the_prompts_are_the_bundle_s_items_as_read() {
    let read = entries(&|_| false);
    let commands = &read[0].bundle.commands;
    assert_eq!(commands[SETUP_INTERVIEW_NAME].body.content, SETUP_INTERVIEW);
    assert_eq!(commands[DISTILL_NAME].body.content, DISTILL_PROMPT);
    // The file is Go's, its front matter dropped: it opens with the blank
    // line that followed it.
    assert!(SETUP_INTERVIEW
        .starts_with("\nWelcome to ctxloom! You are running the ctxloom **setup interview**"));
}

// Ruling 123: the distill prompt is the owner's own text (it was
// ctxloom-default's skills/distill.yaml `content`), shipped as ctxloom's:
// no third-party notice beside it.
#[test]
fn test_the_distill_prompt_is_the_owner_s_text() {
    assert!(DISTILL_PROMPT.starts_with(
        "You are a text compression assistant. The user message contains content wrapped in <content_to_compress> tags.\n"
    ));
    assert!(DISTILL_PROMPT.contains(
        "- PRESERVE all mustache template variables exactly as written (e.g., {{variable_name}})"
    ));
    assert!(DISTILL_PROMPT.ends_with(
        "Start directly with the compressed content. First character of output should be from the actual content.\n"
    ));
    assert!(!DISTILL_PROMPT.contains("    "), "its yaml indent is gone");
}

// Ruling 119: the interview no longer says ctxloom-default ships the
// agent-roles fragments, and still offers an equivalent.
#[test]
fn test_the_interview_claims_no_content_that_does_not_ship() {
    let words = SETUP_INTERVIEW
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    assert!(!words.contains("ctxloom-default ships"));
    assert!(words.contains(
        "(a remote's, such as `agent-roles#fragments/developer-escalation`, or author an equivalent)"
    ));
    assert!(words.contains(
        "a finder fragment (a remote's, such as `agent-roles#fragments/finder`, or an equivalent)"
    ));
}

// A project bundle of the same name overrides it: the built-in one is not
// read at all.
#[test]
fn test_a_name_the_project_has_leaves_the_built_in_bundle_out() {
    assert!(entries(&|name| name == BUNDLE).is_empty());
    assert_eq!(entries(&|name| name == "other").len(), 1);
}

#[test]
fn test_no_other_name_has_files() {
    assert!(files("project").is_none());
}
