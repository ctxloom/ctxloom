use super::*;

fn handing_over() -> HandingOver {
    HandingOver {
        daemon: "v0.1.0-a".to_string(),
        harps: vec!["brisk-copper-moth".to_string(), "lone-wolf-pup".to_string()],
        seconds: 300,
    }
}

// OD-54: the drain's warning is this crate's code, its args (its stamp, the
// harps it hands over, the period in seconds) and its en-US text.
#[test]
fn the_warning_is_the_code_its_args_and_its_text() {
    let notice = handing_over().notice();
    assert_eq!(notice.code, "daemon-api-handing-over");
    let args: Vec<(String, String)> = notice
        .args
        .iter()
        .map(|arg| (arg.name.clone(), arg.value.clone()))
        .collect();
    assert_eq!(
        args,
        [
            ("daemon".to_string(), "v0.1.0-a".to_string()),
            (
                "harps".to_string(),
                "brisk-copper-moth, lone-wolf-pup".to_string()
            ),
            ("seconds".to_string(), "300".to_string()),
        ]
    );
    assert_eq!(notice.text, handing_over().to_string());
    for part in ["v0.1.0-a", "lone-wolf-pup", "300"] {
        assert!(notice.text.contains(part), "{part}: {}", notice.text);
    }
}
