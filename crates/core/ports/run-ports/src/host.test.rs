use std::collections::BTreeMap;

use super::{
    search_path_led_by, search_path_separator, session_profile, WINDOWS_PROFILE_VARIABLES,
    WINDOWS_STARTUP_VARIABLES,
};

#[test]
fn test_path_s_separator_is_a_semicolon_on_windows_and_a_colon_elsewhere() {
    assert_eq!(search_path_separator("windows"), ';');
    assert_eq!(search_path_separator("linux"), ':');
    assert_eq!(search_path_separator("macos"), ':');
}

#[test]
fn test_the_first_directory_leads_the_inherited_path_as_the_platform_joins_it() {
    assert_eq!(
        search_path_led_by("linux", "/opt/ctxloom", Some("/usr/bin:/bin")),
        "/opt/ctxloom:/usr/bin:/bin"
    );
    assert_eq!(
        search_path_led_by("windows", r"C:\ctxloom", Some(r"C:\Windows;C:\bin")),
        r"C:\ctxloom;C:\Windows;C:\bin"
    );
}

#[test]
fn test_with_no_inherited_path_the_first_directory_is_the_path() {
    assert_eq!(
        search_path_led_by("linux", "/opt/ctxloom", None),
        "/opt/ctxloom"
    );
    assert_eq!(
        search_path_led_by("windows", r"C:\ctxloom", Some("")),
        r"C:\ctxloom"
    );
}

#[test]
fn test_on_windows_a_directory_holding_the_separator_is_quoted() {
    assert_eq!(
        search_path_led_by("windows", r"C:\a;b", Some(r"C:\bin")),
        r#""C:\a;b";C:\bin"#
    );
    assert_eq!(
        search_path_led_by("windows", r"C:\a;b", None),
        r#""C:\a;b""#
    );
    assert_eq!(
        search_path_led_by("linux", "/a;b", Some("/bin")),
        "/a;b:/bin"
    );
}

#[test]
fn test_the_windows_profile_is_relocated_into_the_session_home_where_the_caller_has_it() {
    let caller: BTreeMap<String, String> = [
        ("USERPROFILE", r"C:\Users\her"),
        ("APPDATA", r"C:\Users\her\AppData\Roaming"),
        ("LOCALAPPDATA", r"C:\Users\her\AppData\Local"),
    ]
    .into_iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    assert_eq!(
        session_profile("/s/home", &caller),
        [
            ("USERPROFILE".to_string(), "/s/home".to_string()),
            ("APPDATA".to_string(), "/s/home/AppData/Roaming".to_string()),
            (
                "LOCALAPPDATA".to_string(),
                "/s/home/AppData/Local".to_string()
            ),
        ]
    );
}

#[test]
fn test_a_caller_without_a_windows_profile_is_given_none() {
    let caller: BTreeMap<String, String> = [("HOME".to_string(), "/home/her".to_string())]
        .into_iter()
        .collect();
    assert!(session_profile("/s/home", &caller).is_empty());
}

#[test]
fn test_no_profile_variable_is_a_startup_one() {
    for (name, _) in WINDOWS_PROFILE_VARIABLES {
        assert!(!WINDOWS_STARTUP_VARIABLES.contains(name), "{name}");
    }
}
