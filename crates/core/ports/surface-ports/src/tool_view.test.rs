use super::*;

/// A made-up, random-looking token (not a credential): 48 characters.
const TOKEN: &str = "k3Jf9Zq2LmX8vB1nRt7YwP5dHs0AcE4uGiOb6QxTzVyN2Mw"; // gitleaks:allow

#[test]
fn a_summary_is_the_first_non_empty_line() {
    assert_eq!(summary_line("\n\n  cargo build\nsecond"), "cargo build");
    assert_eq!(summary_line("   \n"), "");
}

#[test]
fn a_summary_is_cut_at_120_characters_with_an_ellipsis() {
    let long = "a".repeat(200);
    let said = summary_line(&long);
    assert_eq!(said.chars().count(), SUMMARY_CUT + 1);
    assert!(said.ends_with('\u{2026}'));
    let exact = "b".repeat(SUMMARY_CUT);
    assert_eq!(summary_line(&exact), exact);
}

#[test]
fn a_result_is_its_first_five_non_empty_lines_each_cut() {
    let text = "one\n\ntwo\nthree\nfour\nfive\nsix\nseven";
    assert_eq!(result_lines(text), "one\ntwo\nthree\nfour\nfive");
    let long = format!("{}\nok", "c".repeat(300));
    let said = result_lines(&long);
    let first = said.lines().next().unwrap();
    assert_eq!(first.chars().count(), SUMMARY_CUT + 1);
    assert_eq!(said.lines().nth(1), Some("ok"));
    assert_eq!(result_lines(""), "");
}

#[test]
fn a_search_says_its_pattern_and_where() {
    assert_eq!(search_summary("TODO", "src/", true), "\"TODO\" in src/");
    assert_eq!(search_summary("*.rs", "", false), "*.rs");
    assert_eq!(search_summary("", "src/", true), "");
}

#[test]
fn engine_data_names_what_is_said_under_the_engine() {
    let data = engine_data("claude-code", "ls", "a\nb", Some(2));
    assert_eq!(
        data,
        [
            ("claude-code.summary".to_string(), "ls".to_string()),
            ("claude-code.result".to_string(), "a\nb".to_string()),
            ("claude-code.exit".to_string(), "2".to_string()),
        ]
        .into()
    );
    assert!(engine_data("claude-code", "", "", None).is_empty());
}

// The masking seam is `secret_mask::mask`: these stay red until it is wired.
#[test]
fn a_token_in_a_summary_or_a_result_is_masked_before_it_is_kept() {
    let marker = format!("[masked:{}]", TOKEN.len());
    assert_eq!(
        summary_line(&format!("curl -H token={TOKEN} https://x.test")),
        format!("curl -H token={marker} https://x.test")
    );
    assert_eq!(
        result_lines(&format!("ok\nAPI_KEY={TOKEN}")),
        format!("ok\nAPI_KEY={marker}")
    );
}

#[test]
fn a_token_is_masked_whole_before_the_cut_never_kept_in_part() {
    let line = format!("{} {TOKEN}", "x".repeat(110));
    let said = summary_line(&line);
    assert!(!said.contains(&TOKEN[..8]), "{said}");
}
