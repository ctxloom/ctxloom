use super::*;

#[test]
fn an_asks_key_names_its_run_and_its_engines_request() {
    assert_eq!(ask_key("r-1", "toolu_1"), "approval:r-1:toolu_1");
}

#[test]
fn an_asks_root_is_the_uuidv5_of_its_session_and_its_key() {
    let session = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b";
    let namespace = uuid::Uuid::parse_str(session).unwrap();
    assert_eq!(
        ask_root(session, "r-1", "toolu_1"),
        uuid::Uuid::new_v5(&namespace, b"approval:r-1:toolu_1")
    );
    assert_ne!(
        ask_root(session, "r-1", "toolu_1"),
        ask_root(session, "r-1", "toolu_2")
    );
    assert_eq!(
        ask_root("not a uuid", "r", "q"),
        uuid::Uuid::new_v5(&uuid::Uuid::nil(), b"approval:r:q")
    );
}

#[test]
fn an_asks_name_begins_ask_and_only_such_a_word_names_an_ask() {
    assert_eq!(ask_name("tidy-wren"), "ask-tidy-wren");
    assert!(is_ask_name("ask-tidy-wren"));
    assert!(!is_ask_name("brisk-copper-moth"));
    assert!(!is_ask_name("task-x"));
}

#[test]
fn an_approvals_resource_name_reads_back_and_roots_its_claim_on_itself() {
    let name = ApprovalName::parse("projects/amber/approvals/ask-tidy-wren").unwrap();
    assert_eq!(name.project, "amber");
    assert_eq!(name.ask, "ask-tidy-wren");
    assert_eq!(name.to_string(), "projects/amber/approvals/ask-tidy-wren");
    assert_eq!(
        name_root(&name),
        sessions::root_of("projects/amber/approvals/ask-tidy-wren")
    );
    for bad in [
        "projects/amber/sessions/x",
        "projects//approvals/x",
        "projects/a/approvals/",
        "projects/a/approvals/x/y",
        "amber/approvals/x",
    ] {
        assert_eq!(ApprovalName::parse(bad), None, "{bad}");
    }
}

#[test]
fn a_fact_book_carries_its_fact_and_its_external_id() {
    let cover = angzarr_embedded::v1::Cover {
        domain: "approvals".to_string(),
        root: None,
        edition: None,
    };
    let fact = crate::RecordApprovalWithdrawn {
        ask_id: "ask-a-b".to_string(),
        reason: "r".to_string(),
    };
    let book = fact_book(cover.clone(), &fact, "approval-withdrawn:ask-a-b");
    assert_eq!(book.cover, Some(cover));
    match book.page.unwrap().body.unwrap() {
        command_page::Body::Fact(f) => {
            assert_eq!(f.origin.unwrap().external_id, "approval-withdrawn:ask-a-b");
            assert!(f
                .payload
                .unwrap()
                .type_url
                .ends_with("RecordApprovalWithdrawn"));
        }
        other => panic!("not a fact: {other:?}"),
    }
}
