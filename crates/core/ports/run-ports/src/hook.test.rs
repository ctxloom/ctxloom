use std::collections::BTreeMap;

use angzarr_diagnostic::Diagnostic;

use crate::hook::{
    endpoint_path, hook_environment, Bearer, BearerError, EndpointError, RedactedEnv,
    BEARER_VARIABLE, ENDPOINT_VARIABLE, HOOK_VARIABLES, RUN_VARIABLE, SOCKET_PATH_LIMIT,
};
use crate::Token;

const RUN: [u8; 16] = [
    0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x10, 0x11,
];
const RUN_TEXT: &str = "00010203-0405-5607-8aaa-bbccddeeff10";

fn token() -> Token {
    Token::from_bytes([0x5e; 32])
}

// NEW-DESIGN (owner ruling 2026-11-09, item 16): the socket is
// `$XDG_RUNTIME_DIR/ctxloom/<short>.sock`, the short token the first 8 bytes of
// the run's root.
#[test]
fn the_socket_is_under_the_runtime_directory_named_by_the_run() {
    let path = endpoint_path(Some("/run/user/1000"), "/home/u", &RUN).unwrap();
    assert_eq!(path, "/run/user/1000/ctxloom/0001020304050607.sock");
}

// NEW-DESIGN: with no usable runtime directory (unset, empty, or relative,
// which the XDG rules forbid) the socket falls back to a short path under
// ctxloom's home.
#[test]
fn without_a_runtime_directory_the_socket_is_under_ctxloom_home() {
    for unusable in [None, Some(""), Some("run/user")] {
        let path = endpoint_path(unusable, "/home/u", &RUN).unwrap();
        assert_eq!(path, "/home/u/.ctxloom/run/0001020304050607.sock");
    }
}

// NEW-DESIGN: two runs never share a socket (the run's root is unique, and the
// short token is a prefix of it).
#[test]
fn different_runs_have_different_sockets() {
    let mut other = RUN;
    other[0] = 0xff;
    assert_ne!(
        endpoint_path(Some("/run/u"), "/h", &RUN).unwrap(),
        endpoint_path(Some("/run/u"), "/h", &other).unwrap()
    );
}

// NEW-DESIGN (sun_path is 108 bytes with its terminator): a path of exactly the
// limit is taken, one byte more is a typed error that carries the path and the
// limit, whichever way the directory was found.
#[test]
fn a_path_over_the_socket_limit_is_a_typed_error_at_the_boundary() {
    // dir + "/ctxloom/" (9) + 16 digits + ".sock" (5)
    let fits = "/".to_string() + &"d".repeat(SOCKET_PATH_LIMIT - 30 - 1);
    let path = endpoint_path(Some(&fits), "/h", &RUN).unwrap();
    assert_eq!(path.len(), SOCKET_PATH_LIMIT);
    let over = format!("{fits}d");
    let EndpointError::TooLong { path, limit } =
        endpoint_path(Some(&over), "/h", &RUN).unwrap_err()
    else {
        panic!("not too long");
    };
    assert_eq!(path.len(), SOCKET_PATH_LIMIT + 1);
    assert_eq!(limit, SOCKET_PATH_LIMIT as u64);
    let long_home = format!("/{}", "h".repeat(120));
    let error = endpoint_path(None, &long_home, &RUN).unwrap_err();
    assert_eq!(error.code(), "run-ports-endpoint-too-long");
}

// NEW-DESIGN: the diagnostic names the length and the limit.
#[test]
fn the_too_long_error_says_how_long_and_how_long_it_may_be() {
    let error = endpoint_path(Some(&format!("/{}", "d".repeat(100))), "/h", &RUN).unwrap_err();
    let args: BTreeMap<String, String> = error
        .args()
        .into_iter()
        .map(|(name, value)| (name.to_string(), format!("{value:?}")))
        .collect();
    assert!(args.contains_key("length"));
    assert!(args.contains_key("limit"));
    assert!(args.contains_key("path"));
}

#[test]
fn a_run_root_too_short_to_name_a_socket_is_refused() {
    let error = endpoint_path(Some("/r"), "/h", &RUN[..7]).unwrap_err();
    assert_eq!(error, EndpointError::RunTooShort { length: 7 });
    assert_eq!(error.code(), "run-ports-endpoint-run-too-short");
    assert!(endpoint_path(Some("/r"), "/h", &RUN[..8]).is_ok());
}

// NEW-DESIGN: the composite bearer is `<run>.<token hex>` and parses back.
#[test]
fn the_bearer_is_the_run_and_the_token_and_parses_back() {
    let bearer = Bearer::compose(RUN_TEXT, &token());
    assert_eq!(bearer.expose(), format!("{RUN_TEXT}.{}", token().to_text()));
    let (run, parsed) = Bearer::parse(bearer.expose()).unwrap();
    assert_eq!(run, RUN_TEXT);
    assert_eq!(parsed, token());
}

#[test]
fn a_malformed_bearer_is_refused_without_echoing_it() {
    let secret = token().to_text();
    let cases = [
        secret.clone(),
        format!(".{secret}"),
        format!("{RUN_TEXT}.{}", &secret[1..]),
        format!("{RUN_TEXT}.{}", secret.replace('5', "g")),
        format!("{RUN_TEXT}."),
        String::new(),
    ];
    for text in cases {
        let error = Bearer::parse(&text).unwrap_err();
        assert_eq!(
            error,
            BearerError::Malformed {
                length: text.len() as u64
            }
        );
        assert_eq!(error.code(), "run-ports-bearer-malformed");
        assert!(!format!("{error} {error:?}").contains(&secret));
    }
}

// NEW-DESIGN (owner ruling 2026-11-09 item 16 and the "never a log line or
// error message" rule): nothing a person or a log can render holds the secret.
#[test]
fn the_secret_is_in_no_debug_display_or_error_rendering() {
    let secret = token().to_text();
    let bearer = Bearer::compose(RUN_TEXT, &token());
    assert_eq!(format!("{bearer:?}"), "Bearer(redacted)");
    assert!(!format!("{:?}", token()).contains(&secret));
    let env = hook_environment("/r/ctxloom/a.sock", RUN_TEXT, &token());
    let shown = format!("{:?}", RedactedEnv(&env));
    assert!(!shown.contains(&secret), "{shown}");
    assert!(shown.contains(BEARER_VARIABLE));
    assert!(
        shown.contains("/r/ctxloom/a.sock"),
        "the endpoint is not secret"
    );
    for error in [
        format!(
            "{}",
            EndpointError::TooLong {
                path: "/p".into(),
                limit: 107
            }
        ),
        format!("{}", BearerError::Malformed { length: 3 }),
    ] {
        assert!(!error.contains(&secret));
    }
}

// NEW-DESIGN: the three variables the interactive engine is given.
#[test]
fn the_hook_environment_is_the_endpoint_the_bearer_and_the_run() {
    let env = hook_environment("/r/ctxloom/a.sock", RUN_TEXT, &token());
    assert_eq!(env.len(), 3);
    assert_eq!(env[ENDPOINT_VARIABLE], "/r/ctxloom/a.sock");
    assert_eq!(env[RUN_VARIABLE], RUN_TEXT);
    assert_eq!(
        env[BEARER_VARIABLE],
        Bearer::compose(RUN_TEXT, &token()).expose()
    );
    assert_eq!(
        HOOK_VARIABLES,
        ["CTXLOOM_ENDPOINT", "CTXLOOM_BEARER", "CTXLOOM_RUN"]
    );
}

// NEW-DESIGN: no usable runtime directory and no absolute home leaves nowhere to
// put the socket: a typed error, never a relative path.
#[test]
fn with_nowhere_to_put_the_socket_it_is_refused() {
    for home in ["", "relative/home"] {
        let error = endpoint_path(None, home, &RUN).unwrap_err();
        assert_eq!(error, EndpointError::NoDirectory);
        assert_eq!(error.code(), "run-ports-endpoint-no-directory");
    }
}
