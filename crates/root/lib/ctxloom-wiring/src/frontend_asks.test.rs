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
fn test_listing_under_a_session_is_its_whole_projects_waiting_asks_whatever_their_root() {
    let listing = listing_under("projects/calm-quiet-heron/sessions/amber-swift-owl").unwrap();
    assert_eq!(listing.parent, "projects/calm-quiet-heron");
    assert_eq!(listing.root, "", "no root: every ask of the project");
    assert!(listing.waiting);
}

#[test]
fn test_listing_under_a_name_that_is_no_session_is_none() {
    assert!(listing_under("amber-swift-owl").is_none());
}

#[test]
fn test_a_waiting_ask_is_told_once_with_who_what_and_how_to_answer() {
    let mut told = AsksTold::default();
    let waiting = [ask(
        "ask-tidy-wren",
        "brisk-copper-moth",
        "Bash(rm -rf build/)",
        "waiting",
    )];
    let news = told.news(&waiting);
    assert_eq!(news.len(), 1);
    assert_eq!(news[0].0, "ask-tidy-wren");
    assert_eq!(
        news[0].1,
        "brisk-copper-moth asks to run Bash(rm -rf build/): answer with \
         `ctxloom session approvals answer ask-tidy-wren <allow-once|deny>`"
    );
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
    assert_eq!(news.len(), 1);
    assert_eq!(news[0].0, "ask-c-d");
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
fn test_a_tool_with_no_rule_is_shown_by_its_name_and_input() {
    let line = line_of(&ask("ask-a-b", "brisk-copper-moth", "", "waiting"));
    assert!(
        line.contains("asks to run Bash {\"command\":\"rm -rf build/\"}:"),
        "{line}"
    );
}

#[test]
fn test_an_ask_with_no_options_names_the_option_word() {
    let mut bare = ask("ask-a-b", "brisk-copper-moth", "", "waiting");
    bare.options.clear();
    assert!(line_of(&bare).ends_with("answer ask-a-b <option>`"));
}

#[test]
fn test_another_agents_words_carry_no_control_character_to_the_terminal() {
    let hostile = "Bash(echo \u{1b}]0;owned\u{7}\u{1b}[2J\r\nnext line)";
    let line = line_of(&ask("ask-a-b", "brisk-copper-moth", hostile, "waiting"));
    assert!(!line.chars().any(char::is_control), "{line:?}");
    assert!(line.contains("owned"), "its words are still shown: {line}");
}

#[test]
fn test_a_long_command_is_cut_to_a_bounded_line_on_a_character_boundary() {
    let long = format!("Bash({})", "\u{e9}".repeat(500));
    let line = line_of(&ask("ask-a-b", "brisk-copper-moth", &long, "waiting"));
    assert!(line.contains("..."), "{line}");
    assert!(line.chars().count() < 400, "{}", line.chars().count());
}
