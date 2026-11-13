//! Go parity (S1): the same commands on the same fixtures give what Go
//! ctxloom gave.
//!
//! `fixtures/parity/<noun>.tsv` (one file per noun, design §8.4) list the commands; `scripts/go-library-goldens.sh`
//! ran each against Go ctxloom (release/0.7, 860336c4e, which is ca14d6046 or
//! later) in a copy of its fixture, and kept the output as
//! `fixtures/golden/<id>.stdout` and `<id>.exit`. These tests run the same
//! lines against this binary and compare. What consumers read (the VS Code
//! extension, agents, companions) is what is held: the JSON of every list and
//! show, the premise index, the words of the text forms.
//!
//! WHAT IS NOT COMPARED, and why (each a later slice's or an owner's):
//!
//! - ctxloom's built-in bundles (owner rulings 110, 111): Go had none, so
//!   every row runs on the catalog Go had (`Project::on_gos_catalog`); the
//!   built-in origin's own contract is `cli/init.feature`'s.
//! - Go's own companion loadout (the fragment `isolation-axes`, the `ctxloom`
//!   MCP server) is taken out of the goldens (the script says how): its texts
//!   are the owner's (D24) and name MCP tools whose cut list is open.
//! - A dry run's `resolved`, `environment`, `delivery` and `engine_home`
//!   (engine surfaces and isolation: S9 and S11), and the text sections that
//!   print them. Its `profiles`, `fragments`, `context`,
//!   `tokens`, `prompt` and `findings` (D16) are compared.
//! - A row whose Go output carries the retired `llm` vocabulary (the
//!   registry under `llm:`, an agent's or a profile's `llm` key, the `llm`
//!   noun's listing): the engines schema (owner rulings 41-48) removed it
//!   with no alias, so Go's words cannot be the words here. Such a row is
//!   held to its exit code (`carries_retired_llm`); the new schema's output is
//!   asserted by `library_config` and `library_cli`, and the rows of the
//!   retired `llm` noun itself are gone with it.
//! - The premise index that ends an assembled context on demand
//!   (`config.premised`, owner ruling of 2026-11-12): Go never put it there,
//!   so a dry run's context, its token estimate and its text are compared
//!   without it (`without_index`).
//! - The JSON of the catalog verbs (OD-40, design §8.1: Go parity ends for
//!   JSON): bundle, fragment, command, skill, mcp and profile print their
//!   AIP resources (library.proto), not Go's shapes, so such a row is held
//!   to its exit code and to answering JSON at all (`aip_json`); the shapes
//!   are asserted by `library_cli_<noun>`, `library_write_<noun>` and the
//!   features. Their text rows stay Go parity.
//! - A converted noun's JSON shape (OD-40, design §8.1: Go parity ends for
//!   JSON): an agent's and a companion's JSON rows are retired. Where the AIP
//!   resource carries Go's answer whole, that answer is still compared:
//!   `config show`'s `document` (`as_go_printed`) and a dry run's compared
//!   keys, a default proto3 JSON leaves out read as Go's printed default
//!   (`printed`).
//! - Error texts. A failing command's stdout is empty in both and its exit
//!   code is compared; the words are asserted in `library_cli.test.rs` from
//!   the Gherkin. A usage error is exit 20 here (D28, ruled), where Go's
//!   cobra gave 1: `USAGE_REFUSALS`.

use std::collections::BTreeMap;

use serde_json::Value;

use super::library::{fixtures, Project};
use crate::tests::files;

/// Whether `code` is this build's for Go's `go_exit` (rulings 149, 152,
/// 153): Go's 1 was every error, and this build says which it is, failed
/// (10) or declined (20); Go's 2 refused is 20, its 3 fatal findings 21, its
/// 0 is 0.
fn agrees(go_exit: i32, code: i32) -> bool {
    match go_exit {
        1 => code == 10 || code == 20,
        2 => code == 20,
        3 => code == 21,
        other => code == other,
    }
}

/// Rows where Go's exit 1 is clap's usage refusal in this binary: D28 maps
/// those to 20.
const USAGE_REFUSALS: [&str; 2] = ["config-misspelled-subcommand", "run-dry-agent-and-profile"];

/// Rows where Go's exit 1 is the strict decode's refusal of a config (an
/// unknown key, a value the schema breaks): D15 makes it 2 here, with no
/// answer on stdout (engines schema, track 1).
const CONFIG_REFUSALS: [&str; 14] = [
    "cfg-unknownkey-show-json",
    "cfg-unknownkey-bundle-list",
    "cfg-inline-profiles-show-json",
    "cfg-inline-profiles-bundle-list",
    "cfg-bad-agent-key-show-json",
    "cfg-bad-agent-key-bundle-list",
    "cfg-bad-enum-show-json",
    "cfg-bad-enum-bundle-list",
    "cfg-bad-delegation-show-json",
    "cfg-bad-delegation-bundle-list",
    "cfg-native-mcp-show-json",
    "cfg-native-mcp-bundle-list",
    "cfg-retired-turn-cap-show-json",
    "cfg-retired-turn-cap-bundle-list",
];

/// Rows where the owner overrides Go (D15, 2026-11-03): a config whose
/// `schema_version` is newer, older, unstamped or not a number is refused with
/// exit 20, where Go warned and read on (`config_version_refusal_test.go`);
/// and a value the schema breaks where Go took it.
const OWNER_REFUSALS: [&str; 9] = [
    // `--config-set companions=zzz`: a registration is a mapping of names
    // (OD-10), and a string breaks the schema where Go took it.
    "cfg-layers-set-json",
    "cfg-newer-show-json",
    "cfg-newer-bundle-list",
    "cfg-older-show-json",
    "cfg-older-bundle-list",
    "cfg-unstamped-show-json",
    "cfg-unstamped-bundle-list",
    "cfg-notint-show-json",
    "cfg-notint-bundle-list",
];

/// Fixtures every row of which is a refusal (exit 20, no answer) where Go
/// answered: `cfg/v8` holds keys a hard migrate made unknown (the top-level
/// `auth`, engines track 1; an agent's `permissions`, owner ruling 134).
const REFUSED_FIXTURES: [&str; 1] = ["cfg/v8"];

/// Rows where the owner overrides Go (2026-11-12): a skill file's mode is its
/// sidecar's `executable:` declaration, on every OS, never the mode on disk
/// Go read. The `lib` fixture's sidecar declares `scripts/run.sh`, which git
/// checks out 0644, so Go said 0644 and this build says 0755.
const SIDECAR_MODE_ROWS: [&str; 2] = ["skill-show-json", "skill-show-text"];

/// Go's golden for a [`SIDECAR_MODE_ROWS`] row, with the declared script's
/// mode as the sidecar says it.
fn as_declared(id: &str, golden: String) -> String {
    if !SIDECAR_MODE_ROWS.contains(&id) {
        return golden;
    }
    const SCRIPT: &str = "sha256:299001868fb8c02fd431c336c6d058f5558c5dff5b5af5e6fe04b870a6a9cbba";
    golden
        .replace(
            &format!("\"sha256\": \"{SCRIPT}\",\n      \"mode\": \"0644\""),
            &format!("\"sha256\": \"{SCRIPT}\",\n      \"mode\": \"0755\""),
        )
        .replace(&format!("0644  {SCRIPT}"), &format!("0755  {SCRIPT}"))
}

/// Go's golden as this build words it where the owner changed it: the schema
/// stamp (Go writes 8, this build 9; Go's bare-name companions list is read
/// as generation 9's names, OD-10); and Go's boolean
/// `config.use_distilled` as the word that replaced it, `config.content_form`
/// (owner ruling 59: the fixtures say the word, Go's goldens stay Go's).
fn as_changed(golden: &str) -> String {
    golden
        // Owner ruling of 2026-11-12: the premise index's instruction loads
        // what was chosen through the CLI, not Go's MCP tool (T20).
        .replace(GO_LOAD_CLAUSE, LOAD_CLAUSE)
        .replace(
            &GO_LOAD_CLAUSE.replace('\n', "\\n"),
            &LOAD_CLAUSE.replace('\n', "\\n"),
        )
        .replace("\"use_distilled\": true", "\"content_form\": \"distilled\"")
        .replace("\"use_distilled\": false", "\"content_form\": \"original\"")
        .replace("use_distilled: true", "content_form: distilled")
        .replace("use_distilled: false", "content_form: original")
        .replace("schema_version: 8", "schema_version: 9")
        .replace("\"schema_version\": 8", "\"schema_version\": 9")
        // Owner ruling 134: an agent has no engine-neutral `permissions` (Go
        // listed an empty one).
        .replace(",\n    \"permissions\": {}", "")
}

/// The premise index's own first line: on demand (`config.premised`, owner
/// ruling of 2026-11-12) it ends the assembled context, which Go never did
/// (Go served it over MCP alone), so it is taken out of this build's answer
/// before Go's is compared; its own contract is `library_cli`'s and
/// `compose`'s.
const PREMISE_INDEX: &str =
    "# Conditional guidance\n\nSome of this project's guidance applies CONDITIONALLY.";

/// `context` without the premise index that ends it, if one does.
fn without_index(context: &str) -> Option<String> {
    let at = context.rfind(&format!("\n\n---\n\n{PREMISE_INDEX}"))?;
    Some(context[..at].to_string())
}

/// A dry run's JSON without the premise index: the context, and the token
/// estimate made of it.
fn json_without_index(mut got: Value) -> Value {
    let context = got["context"].as_str().and_then(without_index);
    if let Some(context) = context {
        got["tokens"] = Value::from(library::tokens::estimate(&context) as u64);
        got["context"] = Value::String(context);
    }
    got
}

/// A dry run's text without the premise index (the section's `---` and the
/// index up to the next `=== Section ===`), and, where it had one, the
/// context's token estimate in its heading unsaid (the JSON row holds it).
fn text_without_index(text: &str) -> String {
    let lines: Vec<&str> = text.lines().collect();
    let first = PREMISE_INDEX.lines().next().unwrap_or_default();
    let Some(start) = lines.iter().position(|l| *l == first) else {
        return text.to_string();
    };
    let end = lines[start..]
        .iter()
        .position(|l| l.starts_with("=== "))
        .map_or(lines.len(), |n| start + n);
    // Back over the blank lines and the separator before the index.
    let mut cut = start;
    while cut > 0 && (lines[cut - 1].is_empty() || lines[cut - 1] == "---") {
        cut -= 1;
    }
    let mut kept: Vec<String> = lines[..cut].iter().map(ToString::to_string).collect();
    if end < lines.len() {
        kept.push(String::new());
    }
    kept.extend(lines[end..].iter().map(ToString::to_string));
    kept.iter()
        .map(|l| unsay_tokens(l))
        .collect::<Vec<_>>()
        .join("\n")
}

/// `=== Assembled Context (~N tokens) ===` with its count unsaid.
fn unsay_tokens(line: &str) -> String {
    match line.strip_prefix("=== Assembled Context (~") {
        Some(_) => "=== Assembled Context (~? tokens) ===".to_string(),
        None => line.to_string(),
    }
}

/// The premise index instruction's last clause as Go said it, and as the
/// owner reworded it (2026-11-12).
const GO_LOAD_CLAUSE: &str = "Then call the ctxloom\n`assemble_context` tool with the names you chose (the `bundles` argument)\nand follow what it returns.";
const LOAD_CLAUSE: &str =
    "Then run\n`ctxloom fragment show <name>` for each name you chose and follow what it\nreturns.";

/// A dry run's keys that are compared (D16); the rest print later slices'
/// surfaces.
const DRY_RUN_KEYS: [&str; 7] = [
    "agent",
    "runtime",
    "profiles",
    "fragments",
    "context",
    "tokens",
    "prompt",
];

/// Whether Go's `stdout` names the retired `llm` registry or key (engines
/// schema, rulings 41-48: no alias, no shim), so its words cannot be held.
fn carries_retired_llm(golden: &str) -> bool {
    golden.contains("\"llm\"")
        || golden.contains("llm:")
        || golden.contains("LLM:")
        || golden.contains("\"backend\"")
}

/// The nouns whose JSON is their AIP resources (OD-40, B1), not Go's.
const AIP_NOUNS: [&str; 6] = ["bundle", "fragment", "command", "skill", "mcp", "profile"];

/// Whether `row` asks a noun of [`AIP_NOUNS`] for json (`bundle push` and
/// `bundle move` are not converted yet): its stdout is not Go's.
fn aip_json(row: &Row) -> bool {
    let words: Vec<&str> = row
        .args
        .iter()
        .map(String::as_str)
        .filter(|w| !w.starts_with('-') && *w != "json")
        .collect();
    let noun = words.first().copied().unwrap_or_default();
    let verb = words.get(1).copied().unwrap_or_default();
    AIP_NOUNS.contains(&noun) && !(noun == "bundle" && matches!(verb, "push" | "move"))
}

/// A dry run's text sections that print later slices' surfaces.
const DRY_RUN_SECTIONS: [&str; 5] = ["Environment", "Delivery", "Context File", "LLM", "Engine"];

struct Row {
    id: String,
    fixture: String,
    env: Vec<(String, String)>,
    args: Vec<String>,
}

fn rows() -> Vec<Row> {
    let mut tables: Vec<_> = files::children(fixtures().join("parity"))
        .into_iter()
        .filter(|path| path.extension().is_some_and(|e| e == "tsv"))
        .collect();
    tables.sort();
    let text: String = tables
        .iter()
        .map(|path| files::read(path).unwrap_or_else(|e| panic!("{}: {e}", path.display())))
        .collect::<Vec<_>>()
        .join("\n");
    text.lines()
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .map(|line| {
            let mut fields = line.splitn(3, '\t');
            let id = fields.next().expect("an id").to_string();
            let fixture = fields.next().expect("a fixture").to_string();
            let mut env = Vec::new();
            let mut args = Vec::new();
            for word in fields.next().expect("arguments").split_whitespace() {
                match word.split_once('=') {
                    Some((name, value))
                        if args.is_empty()
                            && name.chars().all(|c| c.is_ascii_uppercase() || c == '_') =>
                    {
                        env.push((name.to_string(), value.to_string()));
                    }
                    _ => args.push(word.to_string()),
                }
            }
            Row {
                id,
                fixture,
                env,
                args,
            }
        })
        .collect()
}

fn golden(id: &str, extension: &str) -> String {
    let path = fixtures().join("golden").join(format!("{id}.{extension}"));
    files::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

fn is_dry_run(id: &str) -> bool {
    id.contains("run-dry") || id.starts_with("prof-run")
}

/// The text without the named `=== Section ===` blocks.
fn without_sections(text: &str, names: &[&str]) -> String {
    let mut out = Vec::new();
    let mut skipping = false;
    for line in text.lines() {
        if let Some(name) = line
            .strip_prefix("=== ")
            .and_then(|l| l.strip_suffix(" ==="))
        {
            let name = name.split(" (").next().unwrap_or(name);
            skipping = names.contains(&name);
        }
        if !skipping {
            out.push(line);
        }
    }
    out.join("\n")
}

/// Why `row`'s run differs from Go's, if it does.
fn mismatch(row: &Row, project: &Project) -> Option<String> {
    let args: Vec<&str> = row.args.iter().map(String::as_str).collect();
    let env: Vec<(&str, &str)> = row
        .env
        .iter()
        .map(|(n, v)| (n.as_str(), v.as_str()))
        .collect();
    let ran = project.run_with(&env, &args);

    let go_exit: i32 = golden(&row.id, "exit")
        .trim()
        .parse()
        .expect("an exit code");
    if OWNER_REFUSALS.contains(&row.id.as_str()) || REFUSED_FIXTURES.contains(&row.fixture.as_str())
    {
        // owner override of Go, D15, 2026-11-03
        return (ran.code != 20 || !ran.stdout.trim().is_empty())
            .then(|| format!("want a refusal (exit 20, no answer): {}", ran.said()));
    }
    let refusal =
        USAGE_REFUSALS.contains(&row.id.as_str()) || CONFIG_REFUSALS.contains(&row.id.as_str());
    let agreed = if refusal {
        ran.code == 20
    } else {
        agrees(go_exit, ran.code)
    };
    if !agreed {
        return Some(format!("exit {} (Go {go_exit}): {}", ran.code, ran.said()));
    }
    let want = as_declared(&row.id, as_changed(&golden(&row.id, "stdout")));
    if !is_dry_run(&row.id) && carries_retired_llm(&want) {
        return None;
    }
    let got = project.normalized(&ran.stdout);
    if want.trim().is_empty() {
        return (!got.trim().is_empty()).then(|| format!("stdout should be empty: {got:?}"));
    }
    match serde_json::from_str::<Value>(&want) {
        Ok(_) if aip_json(row) => serde_json::from_str::<Value>(&got)
            .is_err()
            .then(|| format!("stdout is not JSON: {got:?}")),
        Ok(want) => {
            let Ok(got) = serde_json::from_str::<Value>(&got) else {
                return Some(format!("stdout is not JSON: {got:?}"));
            };
            let got = if is_dry_run(&row.id) {
                json_without_index(got)
            } else {
                as_go_printed(row, got)
            };
            json_mismatch(&row.id, &want, &got)
        }
        Err(_) => {
            let (want, got) = if is_dry_run(&row.id) {
                let want = without_sections(&want, &DRY_RUN_SECTIONS);
                let got = without_sections(&got, &DRY_RUN_SECTIONS);
                let unindexed = text_without_index(&got);
                if unindexed == got {
                    (want, got)
                } else {
                    let want = want
                        .lines()
                        .map(unsay_tokens)
                        .collect::<Vec<_>>()
                        .join("\n");
                    (want, unindexed)
                }
            } else {
                (want, got)
            };
            (want.trim_end() != got.trim_end())
                .then(|| format!("text differs\n--- Go\n{want}\n--- here\n{got}"))
        }
    }
}

/// The noun a row runs: its first word that is no flag and no flag's value.
fn noun(row: &Row) -> &str {
    row.args
        .iter()
        .find(|word| !word.starts_with('-') && !word.contains('='))
        .map_or("", String::as_str)
}

/// This build's json as Go printed it, where the AIP resource (OD-40)
/// carries Go's answer whole: `config show` is the `Config` singleton, whose
/// `document` is the configuration Go printed (design §5.8), so that is what
/// is compared. Every other noun's json is compared as it is.
fn as_go_printed(row: &Row, got: Value) -> Value {
    if noun(row) == "config" {
        return got.get("document").cloned().unwrap_or(got);
    }
    got
}

/// A value as proto3 JSON prints it (OD-40: the dry run is the `Run`
/// message): a default (null, false, a zero, an empty text or list) is left
/// out, so Go's printed default and this build's absent field agree, and
/// nothing else does.
fn printed(value: Option<&Value>) -> Option<&Value> {
    value.filter(|value| match value {
        Value::Null => false,
        Value::Bool(b) => *b,
        Value::Number(n) => n.as_f64() != Some(0.0),
        Value::String(s) => !s.is_empty(),
        Value::Array(a) => !a.is_empty(),
        Value::Object(_) => true,
    })
}

fn json_mismatch(id: &str, want: &Value, got: &Value) -> Option<String> {
    if !is_dry_run(id) {
        return (want != got).then(|| format!("JSON differs\n--- Go\n{want:#}\n--- here\n{got:#}"));
    }
    let mut wrong = Vec::new();
    for key in DRY_RUN_KEYS {
        if printed(want.get(key)) != printed(got.get(key)) {
            wrong.push(format!(
                "{key}: Go {:?}, here {:?}",
                want.get(key),
                got.get(key)
            ));
        }
    }
    // Findings: how many, and which are fatal (their words are the
    // catalog's).
    let flags = |doc: &Value| -> Vec<(Value, bool)> {
        doc["findings"]
            .as_array()
            .map(|a| {
                a.iter()
                    .map(|f| (f["kind"].clone(), f["fatal"].as_bool().unwrap_or(false)))
                    .collect()
            })
            .unwrap_or_default()
    };
    if flags(want) != flags(got) {
        wrong.push(format!(
            "findings: Go {:?}, here {:?}",
            flags(want),
            flags(got)
        ));
    }
    (!wrong.is_empty()).then(|| wrong.join("\n"))
}

/// Every row whose fixture starts with `prefix`, against this binary.
fn check(prefix: &str) {
    let mut projects: BTreeMap<String, Project> = BTreeMap::new();
    let mut wrong = Vec::new();
    let mut checked = 0;
    for row in rows().iter().filter(|r| r.fixture.starts_with(prefix)) {
        let project = projects
            .entry(row.fixture.clone())
            .or_insert_with(|| Project::from_fixture(&row.fixture).on_gos_catalog());
        checked += 1;
        if let Some(why) = mismatch(row, project) {
            wrong.push(format!("{}: {why}", row.id));
        }
    }
    assert!(checked > 0, "no row for {prefix}");
    assert!(
        wrong.is_empty(),
        "{} of {checked} differ from Go:\n{}",
        wrong.len(),
        wrong.join("\n\n")
    );
}

// Go's acceptance features for each noun, on the fixture `lib`.
#[test]
fn test_lib_fixture_matches_go() {
    check("lib");
}

// internal/core/composite/assemble_golden_test.go's corpus.
#[test]
fn test_gold_corpus_matches_go() {
    check("gold");
}

// internal/core/bundles/upgrade.go envelopeSteps: generations 0 to 4.
#[test]
fn test_envelope_generations_match_go() {
    check("gens");
}

// fault_tolerance.feature: a malformed bundle is skipped and named.
#[test]
fn test_malformed_bundles_match_go() {
    check("bad");
}

// internal/core/profiles/profiles.go ResolveProfile, composite/select.go.
#[test]
fn test_profile_resolution_matches_go() {
    check("prof");
}

// config.feature, fault_tolerance.feature: schema 8 and older, layering.
#[test]
fn test_configs_match_go() {
    check("cfg/");
}

// empty_listing_hint, cli/agent.feature: nothing there.
#[test]
fn test_empty_projects_match_go() {
    check("empty");
}
