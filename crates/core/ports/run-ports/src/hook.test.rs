use angzarr_diagnostic::Diagnostic;

use crate::hook::{
    endpoint_url, hook_environment, parse_endpoint, Bearer, BearerError, Dial, EndpointError,
    RedactedEnv, BEARER_VARIABLE, ENDPOINT_VARIABLE, HOOK_VARIABLES, RUN_VARIABLE,
    TLS_CERT_VARIABLE,
};
use crate::{PublicCert, Token};

fn cert() -> PublicCert {
    PublicCert::from_der(vec![0x30, 0x03, 0x01, 0x02, 0x03]).unwrap()
}

const RUN_TEXT: &str = "00010203-0405-5607-8aaa-bbccddeeff10";

fn token() -> Token {
    Token::from_bytes([0x5e; 32])
}

#[test]
fn the_told_endpoint_is_an_https_url_of_host_and_port() {
    assert_eq!(
        endpoint_url("127.0.0.1", 40123).unwrap(),
        "https://127.0.0.1:40123"
    );
    assert_eq!(
        endpoint_url("host.docker.internal", 7).unwrap(),
        "https://host.docker.internal:7"
    );
    assert_eq!(endpoint_url("::1", 9).unwrap(), "https://[::1]:9");
}

// Port 0 is a request to bind, never a place to dial.
#[test]
fn port_zero_is_no_endpoint() {
    assert_eq!(
        endpoint_url("127.0.0.1", 0).unwrap_err(),
        EndpointError::PortZero {
            endpoint: "https://127.0.0.1:0".to_string()
        }
    );
    assert_eq!(
        endpoint_url("::1", 0).unwrap_err(),
        EndpointError::PortZero {
            endpoint: "https://[::1]:0".to_string()
        }
    );
    for text in ["https://h:0", "http://h:0", "https://[::1]:0"] {
        assert_eq!(
            parse_endpoint(text).unwrap_err(),
            EndpointError::PortZero {
                endpoint: text.to_string()
            },
            "{text}"
        );
    }
    assert!(endpoint_url("h", 1).is_ok());
    assert!(parse_endpoint("https://h:1").is_ok());
}

#[test]
fn a_told_url_parses_back_to_the_dial() {
    for (host, port) in [
        ("127.0.0.1", 40123),
        ("host.containers.internal", 65535),
        ("::1", 1),
    ] {
        let url = endpoint_url(host, port).unwrap();
        let dial = parse_endpoint(&url).unwrap();
        assert_eq!(
            dial,
            Dial {
                host: host.to_string(),
                port
            }
        );
        assert_eq!(dial.url(), url);
    }
}

// No downgrade: a plaintext endpoint is a typed refusal, not a dial.
#[test]
fn a_plaintext_endpoint_is_refused() {
    for text in [
        "http://127.0.0.1:9",
        "http://[::1]:9",
        "http://h.example:65535",
    ] {
        let error = parse_endpoint(text).unwrap_err();
        assert_eq!(
            error,
            EndpointError::Plaintext {
                endpoint: text.to_string()
            },
            "{text}"
        );
        assert_eq!(error.code(), "run-ports-endpoint-plaintext");
    }
    assert_eq!(
        parse_endpoint("https://127.0.0.1:9").unwrap().url(),
        "https://127.0.0.1:9"
    );
}

#[test]
fn what_is_not_a_listener_url_is_refused() {
    for text in [
        "",
        "127.0.0.1:9",
        "http://",
        "http://:9",
        "http://h",
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
    let env = hook_environment("https://127.0.0.1:9", RUN_TEXT, &token(), &cert());
    let shown = format!("{:?}", RedactedEnv(&env));
    assert!(!shown.contains(&secret), "{shown}");
    assert!(shown.contains(BEARER_VARIABLE));
    // Every value is withheld, public or not: the names alone render.
    assert!(!shown.contains("https://127.0.0.1:9"), "{shown}");
    assert!(!shown.contains(&cert().to_env_value()), "{shown}");
    assert!(shown.contains(ENDPOINT_VARIABLE) && shown.contains(TLS_CERT_VARIABLE));
    assert!(!BearerError::Malformed { length: 3 }
        .to_string()
        .contains(&secret));
}

// The four variables the interactive engine is given.
#[test]
fn the_hook_environment_is_the_endpoint_the_bearer_the_run_and_the_certificate() {
    let env = hook_environment("https://127.0.0.1:9", RUN_TEXT, &token(), &cert());
    assert_eq!(env.len(), 4);
    assert_eq!(env[ENDPOINT_VARIABLE], "https://127.0.0.1:9");
    assert_eq!(env[RUN_VARIABLE], RUN_TEXT);
    assert_eq!(
        env[BEARER_VARIABLE],
        Bearer::compose(RUN_TEXT, &token()).expose()
    );
    assert_eq!(env[TLS_CERT_VARIABLE], cert().to_env_value());
    assert_eq!(TLS_CERT_VARIABLE, "CTXLOOM_TLS_CERT");
    assert_eq!(
        HOOK_VARIABLES,
        [
            "CTXLOOM_ENDPOINT",
            "CTXLOOM_BEARER",
            "CTXLOOM_RUN",
            "CTXLOOM_TLS_CERT"
        ]
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
    assert_eq!(
        EndpointError::Plaintext {
            endpoint: "x".to_string()
        }
        .to_string(),
        "run-ports-endpoint-plaintext endpoint=x"
    );
    assert_eq!(
        EndpointError::PortZero {
            endpoint: "x".to_string()
        }
        .to_string(),
        "run-ports-endpoint-port-zero endpoint=x"
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

// An empty scheme and a scheme with an illegal character are malformed, not an
// unsupported scheme; only a well-formed other scheme is `Scheme`.
#[test]
fn an_empty_or_illegal_scheme_is_malformed_and_a_legal_other_one_is_a_scheme_error() {
    for text in [
        "://h:9",
        "ht tp://h:9",
        "ht_tp://h:9",
        "ht/tp://h:9",
        "h\ttp://h:9",
    ] {
        assert_eq!(
            parse_endpoint(text).unwrap_err(),
            EndpointError::Malformed {
                endpoint: text.to_string()
            },
            "{text:?}"
        );
    }
    for scheme in ["ftp", "ws+x", "a-b", "a.b", "h2"] {
        assert_eq!(
            parse_endpoint(&format!("{scheme}://h:9")).unwrap_err(),
            EndpointError::Scheme {
                scheme: scheme.to_string()
            },
            "{scheme}"
        );
    }
}

// Review finding 6 (ruling 87): a hook addresses its facts to the session the
// run names, told in the engine's environment like the run itself, and never
// read from a file in the session's directory. Always the run's own value.
#[test]
fn the_runs_session_is_one_of_the_variables_a_hook_is_given() {
    assert_eq!(crate::SESSION_VARIABLE, "CTXLOOM_SESSION");
    assert!(
        HOOK_VARIABLES.contains(&crate::SESSION_VARIABLE),
        "{HOOK_VARIABLES:?}"
    );
}
