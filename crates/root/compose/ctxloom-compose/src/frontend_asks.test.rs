use super::*;

fn ask(name: &str, child: &str, rule: &str, state: &str) -> v1::Approval {
    v1::Approval {
        name: format!("projects/calm-quiet-heron/approvals/{name}"),
        child: format!("projects/calm-quiet-heron/sessions/{child}"),
        tool_name: "Bash".to_string(),
        input_excerpt: "{\"command\":\"rm -rf build/\"}".to_string(),
        state: state.to_string(),
        options: ["allow-once", "deny"]
            .into_iter()
            .map(|id| v1::ApprovalOption {
                id: id.to_string(),
                rule: if id == "deny" {
                    String::new()
                } else {
                    rule.to_string()
                },
                ..v1::ApprovalOption::default()
            })
            .collect(),
        ..v1::Approval::default()
    }
}

#[test]
fn test_project_listing_a_session_is_its_whole_projects_waiting_asks_whatever_their_root() {
    let listing = project_listing("projects/calm-quiet-heron/sessions/amber-swift-owl").unwrap();
    assert_eq!(listing.parent, "projects/calm-quiet-heron");
    assert_eq!(listing.root, "", "no root: every ask of the project");
    assert!(listing.waiting);
}

#[test]
fn test_project_listing_a_name_that_is_no_session_is_none() {
    assert!(project_listing("amber-swift-owl").is_none());
}

#[test]
fn test_a_waiting_ask_is_told_once_by_its_name() {
    let mut told = AsksTold::default();
    let waiting = [ask(
        "ask-tidy-wren",
        "brisk-copper-moth",
        "Bash(rm -rf build/)",
        "waiting",
    )];
    assert_eq!(told.news(&waiting), ["ask-tidy-wren"]);
    assert!(told.news(&waiting).is_empty(), "the same ask again");
}

#[test]
fn test_an_ask_that_arrives_later_is_told_and_one_that_left_is_not_told_again() {
    let mut told = AsksTold::default();
    let first = ask("ask-a-b", "brisk-copper-moth", "Bash(a)", "waiting");
    let second = ask("ask-c-d", "brisk-copper-moth", "Bash(b)", "waiting");
    assert_eq!(told.news(std::slice::from_ref(&first)).len(), 1);
    // The first was answered: only the second is listed now.
    let news = told.news(std::slice::from_ref(&second));
    assert_eq!(news, ["ask-c-d"]);
    // The first listed again (an answer that raced the listing): not again.
    assert!(told.news(&[first, second]).is_empty());
}

#[test]
fn test_an_ask_that_is_not_waiting_is_never_told() {
    let mut told = AsksTold::default();
    for state in ["answered", "expired", "withdrawn", ""] {
        assert!(told
            .news(&[ask("ask-a-b", "brisk-copper-moth", "Bash(a)", state)])
            .is_empty());
    }
}

#[test]
fn test_count_waiting_counts_only_the_waiting() {
    let listed = [
        ask("ask-a-b", "brisk-copper-moth", "Bash(a)", "waiting"),
        ask("ask-c-d", "brisk-copper-moth", "Bash(b)", "answered"),
        ask("ask-e-f", "brisk-copper-moth", "Bash(c)", "waiting"),
    ];
    assert_eq!(count_waiting(&listed), 2);
    assert_eq!(count_waiting(&[]), 0);
}
