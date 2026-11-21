use super::*;

fn incomplete() -> StartUpIncomplete {
    StartUpIncomplete {
        why: "the board's catch-up: the store is locked".to_string(),
    }
}

// OD-52: the notice a daemon whose start-up catch-up failed carries is this
// crate's code, its one arg the why, and its en-US text.
#[test]
fn the_notice_is_the_code_its_why_and_its_text() {
    let notice = incomplete().notice();
    assert_eq!(notice.code, "daemon-api-start-up-incomplete");
    assert_eq!(
        notice.args,
        [v1::NeedArg {
            name: "why".to_string(),
            value: "the board's catch-up: the store is locked".to_string(),
        }]
    );
    assert_eq!(notice.text, incomplete().to_string());
    assert!(
        notice.text.contains("the store is locked"),
        "{}",
        notice.text
    );
}
