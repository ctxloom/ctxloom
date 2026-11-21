//! init's preview needs and what the apply makes of their answers (step
//! 10c U6).

use super::*;

fn ids(needs: &[Need]) -> Vec<&str> {
    needs.iter().map(|need| need.id.as_str()).collect()
}

#[test]
fn test_the_needs_are_the_folder_the_guidance_each_shipped_companion_then_by_name() {
    let asked = needs(&["mempalace".to_string()]);
    assert_eq!(
        ids(&asked),
        [
            "output-dir",
            "premised",
            "companion:mempalace",
            "companions"
        ]
    );
    assert_eq!(
        asked[0],
        Need {
            id: OUTPUT_DIR_ID.to_string(),
            text: OUTPUT_DIR_QUESTION.to_string(),
            kind: Kind::Ask {
                suggestion: "ctxloom/".to_string()
            },
        }
    );
    assert_eq!(
        asked[1].kind,
        Kind::Choose {
            choices: vec!["on-demand".to_string(), "always".to_string()]
        }
    );
    assert_eq!(asked[1].text, PREMISED_QUESTION);
    assert_eq!(asked[2].kind, Kind::Confirm);
    assert_eq!(asked[2].text, "Add the mempalace companion?");
    assert_eq!(
        asked[3].kind,
        Kind::Ask {
            suggestion: String::new()
        }
    );
    assert_eq!(asked[3].text, COMPANIONS_BY_NAME);
    assert_eq!(ids(&needs(&[])), ["output-dir", "premised", "companions"]);
}

#[test]
fn test_nothing_answered_chooses_nothing() {
    assert_eq!(chosen(&[]), Chosen::default());
}

#[test]
fn test_the_folder_answered_is_written_trimmed_and_a_blank_one_is_the_suggestion() {
    let given = |text: &str| vec![(OUTPUT_DIR_ID.to_string(), Given::Text(text.to_string()))];
    assert_eq!(
        chosen(&given("  out/ \n")).output_dir.as_deref(),
        Some("out/")
    );
    assert_eq!(chosen(&given("")).output_dir.as_deref(), Some("ctxloom/"));
    assert_eq!(chosen(&given("out/")).premised, None);
}

#[test]
fn test_the_guidance_answered_is_one_of_its_words_else_the_suggestion() {
    let given = |word: &str| vec![(PREMISED_ID.to_string(), Given::Chosen(word.to_string()))];
    assert_eq!(
        chosen(&given(" Always ")).premised.as_deref(),
        Some("always")
    );
    assert_eq!(
        chosen(&given("on-demand")).premised.as_deref(),
        Some("on-demand")
    );
    assert_eq!(
        chosen(&given("sometimes")).premised.as_deref(),
        Some("on-demand")
    );
    assert_eq!(chosen(&given("")).premised.as_deref(), Some("on-demand"));
    assert_eq!(chosen(&given("always")).output_dir, None);
}
