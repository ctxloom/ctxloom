use super::*;

#[test]
fn a_relative_slash_path_is_kept() {
    assert!(check_rel("home/settings.json").is_ok());
    assert!(check_rel("records.jsonl").is_ok());
}

#[test]
fn a_path_that_leaves_the_session_directory_is_refused() {
    for bad in [
        "",
        "/etc/passwd",
        "../x",
        "a/../../x",
        "a/./b",
        "a//b",
        "a\\b",
        "a/",
    ] {
        assert_eq!(
            check_rel(bad),
            Err(RecordsError::BadPath {
                rel: bad.to_string()
            }),
            "{bad:?}"
        );
    }
}

#[test]
fn a_records_error_displays_its_code_and_args() {
    assert_eq!(
        RecordsError::BadPath {
            rel: "..".to_string()
        }
        .to_string(),
        "run-ports-records-bad-path rel=.."
    );
    assert_eq!(
        RecordsError::Io {
            harp: "h".to_string(),
            rel: "r".to_string(),
            ext_err_txt: "denied".to_string()
        }
        .to_string(),
        "run-ports-records-io harp=h rel=r ext_err_txt=denied"
    );
}
