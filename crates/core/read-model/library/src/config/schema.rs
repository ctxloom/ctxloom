//! What a config may say (Go's `resources/schema/input/config-schema.json`):
//! its keys, and the shape of each value. A violation is worded as Go words
//! it, so the same file is refused in the same terms.

use std::sync::OnceLock;

use angzarr_yaml::Value;

use crate::yamlx::nearest;

/// One way the document breaks the schema.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Violation {
    /// A key the schema does not describe: its dotted path, where it would
    /// have been legal (`the top level` or a path), and the keys that are.
    UnknownKey {
        path: String,
        site: String,
        known: Vec<String>,
    },
    /// A value of the wrong type: the key, what it holds (`string`, `number`,
    /// `boolean`, `array`, `object`) and what was expected.
    Holds {
        path: String,
        holds: String,
        expected: String,
    },
    /// A value the schema refuses: the key and why.
    Breaks { path: String, detail: String },
}

impl Violation {
    /// The violation as Go words it, naming `file`.
    pub fn text(&self, file: &str) -> String {
        match self {
            Violation::UnknownKey { path, site, known } => {
                let key = path.rsplit('.').next().unwrap_or(path);
                let refs: Vec<&str> = known.iter().map(String::as_str).collect();
                let mut out = format!("unknown key `{path}` in {file}: ctxloom does not know it");
                let near = nearest(key, &refs);
                if !near.is_empty() {
                    out.push_str(&format!(" — did you mean `{near}`?"));
                }
                if !known.is_empty() {
                    out.push_str(&format!(" (known keys at {site}: {})", known.join(", ")));
                }
                out
            }
            Violation::Holds {
                path,
                holds,
                expected,
            } => format!(
                "{file}: `{}` holds a {holds}, expected {expected}",
                shown(path)
            ),
            Violation::Breaks { path, detail } => {
                format!("{file}: `{}` breaks the schema: {detail}", shown(path))
            }
        }
    }

    /// Whether the value is of the wrong shape for its key, so that reading
    /// the config into its model fails (rather than reading on).
    pub fn is_shape(&self) -> bool {
        matches!(self, Violation::Holds { .. })
    }
}

/// A violation's key as it is shown: the document root has no dotted path.
fn shown(path: &str) -> &str {
    if path.is_empty() {
        "(top level)"
    } else {
        path
    }
}

/// What a string must look like.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Pat {
    Duration,
    Age,
    ListWindow,
    PrefixKey,
    MemoryLimit,
    PidsLimit,
    NoEquals,
}

impl Pat {
    /// The pattern as the schema writes it.
    fn source(self) -> &'static str {
        match self {
            Pat::Duration => "^[0-9]+(\\.[0-9]+)?(ns|us|\u{b5}s|ms|s|m|h)([0-9]+(\\.[0-9]+)?(ns|us|\u{b5}s|ms|s|m|h))*$",
            Pat::Age => "^[0-9]+(d|w|h|m|s)$",
            Pat::ListWindow => "^(all|0|[0-9]+(d|w|h|m|s))$",
            Pat::PrefixKey => "^ctrl[-+].$",
            Pat::MemoryLimit => "^\\s*([0-9]+(\\.[0-9]+)?\\s*([kKmMgGtT]([iI]?[bB])?|[bB])?|[uU][nN][lL][iI][mM][iI][tT][eE][dD])\\s*$",
            Pat::PidsLimit => "^\\s*([0-9]+|[uU][nN][lL][iI][mM][iI][tT][eE][dD])\\s*$",
            Pat::NoEquals => "^[^=]+$",
        }
    }

    fn matches(self, s: &str) -> bool {
        match self {
            Pat::Duration => durations(s, &["ns", "us", "\u{b5}s", "ms", "s", "m", "h"]),
            Pat::Age => age(s),
            Pat::ListWindow => s == "all" || s == "0" || age(s),
            Pat::PrefixKey => {
                let mut chars = s.chars();
                s.starts_with("ctrl")
                    && matches!(s.chars().nth(4), Some('-' | '+'))
                    && s.chars().count() == 6
                    && chars.nth(5).is_some_and(|c| c != '\n')
            }
            Pat::MemoryLimit => memory_limit(s.trim_matches(char::is_whitespace)),
            Pat::PidsLimit => {
                let t = s.trim_matches(char::is_whitespace);
                t.eq_ignore_ascii_case("unlimited")
                    || (!t.is_empty() && t.chars().all(|c| c.is_ascii_digit()))
            }
            Pat::NoEquals => !s.is_empty() && !s.contains('=') && !s.contains('\n'),
        }
    }
}

/// `[0-9]+(d|w|h|m|s)`.
fn age(s: &str) -> bool {
    let digits = s.chars().take_while(char::is_ascii_digit).count();
    digits > 0 && matches!(&s[digits..], "d" | "w" | "h" | "m" | "s")
}

/// One or more `<number><unit>` runs.
fn durations(s: &str, units: &[&str]) -> bool {
    if s.is_empty() {
        return false;
    }
    let digits = s.chars().take_while(char::is_ascii_digit).count();
    if digits == 0 {
        return false;
    }
    let mut rest = &s[digits..];
    if let Some(after_dot) = rest.strip_prefix('.') {
        let fraction = after_dot.chars().take_while(char::is_ascii_digit).count();
        if fraction == 0 {
            return false;
        }
        rest = &after_dot[fraction..];
    }
    units.iter().any(|unit| {
        rest.strip_prefix(unit)
            .is_some_and(|after| after.is_empty() || durations(after, units))
    })
}

/// `[0-9]+(\.[0-9]+)?\s*([kKmMgGtT]([iI]?[bB])?|[bB])?` or `unlimited`.
fn memory_limit(s: &str) -> bool {
    if s.eq_ignore_ascii_case("unlimited") {
        return true;
    }
    let digits = s.chars().take_while(char::is_ascii_digit).count();
    if digits == 0 {
        return false;
    }
    let mut rest = &s[digits..];
    if let Some(after_dot) = rest.strip_prefix('.') {
        let fraction = after_dot.chars().take_while(char::is_ascii_digit).count();
        if fraction == 0 {
            return false;
        }
        rest = &after_dot[fraction..];
    }
    let rest = rest.trim_start();
    let mut chars = rest.chars();
    match chars.next() {
        None => true,
        Some('b' | 'B') => chars.next().is_none(),
        Some('k' | 'K' | 'm' | 'M' | 'g' | 'G' | 't' | 'T') => {
            let tail: String = chars.collect();
            matches!(tail.to_lowercase().as_str(), "" | "b" | "ib")
        }
        Some(_) => false,
    }
}

/// How a mapping treats keys its properties do not list.
#[derive(Clone, Debug)]
enum Extra {
    Deny,
    Any,
    Each(Box<Node>),
}

/// The schema, as a tree of what each position holds.
#[derive(Clone, Debug)]
enum Node {
    Str {
        choices: &'static [&'static str],
        pattern: Option<Pat>,
        min_len: bool,
    },
    Int {
        min: Option<i64>,
    },
    Array {
        items: Box<Node>,
        unique: bool,
    },
    Object {
        props: Vec<(&'static str, Node)>,
        extra: Extra,
    },
    /// Any one of the shapes (the limits: a size text or a number).
    AnyOf(Vec<Node>),
    /// An `engines.<engine>.auth`: a word, or a source of variable names
    /// and/or a file (read by [`AuthSource::parse`], the one reading).
    Auth,
    /// Whatever there is.
    Free,
}

fn string() -> Node {
    Node::Str {
        choices: &[],
        pattern: None,
        min_len: false,
    }
}

fn non_empty() -> Node {
    Node::Str {
        choices: &[],
        pattern: None,
        min_len: true,
    }
}

fn pattern(p: Pat) -> Node {
    Node::Str {
        choices: &[],
        pattern: Some(p),
        min_len: false,
    }
}

fn choice(choices: &'static [&'static str]) -> Node {
    Node::Str {
        choices,
        pattern: None,
        min_len: false,
    }
}

fn int(min: Option<i64>) -> Node {
    Node::Int { min }
}

fn list(items: Node) -> Node {
    Node::Array {
        items: Box::new(items),
        unique: false,
    }
}

fn object(props: Vec<(&'static str, Node)>) -> Node {
    Node::Object {
        props,
        extra: Extra::Deny,
    }
}

fn map_of(each: Node) -> Node {
    Node::Object {
        props: Vec::new(),
        extra: Extra::Each(Box::new(each)),
    }
}

const RUNTIMES: &[&str] = &["host", "container-rootless", "container-rootful"];

/// The engines ctxloom drives: the keys `engines` may name, and what an
/// agent's `engine` may be, in name order. A build without `test-support`
/// (the one that ships) knows only the real engines (ruling 52).
#[cfg(not(feature = "test-support"))]
pub const ENGINES: &[&str] = &["claude-code", "opencode"];

/// The engines ctxloom drives, the mock and the mock-* test engines included:
/// `test-support` compiles them, for the acceptance tests.
#[cfg(feature = "test-support")]
pub const ENGINES: &[&str] = &[
    "claude-code",
    "mock",
    "mock-launch",
    "mock-lossy",
    "mock-noskills",
    "opencode",
];

/// Whose home an engine runs on: a session home of its own, or the user's.
const SESSION_HOMES: &[&str] = &["host", "session"];

/// One `engines.<engine>` block: what its agents share. `model`, `effort` and
/// `permissions` are the agent's, never here.
fn engine_block() -> Node {
    object(vec![
        ("command", non_empty()),
        ("session_home", choice(SESSION_HOMES)),
        ("auth", Node::Auth),
        ("runtime", choice(RUNTIMES)),
        ("env", list(pattern(Pat::NoEquals))),
    ])
}

/// The schema of a config (`resources/schema/input/config-schema.json`).
fn root() -> Node {
    let limit = |p: Pat| Node::AnyOf(vec![pattern(p), int(Some(0))]);
    Node::Object {
        props: vec![
            ("schema_version", int(None)),
            (
                "engines",
                object(ENGINES.iter().map(|e| (*e, engine_block())).collect()),
            ),
            (
                "editor",
                object(vec![("command", string()), ("args", list(string()))]),
            ),
            (
                "config",
                object(vec![
                    // Owner ruling 59: what a fragment delivers, and whose
                    // statusline; words, never booleans.
                    ("content_form", choice(&["distilled", "original"])),
                    // Owner ruling of 2026-11-12: when a fragment with a
                    // premise loads (`on-demand`, the default, or `always`).
                    ("premised", choice(&["on-demand", "always"])),
                    ("statusline", choice(&["ctxloom", "none"])),
                ]),
            ),
            // Rulings 59 and 60: what `run` does with the dependencies at its
            // start (`missing`, the default, installs what is not there).
            (
                "sync",
                object(vec![("on_run", choice(&["none", "missing"]))]),
            ),
            // Owner ruling 162: whether Go ctxloom still runs on this
            // machine, beside this one (`present`, the default) or not
            // (`retired`); the home config's alone (`take_home_only`).
            (
                "legacy",
                object(vec![("go", choice(&["present", "retired"]))]),
            ),
            ("workspace", choice(&["none", "worktree"])),
            ("runtime", choice(RUNTIMES)),
            // Owner ruling 134: no engine-neutral `permissions` (approver,
            // approval_timeout, sandbox, network) at the project level; an
            // engine's permissions are its own document, on the agent.
            (
                "delegation",
                object(vec![
                    ("concurrency", int(Some(1))),
                    ("depth", int(Some(1))),
                    ("idle_timeout", pattern(Pat::Duration)),
                ]),
            ),
            (
                "shell_timeout",
                object(vec![
                    ("default", pattern(Pat::Duration)),
                    ("max", pattern(Pat::Duration)),
                ]),
            ),
            ("isolation_images", map_of(non_empty())),
            ("isolation_base", non_empty()),
            ("isolation_devcontainer_service", non_empty()),
            ("isolation_memory_limit", limit(Pat::MemoryLimit)),
            ("isolation_pids_limit", limit(Pat::PidsLimit)),
            // Ruling 106: where a session's outputs go, relative to the
            // project root unless absolute.
            ("output", object(vec![("dir", non_empty())])),
            ("isolation_engines", list(non_empty())),
            // A registration is the name alone (OD-10); its state a word
            // (OD-9: `enabled`, the default, or `disabled`).
            (
                "companions",
                map_of(object(vec![(
                    "state",
                    choice(&crate::companion::STATE_WORDS),
                )])),
            ),
            ("agents", map_of(binding())),
            ("default_agent", string()),
            (
                "ui",
                object(vec![
                    ("prefix_key", pattern(Pat::PrefixKey)),
                    ("list_window", pattern(Pat::ListWindow)),
                ]),
            ),
            ("session_reap_age", pattern(Pat::Age)),
            ("session_purge_age", pattern(Pat::Age)),
            ("session_transcript_retention", pattern(Pat::Age)),
            // plan supersedes Go: D28, json and text only
            ("non_tty_format", choice(&["json", "text"])),
        ],
        extra: Extra::Deny,
    }
}

/// An agent's settings for one engine (`agents.<name>.engines.<engine>`,
/// owner ruling 57): its model, effort and permissions. The permissions are
/// the engine's own document, copied to it verbatim (owner rulings 135,
/// 137): any document is taken, and nothing inside it is checked.
fn agent_engine_block() -> Node {
    object(vec![
        ("model", non_empty()),
        ("effort", non_empty()),
        (
            "permissions",
            Node::Object {
                props: Vec::new(),
                extra: Extra::Any,
            },
        ),
    ])
}

/// An `agents.<name>` binding. Its engine-neutral settings (profiles,
/// runtime, env, ...) are its own; what is in an
/// engine's vocabulary is per engine, under `engines` (ruling 57).
fn binding() -> Node {
    Node::Object {
        props: vec![
            ("engine", choice(ENGINES)),
            (
                "engines",
                object(ENGINES.iter().map(|e| (*e, agent_engine_block())).collect()),
            ),
            ("profiles", list(non_empty())),
            ("runtime", choice(RUNTIMES)),
            // Owner ruling of 2026-11-13 (superseding 44(b) for this key):
            // the agent's own, the engine block's its default.
            ("session_home", choice(SESSION_HOMES)),
            (
                "may_delegate",
                Node::Array {
                    items: Box::new(non_empty()),
                    unique: true,
                },
            ),
            ("driving", choice(&["conversational", "oneshot"])),
            ("surfaces", map_of(string())),
            ("roots", map_of(string())),
            (
                "env",
                list(Node::Str {
                    choices: &[],
                    pattern: Some(Pat::NoEquals),
                    min_len: false,
                }),
            ),
        ],
        extra: Extra::Deny,
    }
}

fn schema() -> &'static Node {
    static ROOT: OnceLock<Node> = OnceLock::new();
    ROOT.get_or_init(root)
}

/// The type a scalar's text reads as, as the schema's validator names it.
fn type_of_scalar(text: &str) -> &'static str {
    if text.parse::<i64>().is_ok() || text.parse::<f64>().is_ok_and(f64::is_finite) {
        "number"
    } else if text == "true" || text == "false" {
        "boolean"
    } else {
        "string"
    }
}

/// What a value is, as the schema's validator names it.
fn type_of(value: &Value) -> &'static str {
    match value {
        Value::Null => "null",
        Value::Scalar(text) => type_of_scalar(text),
        Value::Seq(_) => "array",
        Value::Map(_) => "object",
    }
}

fn dotted(path: &[String]) -> String {
    path.join(".")
}

fn push(path: &[String], key: &str) -> Vec<String> {
    let mut next = path.to_vec();
    next.push(key.to_string());
    next
}

/// The keys any shape of `node` lists, sorted.
fn keys_of(node: &Node) -> Vec<String> {
    let mut keys: Vec<String> = match node {
        Node::Object { props, .. } => props.iter().map(|(k, _)| (*k).to_string()).collect(),
        _ => Vec::new(),
    };
    keys.sort();
    keys.dedup();
    keys
}

/// The node under `key` of `node`, if the schema has one: a property, or the
/// shape every label of a map takes.
fn child<'a>(node: &'a Node, key: &str) -> Option<std::borrow::Cow<'a, Node>> {
    match node {
        Node::Object { props, extra } => props
            .iter()
            .find(|(k, _)| *k == key)
            .map(|(_, n)| std::borrow::Cow::Borrowed(n))
            .or_else(|| match extra {
                Extra::Each(each) => Some(std::borrow::Cow::Borrowed(each.as_ref())),
                Extra::Any => Some(std::borrow::Cow::Owned(Node::Free)),
                Extra::Deny => None,
            }),
        Node::Array { items, .. } if key.parse::<usize>().is_ok() => {
            Some(std::borrow::Cow::Borrowed(items.as_ref()))
        }
        Node::Free => Some(std::borrow::Cow::Owned(Node::Free)),
        _ => None,
    }
}

/// The node at `path`, if the schema describes it.
fn node_at(path: &[String]) -> Option<std::borrow::Cow<'static, Node>> {
    let mut here = std::borrow::Cow::Borrowed(schema());
    for key in path {
        here = match here {
            std::borrow::Cow::Borrowed(node) => match child(node, key)? {
                std::borrow::Cow::Borrowed(b) => std::borrow::Cow::Borrowed(b),
                std::borrow::Cow::Owned(o) => std::borrow::Cow::Owned(o),
            },
            std::borrow::Cow::Owned(node) => {
                std::borrow::Cow::Owned(child(&node, key)?.into_owned())
            }
        };
    }
    Some(here)
}

/// Every violation of `doc`.
/// The keys only the home config may say (owner ruling 162): `legacy.go`,
/// whether Go ctxloom runs on this machine, which a checked-out repository
/// cannot know.
pub const HOME_ONLY: &[(&str, &str)] = &[("legacy", "go")];

/// Take each of [`HOME_ONLY`] out of a PROJECT layer and return its dotted
/// path, so the loader can refuse it (and `--degraded` never reads it); a
/// section left empty goes too. Nothing else is touched.
pub fn take_home_only(layer: &mut Value) -> Vec<String> {
    let mut taken = Vec::new();
    for (section, key) in HOME_ONLY {
        let Some(block @ Value::Map(_)) = layer.get_mut(section) else {
            continue;
        };
        if block.get(key).is_some() {
            block.remove(key);
            taken.push(format!("{section}.{key}"));
        }
        if block.as_map().is_some_and(<[_]>::is_empty) {
            layer.remove(section);
        }
    }
    taken
}

pub fn validate(doc: &Value) -> Vec<Violation> {
    let mut out = Vec::new();
    check(schema(), doc, &mut Vec::new(), &mut out);
    out
}

/// `value` against `node`, its place `path`.
fn check(node: &Node, value: &Value, path: &mut Vec<String>, out: &mut Vec<Violation>) {
    match node {
        Node::Free => {}
        Node::Auth => check_auth(value, path, out),
        Node::Str {
            choices,
            pattern,
            min_len,
        } => check_str(value, choices, *pattern, *min_len, path, out),
        Node::Int { min } => match value {
            Value::Scalar(text) if text.parse::<i64>().is_ok() => {
                let n: i64 = text.parse().unwrap_or(0);
                if let Some(min) = min.filter(|min| n < *min) {
                    out.push(Violation::Breaks {
                        path: dotted(path),
                        detail: format!("must be >= {min} but found {n}"),
                    });
                }
            }
            other => out.push(holds(path, other, "integer")),
        },
        Node::Array { items, unique } => check_array(items, *unique, value, path, out),
        Node::Object { props, extra } => check_object(node, props, extra, value, path, out),
        Node::AnyOf(alts) => {
            let ok = alts.iter().any(|alt| {
                let mut found = Vec::new();
                check(alt, value, &mut path.clone(), &mut found);
                found.is_empty()
            });
            if !ok {
                out.push(none_of_the_shapes(path));
            }
        }
    }
}

fn holds(path: &[String], value: &Value, expected: &str) -> Violation {
    Violation::Holds {
        path: dotted(path),
        holds: type_of(value).to_string(),
        expected: expected.to_string(),
    }
}

fn none_of_the_shapes(path: &[String]) -> Violation {
    Violation::Breaks {
        path: dotted(path),
        detail: "matches none of the shapes the schema allows here".to_string(),
    }
}

fn check_str(
    value: &Value,
    choices: &[&str],
    pattern: Option<Pat>,
    min_len: bool,
    path: &[String],
    out: &mut Vec<Violation>,
) {
    let Value::Scalar(text) = value else {
        out.push(holds(path, value, "string"));
        return;
    };
    if !choices.is_empty() && !choices.contains(&text.as_str()) {
        let list: Vec<String> = choices.iter().map(|c| format!("{c:?}")).collect();
        out.push(Violation::Breaks {
            path: dotted(path),
            detail: format!("value must be one of {}", list.join(", ")),
        });
    } else if let Some(p) = pattern.filter(|p| !p.matches(text)) {
        out.push(Violation::Breaks {
            path: dotted(path),
            detail: format!("does not match pattern '{}'", p.source()),
        });
    } else if min_len && text.is_empty() {
        out.push(Violation::Breaks {
            path: dotted(path),
            detail: "length must be >= 1, but got 0".to_string(),
        });
    }
}

fn check_array(
    items: &Node,
    unique: bool,
    value: &Value,
    path: &mut Vec<String>,
    out: &mut Vec<Violation>,
) {
    let Value::Seq(entries) = value else {
        out.push(holds(path, value, "array"));
        return;
    };
    for (i, entry) in entries.iter().enumerate() {
        path.push(i.to_string());
        check(items, entry, path, out);
        path.pop();
    }
    if unique {
        for (j, entry) in entries.iter().enumerate() {
            if let Some(i) = entries[..j].iter().position(|earlier| earlier == entry) {
                out.push(Violation::Breaks {
                    path: dotted(path),
                    detail: format!("items at index {i} and {j} are equal"),
                });
                break;
            }
        }
    }
}

fn check_object(
    node: &Node,
    props: &[(&'static str, Node)],
    extra: &Extra,
    value: &Value,
    path: &mut Vec<String>,
    out: &mut Vec<Violation>,
) {
    let Value::Map(entries) = value else {
        out.push(holds(path, value, "object"));
        return;
    };
    for (key, entry) in entries {
        let here = props.iter().find(|(k, _)| k == key).map(|(_, n)| n);
        path.push(key.clone());
        match (here, extra) {
            (Some(prop), _) => check(prop, entry, path, out),
            (None, Extra::Each(each)) => check(each, entry, path, out),
            (None, Extra::Any) => {}
            (None, Extra::Deny) => {
                path.pop();
                out.push(Violation::UnknownKey {
                    path: dotted(&push(path, key)),
                    site: site(path),
                    known: keys_of(node),
                });
                path.push(key.clone());
            }
        }
        path.pop();
    }
}

/// Where a key would have been legal, as the message names it.
fn site(path: &[String]) -> String {
    if path.is_empty() {
        "the top level".to_string()
    } else {
        format!("`{}`", dotted(path))
    }
}

/// An `engines.<engine>.auth`, judged by the one reading of it. A refusal
/// names the key and never the text written there: a secret pasted where a
/// variable name belongs must not be echoed.
fn check_auth(value: &Value, path: &[String], out: &mut Vec<Violation>) {
    use crate::config::engines::{AuthError, AuthSource};
    let Err(why) = AuthSource::parse(value) else {
        return;
    };
    let (key, detail) = match why {
        AuthError::Shape => (
            None,
            "must be `none`, `login`, `token`, or a mapping of `env` and/or `file`",
        ),
        AuthError::UnknownKey { key } => {
            out.push(Violation::UnknownKey {
                path: dotted(&push(path, &key)),
                site: site(path),
                known: vec!["env".to_string(), "file".to_string()],
            });
            return;
        }
        AuthError::Empty => (None, "needs `env` (variable names) or `file`"),
        AuthError::NotAName { index } => {
            let at = push(&push(path, "env"), &index.to_string());
            out.push(Violation::Breaks {
                path: dotted(&at),
                detail: "is not an environment variable NAME (the source names a variable, never holds a credential)".to_string(),
            });
            return;
        }
        AuthError::EmptyFile => (Some("file"), "must not be empty"),
    };
    let at = key.map_or_else(|| path.to_vec(), |k| push(path, k));
    out.push(Violation::Breaks {
        path: dotted(&at),
        detail: detail.to_string(),
    });
}

/// Whether the schema describes `path` (a key, or a free-form label under a
/// map such as `agents.<name>.runtime`).
pub fn known_path(path: &[String]) -> bool {
    node_at(path).is_some()
}

/// Whether `value` is one the schema allows at `path`.
pub fn validate_at(path: &[String], value: &Value) -> Result<(), String> {
    let Some(node) = node_at(path) else {
        return Ok(());
    };
    let mut found = Vec::new();
    check(&node, value, &mut path.to_vec(), &mut found);
    match found.first() {
        None => Ok(()),
        Some(Violation::UnknownKey { path, .. }) => Err(format!("unknown key `{path}`")),
        Some(Violation::Holds {
            holds, expected, ..
        }) => Err(format!("holds a {holds}, expected {expected}")),
        Some(Violation::Breaks { detail, .. }) => Err(detail.clone()),
    }
}

/// The type of the scalars the schema puts at `path`, for output.
pub(crate) fn scalar_kind(path: &[String]) -> ScalarKind {
    match node_at(path).as_deref() {
        Some(Node::Int { .. }) => ScalarKind::Int,
        Some(Node::Free) => ScalarKind::Inferred,
        _ => ScalarKind::Str,
    }
}

/// How a scalar is printed.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum ScalarKind {
    Int,
    Str,
    /// Not in the schema: numbers and booleans read as what they look like.
    Inferred,
}

/// The keys of a mapping at `path` in the order its record lists them, then
/// the rest sorted; `None` for a position the schema lists nothing at.
pub(crate) fn field_order(path: &[String]) -> Vec<String> {
    match node_at(path).as_deref() {
        Some(Node::Object { props, .. }) => props.iter().map(|(k, _)| (*k).to_string()).collect(),
        _ => Vec::new(),
    }
}

/// The top-level keys, in the order a section is named: `config show nonsense`
/// lists them.
pub const SECTIONS: [&str; 24] = [
    "schema_version",
    "engines",
    "editor",
    "config",
    "sync",
    "agents",
    "default_agent",
    "workspace",
    "runtime",
    "delegation",
    "shell_timeout",
    "isolation_images",
    "isolation_base",
    "isolation_devcontainer_service",
    "isolation_engines",
    "isolation_memory_limit",
    "isolation_pids_limit",
    "companions",
    "output",
    "ui",
    "session_reap_age",
    "session_purge_age",
    "session_transcript_retention",
    "non_tty_format",
];

#[cfg(test)]
#[path = "schema.test.rs"]
mod tests;
