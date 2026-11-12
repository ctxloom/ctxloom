//! Reading the engine's settings files (JSON): which hook entries are
//! ctxloom's, which of them run a subcommand this ctxloom lacks, and which
//! MCP entry launches a server that does not exist.

use serde_json::Value;

/// What this ctxloom writes and takes: `hook <engine> execute <Event>` (the
/// owner's ruling, 2026-11-08), exactly. Any other `ctxloom hook ...` entry
/// (`stamp-plan`, `session-start`, `hud`, `inject-context`, ...) is Go-era
/// (owner ruling 162): a live hook of a Go ctxloom still installed, or a
/// leftover once it is retired. The rule is the form, not a list of names.
const LIVE_VERB: &str = "execute";

/// A Go-era hook entry: one running `ctxloom hook` in any form but the
/// Rust-era `hook <engine> execute <Event>`.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Stale {
    /// The engine's event (`SessionStart`).
    pub event: String,
    /// The subcommand (`inject-context`).
    pub sub: String,
}

/// A command line's words: whitespace separates, quotes group and vanish.
fn words(command: &str) -> Vec<String> {
    let mut words = Vec::new();
    let (mut word, mut quote, mut any) = (String::new(), None, false);
    for c in command.chars() {
        match (quote, c) {
            (Some(q), c) if c == q => quote = None,
            (Some(_), c) => word.push(c),
            (None, '\'' | '"') => {
                quote = Some(c);
                any = true;
            }
            (None, c) if c.is_whitespace() => {
                if any {
                    words.push(std::mem::take(&mut word));
                    any = false;
                }
            }
            (None, c) => {
                word.push(c);
                any = true;
            }
        }
    }
    if any {
        words.push(word);
    }
    words
}

/// Whether `program` is ctxloom itself, by its file name.
fn is_ctxloom(program: &str) -> bool {
    let name = program.rsplit(['/', '\\']).next().unwrap_or_default();
    name == "ctxloom" || name == "ctxloom.exe"
}

/// `ctxloom hook <sub> ...`: the subcommand and whether the words are
/// exactly the Rust-era `hook <engine> execute <Event>`, when they run one.
fn hook_sub(words: &[String]) -> Option<(String, bool)> {
    match words {
        [program, hook, sub, rest @ ..]
            if is_ctxloom(program) && hook == "hook" && !sub.starts_with('-') =>
        {
            let rust_era = matches!(rest, [verb, event]
                if verb == LIVE_VERB && !event.is_empty() && !event.starts_with('-'));
            Some((sub.clone(), rust_era))
        }
        _ => None,
    }
}

/// A hook's words: its `command` line, then its `args` (claude runs
/// `command` with `args` when they are given, as ctxloom's surface writes it).
fn hook_words(hook: &Value) -> Vec<String> {
    let mut found = hook
        .get("command")
        .and_then(Value::as_str)
        .map(words)
        .unwrap_or_default();
    found.extend(
        hook.get("args")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .map(str::to_string),
    );
    found
}

fn parse(text: &str) -> Option<Value> {
    serde_json::from_str(text).ok()
}

/// The `(event, words)` of every hook entry in a settings document.
fn entries(doc: &Value) -> Vec<(String, Vec<String>)> {
    let mut found = Vec::new();
    let Some(events) = doc.get("hooks").and_then(Value::as_object) else {
        return found;
    };
    for (event, groups) in events {
        for group in groups.as_array().into_iter().flatten() {
            for hook in group
                .get("hooks")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
            {
                if hook.get("command").and_then(Value::as_str).is_some() {
                    found.push((event.clone(), hook_words(hook)));
                }
            }
        }
    }
    found
}

fn go_era_of(event: &str, words: &[String]) -> Option<Stale> {
    let (sub, rust_era) = hook_sub(words)?;
    (!rust_era).then(|| Stale {
        event: event.to_string(),
        sub,
    })
}

/// Whether the settings register any ctxloom hook.
pub fn registered(text: &str) -> bool {
    parse(text).is_some_and(|doc| entries(&doc).iter().any(|(_, w)| hook_sub(w).is_some()))
}

/// The Go-era entries of a settings file.
pub fn go_era(text: &str) -> Vec<Stale> {
    parse(text)
        .map(|doc| {
            entries(&doc)
                .iter()
                .filter_map(|(event, words)| go_era_of(event, words))
                .collect()
        })
        .unwrap_or_default()
}

/// The settings with the Go-era entries gone and nothing else touched (a
/// group left with no hook goes, and an event left with no group), and which
/// went; none where there is none. The caller strips only once Go ctxloom
/// is retired (`legacy.go`).
pub fn strip(text: &str) -> Option<(String, Vec<Stale>)> {
    let mut doc = parse(text)?;
    let mut gone = Vec::new();
    let events = doc.get_mut("hooks")?.as_object_mut()?;
    for (event, groups) in events.iter_mut() {
        let Some(groups) = groups.as_array_mut() else {
            continue;
        };
        for group in groups.iter_mut() {
            let Some(hooks) = group.get_mut("hooks").and_then(Value::as_array_mut) else {
                continue;
            };
            hooks.retain(|hook| {
                if hook.get("command").and_then(Value::as_str).is_none() {
                    return true;
                }
                match go_era_of(event, &hook_words(hook)) {
                    Some(stale) => {
                        gone.push(stale);
                        false
                    }
                    None => true,
                }
            });
        }
        groups.retain(|group| {
            group
                .get("hooks")
                .and_then(Value::as_array)
                .is_none_or(|hooks| !hooks.is_empty())
        });
    }
    events.retain(|_, groups| groups.as_array().is_none_or(|groups| !groups.is_empty()));
    if gone.is_empty() {
        return None;
    }
    let mut out = serde_json::to_string_pretty(&doc).ok()?;
    out.push('\n');
    Some((out, gone))
}

/// Whether a settings document's `mcpServers.ctxloom` launches a process
/// (`command`, no `url`) running `ctxloom mcp ...`.
pub fn launches_ctxloom_mcp(text: &str) -> bool {
    let Some(doc) = parse(text) else { return false };
    let Some(entry) = doc.get("mcpServers").and_then(|s| s.get("ctxloom")) else {
        return false;
    };
    if entry.get("url").is_some() {
        return false;
    }
    let Some(command) = entry.get("command").and_then(Value::as_str) else {
        return false;
    };
    let first_arg = entry
        .get("args")
        .and_then(Value::as_array)
        .and_then(|args| args.first())
        .and_then(Value::as_str);
    is_ctxloom(command) && first_arg == Some("mcp")
}
