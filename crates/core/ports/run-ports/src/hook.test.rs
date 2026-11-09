use angzarr_diagnostic::Diagnostic;

use crate::hook::{
    endpoint_url, hook_environment, parse_endpoint, Bearer, BearerError, Dial, EndpointError,
    RedactedEnv, BEARER_VARIABLE, ENDPOINT_VARIABLE, HOOK_VARIABLES, RUN_VARIABLE,
};
use crate::Token;

const RUN_TEXT: &str = "00010203-0405-5607-8aaa-bbccddeeff10";

fn token() -> Token {
    Token::from_bytes([0x5e; 32])
}

#[test]
fn the_told_endpoint_is_an_http_url_of_host_and_port() {
    assert_eq!(endpoint_url("127.0.0.1", 40123), "http://127.0.0.1:40123");
    assert_eq!(
        endpoint_url("host.docker.internal", 7),
        "http://host.docker.internal:7"
    );
    assert_eq!(endpoint_url("::1", 9), "http://[::1]:9");
}

#[test]
fn a_told_url_parses_back_to_the_dial() {
    for (host, port) in [
        ("127.0.0.1", 40123),
        ("host.containers.internal", 65535),
        ("::1", 1),
    ] {
        let dial = parse_endpoint(&endpoint_url(host, port)).unwrap();
        assert_eq!(
            dial,
            Dial {
                secure: false,
                host: host.to_string(),
                port
            }
        );
        assert_eq!(dial.url(), endpoint_url(host, port));
    }
}

#[test]
fn the_scheme_drives_the_dial_so_tls_is_additive() {
    let dial = parse_endpoint("https://127.0.0.1:9").unwrap();
    assert!(dial.secure);
    assert_eq!(dial.url(), "https://127.0.0.1:9");
}

#[test]
fn what_is_not_a_listener_url_is_refused() {
    for text in [
        "",
        "127.0.0.1:9",
        "http://",
        "http://:9",
        "http://h",
        "http://h:0",
        "http://h:65536",
        "http://h:x",
        "http://h:9/path",
        "http://u@h:9",
        "http://[::1:9",
        "/run/user/1000/ctxloom/a.sock",
    ] {
        assert_eq!(
            parse_endpoint(text).unwrap_err(),
            EndpointError::Malformed {
                endpoint: text.to_string()
            },
            "{text}"
        );
    }
    assert_eq!(
        parse_endpoint("ftp://h:9").unwrap_err(),
        EndpointError::Scheme {
            scheme: "ftp".to_string()
        }
    );
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
    let env = hook_environment("http://127.0.0.1:9", RUN_TEXT, &token());
    let shown = format!("{:?}", RedactedEnv(&env));
    assert!(!shown.contains(&secret), "{shown}");
    assert!(shown.contains(BEARER_VARIABLE));
    assert!(
        shown.contains("http://127.0.0.1:9"),
        "the endpoint is not secret"
    );
    assert!(!BearerError::Malformed { length: 3 }
        .to_string()
        .contains(&secret));
}

// NEW-DESIGN: the three variables the interactive engine is given.
#[test]
fn the_hook_environment_is_the_endpoint_the_bearer_and_the_run() {
    let env = hook_environment("http://127.0.0.1:9", RUN_TEXT, &token());
    assert_eq!(env.len(), 3);
    assert_eq!(env[ENDPOINT_VARIABLE], "http://127.0.0.1:9");
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

// The exact text of each error (code, then its args).
#[test]
fn the_endpoint_errors_display_and_report_their_args_exactly() {
    let malformed = EndpointError::Malformed {
        endpoint: "x".to_string(),
    };
    assert_eq!(
        malformed.to_string(),
        "run-ports-endpoint-malformed endpoint=x"
    );
    assert_eq!(malformed.args().len(), 1);
    assert_eq!(
        EndpointError::Scheme {
            scheme: "ftp".to_string()
        }
        .to_string(),
        "run-ports-endpoint-scheme scheme=ftp"
    );
}

#[test]
fn the_bearer_error_displays_and_reports_its_length_exactly() {
    let error = BearerError::Malformed { length: 7 };
    assert_eq!(error.to_string(), "run-ports-bearer-malformed length=7");
    let args = error.args();
    assert_eq!(args.len(), 1);
    assert_eq!(args[0].0, "length");
}
