use super::{git_config, GitUser, GIT_CONFIG_FILE};

fn user(name: &str, email: &str) -> GitUser {
    GitUser {
        name: name.to_string(),
        email: email.to_string(),
    }
}

// Owner ruling 112: the session home's `.gitconfig` holds the user's identity
// and nothing else, each value quoted as git reads it.
#[test]
fn the_identity_is_a_user_section_of_name_and_email() {
    assert_eq!(GIT_CONFIG_FILE, ".gitconfig");
    assert_eq!(
        git_config(&user("Alice Example", "alice@example.com")).as_deref(),
        Some("[user]\n\tname = \"Alice Example\"\n\temail = \"alice@example.com\"\n")
    );
}

#[test]
fn a_quote_or_backslash_in_a_value_is_escaped() {
    assert_eq!(
        git_config(&user(r#"A "B" \C"#, "")).as_deref(),
        Some("[user]\n\tname = \"A \\\"B\\\" \\\\C\"\n")
    );
}

#[test]
fn an_unknown_part_is_left_out_and_no_identity_is_no_file() {
    assert_eq!(
        git_config(&user("", "a@x")).as_deref(),
        Some("[user]\n\temail = \"a@x\"\n")
    );
    assert_eq!(git_config(&user("", "")), None);
}

// A value is one line: a newline in it would start another key.
#[test]
fn a_line_break_in_a_value_is_dropped() {
    assert_eq!(
        git_config(&user("Al\nice", "")).as_deref(),
        Some("[user]\n\tname = \"Alice\"\n")
    );
}
