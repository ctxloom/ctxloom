//! The text forms: what each command prints for a person (Go's renderers,
//! line for line). Every value a bundle's author wrote goes through
//! [`inert`](crate::inert) first.

use std::io::{self, Write};

use angzarr_presentation::text;
use library_application::{
    AgentRow, AgentSettings, AgentShow, BundleList, BundleRow, BundleShow, BundleView,
    CompanionRow, DryRun, EngineRow, EnvSetting, ItemList, ItemShow, McpList, McpShow, Premises,
    ProfileList, ProfileShow, RemoteFound, RuntimeSetting, SearchResults, SkillList, SkillShow,
    SourcedValue,
};

use crate::inert::{body, field};

/// Text that stands for a body, and the notices that went with it.
pub struct Notes(pub Vec<String>);

fn f(s: &str) -> String {
    field(s, 256)
}

/// `bundle list`.
pub fn bundle_list(out: &mut dyn Write, list: &BundleList) -> io::Result<()> {
    if list.no_root {
        if list.hint.starts_with("No project here") {
            return writeln!(out, "{}", list.hint);
        }
        return writeln!(
            out,
            "No bundles directory found. Create one with: mkdir -p .ctxloom/content/bundles"
        );
    }
    if list.rows.is_empty() {
        writeln!(out, "No bundles installed.")?;
        return writeln!(out, "{}", list.hint);
    }
    writeln!(out, "Installed bundles ({}):\n", list.rows.len())?;
    for row in &list.rows {
        bundle_entry(out, row)?;
    }
    Ok(())
}

fn contents(row: &BundleRow) -> Vec<String> {
    let mut parts = Vec::new();
    for (n, singular, plural) in [
        (row.fragment_count, "fragment", "fragments"),
        (row.command_count, "command", "commands"),
        (row.mcp_count, "MCP server", "MCP servers"),
    ] {
        match n {
            0 => {}
            1 => parts.push(format!("1 {singular}")),
            n => parts.push(format!("{n} {plural}")),
        }
    }
    parts
}

fn bundle_entry(out: &mut dyn Write, row: &BundleRow) -> io::Result<()> {
    write!(out, "  {}", f(&row.name))?;
    if !row.version.is_empty() {
        write!(out, " (v{})", f(&row.version))?;
    }
    if row.held {
        write!(out, " [held]")?;
    }
    if !row.origin.is_empty() {
        write!(out, " [built-in]")?;
    }
    writeln!(out)?;
    if !row.description.is_empty() {
        writeln!(out, "    {}", f(&row.description))?;
    }
    let parts = contents(row);
    if !parts.is_empty() {
        writeln!(out, "    Contains: {}", parts.join(", "))?;
    }
    if !row.tags.is_empty() {
        writeln!(out, "    Tags: {}", f(&row.tags.join(", ")))?;
    }
    writeln!(out)
}

/// A body behind `indent`, its alteration noted in `notes`.
fn publisher_body(
    out: &mut dyn Write,
    notes: &mut Notes,
    indent: &str,
    collapse: bool,
    reference: &str,
    content: &str,
) -> io::Result<()> {
    let rendered = body(indent, "", collapse, reference, content);
    if !rendered.notice.is_empty() {
        notes.0.push(rendered.notice);
    }
    write!(out, "{}", rendered.text)
}

/// `bundle show`.
pub fn bundle_show(out: &mut dyn Write, notes: &mut Notes, show: &BundleShow) -> io::Result<()> {
    writeln!(out, "Bundle: {}", f(&show.name))?;
    if !show.version.is_empty() {
        writeln!(out, "Version: {}", f(&show.version))?;
    }
    if !show.author.is_empty() {
        writeln!(out, "Author: {}", f(&show.author))?;
    }
    if !show.description.is_empty() {
        writeln!(out, "Description: {}", f(&show.description))?;
    }
    if !show.tags.is_empty() {
        writeln!(out, "Tags: {}", f(&show.tags.join(", ")))?;
    }
    writeln!(out, "Path: {}\n", show.path)?;
    if !show.mcp.is_empty() {
        writeln!(out, "MCP Servers ({}):", show.mcp.len())?;
        for (name, mcp) in &show.mcp {
            writeln!(out, "  - {}", f(name))?;
            writeln!(out, "      Command: {}", f(&mcp.command))?;
            if !mcp.args.is_empty() {
                writeln!(out, "      Args: {}", f(&mcp.args.join(" ")))?;
            }
            if !mcp.env.is_empty() {
                writeln!(out, "      Env:")?;
                for (key, value) in &mcp.env {
                    writeln!(out, "        {}={}", f(key), f(value))?;
                }
            }
            if !mcp.notes.is_empty() {
                writeln!(out, "      Notes:")?;
                publisher_body(
                    out,
                    notes,
                    "        ",
                    true,
                    &format!("{name} notes"),
                    &mcp.notes,
                )?;
            }
            if !mcp.installation.is_empty() {
                writeln!(out, "      Installation:")?;
                publisher_body(
                    out,
                    notes,
                    "        ",
                    true,
                    &format!("{name} installation"),
                    &mcp.installation,
                )?;
            }
        }
        writeln!(out)?;
    }
    if !show.fragments.is_empty() {
        writeln!(out, "Fragments ({}):", show.fragments.len())?;
        for (name, fragment) in &show.fragments {
            item_line(
                out,
                name,
                &fragment.tags,
                fragment.distilled,
                fragment.no_distill,
            )?;
            writeln!(out, "      {}", f(&fragment.preview))?;
        }
        writeln!(out)?;
    }
    if !show.commands.is_empty() {
        writeln!(out, "Commands ({}):", show.commands.len())?;
        for (name, command) in &show.commands {
            item_line(
                out,
                name,
                &command.tags,
                command.distilled,
                command.no_distill,
            )?;
            if !command.description.is_empty() {
                writeln!(out, "      {}", f(&command.description))?;
            }
        }
        writeln!(out)?;
    }
    if !show.notes.is_empty() {
        writeln!(out, "Notes:")?;
        publisher_body(
            out,
            notes,
            "  ",
            true,
            &format!("{} notes", show.name),
            &show.notes,
        )?;
    }
    Ok(())
}

fn item_line(
    out: &mut dyn Write,
    name: &str,
    tags: &[String],
    distilled: bool,
    no_distill: bool,
) -> io::Result<()> {
    write!(out, "  - {}", f(name))?;
    if !tags.is_empty() {
        write!(out, " [{}]", f(&tags.join(", ")))?;
    }
    if distilled {
        write!(out, " (distilled)")?;
    } else if no_distill {
        write!(out, " (no_distill)")?;
    }
    writeln!(out)
}

/// `bundle view`.
pub fn bundle_view(
    out: &mut dyn Write,
    notes: &mut Notes,
    view: &BundleView,
    ask: &str,
) -> io::Result<()> {
    publisher_body(out, notes, "", view.item, ask, &view.content)
}

/// `fragment list` and `command list`.
pub fn item_list(out: &mut dyn Write, list: &ItemList) -> io::Result<()> {
    let kind = &list.kind;
    if list.rows.is_empty() {
        if !list.bundle_filter.is_empty() {
            return writeln!(
                out,
                "No {kind}s in bundle {:?} ({} {kind}s exist in other bundles).",
                list.bundle_filter, list.total
            );
        }
        writeln!(out, "No {kind}s found.")?;
        return writeln!(out, "{}", list.hint);
    }
    let title = format!("{}{}s", kind[..1].to_uppercase(), &kind[1..]);
    writeln!(out, "{title} ({}):\n", list.rows.len())?;
    let mut current = "";
    for row in &list.rows {
        if row.bundle != current {
            if !current.is_empty() {
                writeln!(out)?;
            }
            writeln!(out, "  {}:", f(&row.bundle_label))?;
            current = &row.bundle;
        }
        write!(out, "    - {}", f(&row.name))?;
        if !row.tags.is_empty() {
            write!(out, " [{}]", f(&row.tags.join(", ")))?;
        }
        writeln!(out)?;
    }
    Ok(())
}

/// `fragment show` and `command show`.
pub fn item_show(out: &mut dyn Write, notes: &mut Notes, show: &ItemShow) -> io::Result<()> {
    if show.distilled {
        writeln!(out, "# (distilled version)")?;
    }
    writeln!(out, "{}\n", f(&show.name))?;
    publisher_body(out, notes, "", true, &show.reference, &show.content)
}

/// `fragment premises`.
pub fn premises(out: &mut dyn Write, premises: &Premises) -> io::Result<()> {
    if premises.fragments.is_empty() {
        return writeln!(out, "{}", premises.instruction);
    }
    write!(out, "{}", premises.instruction)?;
    for row in &premises.fragments {
        write!(out, "\n- {}: {}", row.name, row.premise)?;
    }
    writeln!(out)
}

/// `skill list`.
pub fn skill_list(out: &mut dyn Write, list: &SkillList) -> io::Result<()> {
    if list.rows.is_empty() {
        if !list.bundle_filter.is_empty() && list.total > 0 {
            writeln!(out, "No skills found in bundle {:?}.", list.bundle_filter)?;
            return writeln!(
                out,
                "Skills exist in other bundles \u{2014} run `ctxloom skill list` without --bundle to see them all."
            );
        }
        writeln!(out, "No skills found.")?;
        return writeln!(out, "Create one with: ctxloom skill create <bundle> <name>");
    }
    writeln!(out, "Skills ({}):\n", list.rows.len())?;
    let mut current = "";
    for row in &list.rows {
        if row.source != current {
            if !current.is_empty() {
                writeln!(out)?;
            }
            writeln!(out, "  {}:", f(&row.source))?;
            current = &row.source;
        }
        write!(out, "    - {}", f(&row.name))?;
        if !row.description.is_empty() {
            write!(out, ": {}", f(&row.description))?;
        }
        write!(out, " [{} file(s)]", row.file_count)?;
        if !row.tags.is_empty() {
            write!(out, " ({})", f(&row.tags.join(", ")))?;
        }
        writeln!(out)?;
    }
    Ok(())
}

/// `skill show`.
pub fn skill_show(out: &mut dyn Write, notes: &mut Notes, show: &SkillShow) -> io::Result<()> {
    writeln!(out, "{}#skills/{}\n", f(&show.bundle), f(&show.name))?;
    writeln!(out, "description: {}", f(&show.description))?;
    if !show.license.is_empty() {
        writeln!(out, "license: {}", f(&show.license))?;
    }
    if !show.compatibility.is_empty() {
        writeln!(out, "compatibility: {}", f(&show.compatibility))?;
    }
    if !show.allowed_tools.is_empty() {
        writeln!(out, "allowed-tools: {}", f(&show.allowed_tools.join(", ")))?;
    }
    writeln!(out)?;
    let reference = format!("{}#skills/{}", show.bundle, show.name);
    publisher_body(out, notes, "", true, &reference, &show.body)?;
    writeln!(out, "\nFiles ({}):", show.files.len())?;
    for file in &show.files {
        writeln!(out, "  {}  {}  {}", file.mode, file.sha256, f(&file.path))?;
    }
    Ok(())
}

/// `profile list`.
pub fn profile_list(out: &mut dyn Write, list: &ProfileList) -> io::Result<()> {
    if list.rows.is_empty() {
        writeln!(out, "No profiles defined.")?;
        return writeln!(out, "{}", list.hint);
    }
    writeln!(out, "Profiles ({}):", list.rows.len())?;
    for row in &list.rows {
        write!(out, "  {}", row.name)?;
        if row.default {
            write!(out, " (default)")?;
        }
        writeln!(out)?;
        if !row.description.is_empty() {
            writeln!(out, "    {}", row.description)?;
        }
        let mut parts = Vec::new();
        if !row.bundle.is_empty() {
            parts.push(format!("from bundle: {}", row.bundle));
        }
        if !row.parents.is_empty() {
            parts.push(format!("parents: {}", row.parents.join(", ")));
        }
        if !row.bundles.is_empty() {
            parts.push(format!("{} bundles", row.bundles.len()));
        }
        if !parts.is_empty() {
            writeln!(out, "    {}", parts.join(", "))?;
        }
    }
    Ok(())
}

fn bullets(out: &mut dyn Write, heading: &str, items: &[String]) -> io::Result<()> {
    if items.is_empty() {
        return Ok(());
    }
    writeln!(out, "{heading}:")?;
    for item in items {
        writeln!(out, "  - {}", f(item))?;
    }
    Ok(())
}

/// `profile show`.
pub fn profile_show(out: &mut dyn Write, show: &ProfileShow) -> io::Result<()> {
    writeln!(out, "Profile: {}", f(&show.name))?;
    writeln!(out, "Path: {}", show.path)?;
    if !show.bundle.is_empty() {
        writeln!(out, "Bundle: {}", f(&show.bundle))?;
    }
    if show.default {
        writeln!(out, "Default: yes")?;
    }
    if !show.description.is_empty() {
        writeln!(out, "Description: {}", f(&show.description))?;
    }
    bullets(out, "Parents", &show.parents)?;
    bullets(out, "Bundles", &show.bundles)?;
    bullets(out, "Tags", &show.tags)?;
    if !show.variables.is_empty() {
        writeln!(out, "Variables:")?;
        for (key, value) in &show.variables {
            writeln!(out, "  {}: {}", f(key), f(value))?;
        }
    }
    bullets(out, "Excluded fragments", &show.exclude_fragments)?;
    bullets(out, "Excluded MCP servers", &show.exclude_mcp)
}

/// `agent list`.
pub fn agent_list(out: &mut dyn Write, rows: &[AgentRow]) -> io::Result<()> {
    if rows.is_empty() {
        writeln!(out, "No agents defined.")?;
        return writeln!(out, "Define one under 'agents:' in .ctxloom/config.yaml.");
    }
    writeln!(out, "Agents ({}):", rows.len())?;
    for row in rows {
        write!(out, "  {}", row.name)?;
        if row.engine.is_empty() {
            write!(out, " (engine: default)")?;
        } else {
            write!(out, " (engine: {})", row.engine)?;
        }
        writeln!(out)?;
        if !row.profiles.is_empty() {
            writeln!(out, "    profiles: {}", row.profiles.join(", "))?;
        }
        for (key, value) in [
            ("runtime", &row.runtime),
            ("permissions", &row.permissions_line),
            ("driving", &row.driving),
        ] {
            if !value.is_empty() {
                writeln!(out, "    {key}: {value}")?;
            }
        }
        // Ruling 67: each engine block as the config holds it.
        for line in &row.engine_lines {
            writeln!(out, "    {line}")?;
        }
    }
    Ok(())
}

fn plain_bullets(out: &mut dyn Write, heading: &str, items: &[String]) -> io::Result<()> {
    if items.is_empty() {
        return Ok(());
    }
    writeln!(out, "{heading}:")?;
    for item in items {
        writeln!(out, "  - {item}")?;
    }
    Ok(())
}

/// How a headless run of the agent meets a call its posture leaves open.
fn headless(backend: &str, posture: &str, approver: &str) -> String {
    let lead = if posture.is_empty() {
        format!("approver {approver}")
    } else {
        format!("{posture}, approver {approver}")
    };
    let asks_nobody = backend == "claude-code" && posture == "bypassPermissions";
    if asks_nobody {
        format!("{lead} \u{2014} nothing is asked")
    } else if approver == "none" {
        format!("{lead} \u{2014} asks are denied at once")
    } else if approver == "reviewer" {
        format!("{lead} \u{2014} the engine's reviewer decides asks")
    } else {
        format!(
            "{lead} \u{2014} asks go to the root's approvals modal; denied at once when the root is unattended (one-shot, --plain-terminal, headless)"
        )
    }
}

/// `agent show`.
pub fn agent_show(out: &mut dyn Write, show: &AgentShow) -> io::Result<()> {
    let def = &show.definition;
    writeln!(out, "Agent: {}", def.name)?;
    if def.engine.is_empty() {
        writeln!(out, "Engine (declared): (default)")?;
    } else {
        writeln!(out, "Engine (declared): {}", def.engine)?;
    }
    match show.resolved.as_ref() {
        Some(resolved) => settings_lines(out, &resolved.settings, "")?,
        None => {
            if !def.runtime.is_empty() {
                writeln!(out, "Runtime: {}", def.runtime)?;
            }
            if !def.permissions_line.is_empty() {
                writeln!(out, "Permissions: {}", def.permissions_line)?;
            }
            for line in &def.engine_lines {
                writeln!(out, "{line}")?;
            }
        }
    }
    if !def.driving.is_empty() {
        writeln!(out, "Driving: {}", def.driving)?;
    }
    plain_bullets(out, "Env", &def.env)?;
    plain_bullets(out, "Profiles", &def.profiles)?;
    match &show.resolved {
        None => writeln!(
            out,
            "Resolved engine: unavailable ({})",
            show.resolution_error
        )?,
        Some(resolved) => {
            write!(out, "Resolved engine: {}", resolved.engine)?;
            if let Some(model) = &resolved.settings.model.value {
                write!(out, " (model: {model})")?;
            }
            writeln!(out)?;
            if !resolved.effective_permissions.is_empty() {
                writeln!(
                    out,
                    "Resolved permissions: {}",
                    resolved.effective_permissions
                )?;
            }
            if !resolved.approver.is_empty() {
                writeln!(
                    out,
                    "Headless posture: {}",
                    headless(
                        &resolved.engine,
                        &resolved.effective_permissions,
                        &resolved.approver
                    )
                )?;
            }
            if !resolved.session_home.is_empty() {
                writeln!(out, "Session home: {}", resolved.session_home)?;
            }
            writeln!(out, "Composed fragments: {}", resolved.fragments.len())?;
        }
    }
    if !show.requirements_error.is_empty() {
        return writeln!(out, "Requires: unavailable ({})", show.requirements_error);
    }
    if show.requirements.is_empty() {
        return Ok(());
    }
    writeln!(out, "Requires:")?;
    for requirement in &show.requirements {
        let sources: Vec<String> = requirement
            .sources
            .iter()
            .map(|s| format!("{} {}", s.kind, s.reference))
            .collect();
        writeln!(out, "  - {} ({})", requirement.command, sources.join(", "))?;
    }
    Ok(())
}

/// A permission mode nobody set: the engine's own.
const NO_MODE: &str = "engine default, no mode set";

/// The lines of an agent's settings, each with the layer that said it. A
/// runtime that replaces another layer's says what it replaced and how the
/// two differ; a model or effort nobody set says no flag is passed.
fn settings_lines(out: &mut dyn Write, s: &AgentSettings, indent: &str) -> io::Result<()> {
    let capital = indent.is_empty();
    let key = |word: &str| {
        if capital {
            let mut chars = word.chars();
            chars
                .next()
                .map(|c| c.to_uppercase().chain(chars).collect::<String>())
                .unwrap_or_default()
        } else {
            word.to_string()
        }
    };
    for (word, setting) in [("model", &s.model), ("effort", &s.effort)] {
        match &setting.value {
            Some(value) => writeln!(out, "{indent}{}: {value} ({})", key(word), setting.source)?,
            None => writeln!(
                out,
                "{indent}{}: engine default, no flag passed ({})",
                key(word),
                setting.source
            )?,
        }
    }
    let permissions = if s.permissions.value.is_empty() {
        NO_MODE
    } else {
        s.permissions.value.as_str()
    };
    writeln!(
        out,
        "{indent}{}: {permissions} ({})",
        key("permissions"),
        s.permissions.source
    )?;
    writeln!(
        out,
        "{indent}{}: {}",
        key("runtime"),
        runtime_text(&s.runtime)
    )?;
    if !capital {
        writeln!(out, "{indent}{}", env_line(&s.env))?;
    }
    Ok(())
}

/// How a runtime differs from the one it replaced, in words.
fn change_words(change: &str) -> &'static str {
    match change {
        "weakens" => "weakens isolation",
        "strengthens" => "strengthens isolation",
        _ => "ownership differs, not interchangeable",
    }
}

/// `host (default)`, or `host (agent; replaces engine container-rootless:
/// weakens isolation)`.
fn runtime_text(runtime: &RuntimeSetting) -> String {
    let (Some(replaced), Some(change)) = (&runtime.overrides, runtime.change) else {
        return format!("{} ({})", runtime.value, runtime.source);
    };
    let how = change_words(change);
    format!(
        "{} ({}; replaces {} {replaced}: {how})",
        runtime.value,
        runtime.source,
        runtime.overrides_source.unwrap_or("lower layer")
    )
}

fn sourced_line<T: std::fmt::Display>(key: &str, value: &SourcedValue<T>) -> String {
    format!("{key}: {} ({})", value.value, value.source)
}

fn names_line(key: &str, value: &SourcedValue<Vec<String>>) -> String {
    let names = if value.value.is_empty() {
        "none".to_string()
    } else {
        value.value.join(", ")
    };
    format!("{key}: {names} ({})", value.source)
}

/// An agent's env: `env: A, B (engine)` when one layer said every name,
/// else each name with its own layer (`env: A (engine), B (agent)`).
fn env_line(env: &EnvSetting) -> String {
    if env.keys.iter().all(|key| key.source == env.source) {
        return names_line(
            "env",
            &SourcedValue {
                value: env.value.clone(),
                source: env.source,
            },
        );
    }
    let each: Vec<String> = env
        .keys
        .iter()
        .map(|key| format!("{} ({})", key.name, key.source))
        .collect();
    format!("env: {}", each.join(", "))
}

/// One engine's own settings.
fn engine_lines(out: &mut dyn Write, row: &EngineRow) -> io::Result<()> {
    writeln!(out, "{}", row.name)?;
    writeln!(out, "  {}", sourced_line("command", &row.command))?;
    writeln!(out, "  {}", sourced_line("session_home", &row.session_home))?;
    writeln!(out, "  {}", sourced_line("runtime", &row.runtime))?;
    writeln!(out, "  {}", names_line("env", &row.env))?;
    let via = row
        .auth
        .via
        .as_ref()
        .map_or_else(String::new, |via| format!(" via {via}"));
    writeln!(out, "  auth: {}{via} ({})", row.auth.state, row.auth.source)
}

/// `engine list`.
pub fn engine_list(out: &mut dyn Write, rows: &[EngineRow]) -> io::Result<()> {
    for row in rows {
        engine_lines(out, row)?;
        if !row.agents.is_empty() {
            let names: Vec<&str> = row.agents.iter().map(|a| a.name.as_str()).collect();
            writeln!(out, "  agents: {}", names.join(", "))?;
        }
    }
    Ok(())
}

/// `engine show`.
pub fn engine_show(out: &mut dyn Write, row: &EngineRow) -> io::Result<()> {
    engine_lines(out, row)?;
    if row.agents.is_empty() {
        return writeln!(out, "  agents: none");
    }
    writeln!(out, "  agents:")?;
    for agent in &row.agents {
        writeln!(out, "    {}", agent.name)?;
        settings_lines(out, &agent.settings, "      ")?;
    }
    Ok(())
}

/// `companion list`: the name, the command, and whether the program is
/// there (it is not run to say so).
pub fn companion_list(out: &mut dyn Write, rows: &[CompanionRow]) -> io::Result<()> {
    if rows.is_empty() {
        return writeln!(
            out,
            "No companions registered.\nUse 'ctxloom companion add <name> --command <program> [args...]' to register one."
        );
    }
    for row in rows {
        let status = if row.exists {
            "found".to_string()
        } else {
            format!(
                "NOT FOUND \u{2014} fix the command, or: ctxloom companion remove {} --yes",
                row.name
            )
        };
        writeln!(
            out,
            "{:<12} {:<28} {}",
            row.name,
            row.command.join(" "),
            status
        )?;
    }
    Ok(())
}

fn mcp_target(
    out: &mut dyn Write,
    indent: &str,
    served_by: &str,
    url: &str,
    command: &str,
    args: &[String],
) -> io::Result<()> {
    if served_by == "session-endpoint" {
        return writeln!(out, "{indent}Served by: the running session's endpoint");
    }
    if !url.is_empty() {
        return writeln!(out, "{indent}URL: {}", f(url));
    }
    writeln!(out, "{indent}Command: {}", f(command))?;
    if !args.is_empty() {
        writeln!(out, "{indent}Args: {}", f(&args.join(" ")))?;
    }
    Ok(())
}

/// `mcp server list`.
pub fn mcp_list(out: &mut dyn Write, list: &McpList) -> io::Result<()> {
    if list.servers.is_empty() {
        writeln!(out, "No MCP servers registered.")?;
        return writeln!(
            out,
            "\nMCP servers ship in bundles \u{2014} compose one that declares an `mcp:` server, or check whether a profile's `exclude_mcp` withholds it."
        );
    }
    writeln!(out, "MCP Servers:")?;
    for server in &list.servers {
        writeln!(out, "  {}", f(&server.name))?;
        mcp_target(
            out,
            "    ",
            &server.served_by,
            &server.url,
            &server.command,
            &server.args,
        )?;
        writeln!(out, "    Bundle: {}", f(&server.source))?;
    }
    Ok(())
}

/// `mcp server show`.
pub fn mcp_show(out: &mut dyn Write, show: &McpShow) -> io::Result<()> {
    for entry in &show.entries {
        writeln!(out, "MCP Server: {}", f(&entry.name))?;
        writeln!(out, "Bundle: {}", f(&entry.source))?;
        mcp_target(
            out,
            "",
            &entry.served_by,
            &entry.url,
            &entry.command,
            &entry.args,
        )?;
        if !entry.env.is_empty() {
            writeln!(out, "Environment:")?;
            for (key, value) in &entry.env {
                writeln!(out, "  {}={}", f(key), f(value))?;
            }
        }
    }
    Ok(())
}

/// `search`.
pub fn search(out: &mut dyn Write, results: &SearchResults) -> io::Result<()> {
    if results.count == 0 {
        return writeln!(out, "No results found.");
    }
    writeln!(out, "Results ({}):\n", results.count)?;
    for (kind, title) in [
        ("fragment", "Fragments"),
        ("command", "Commands"),
        ("skill", "Skills"),
        ("profile", "Profiles"),
        ("mcp_server", "MCP Servers"),
    ] {
        let hits: Vec<_> = results.local.iter().filter(|h| h.kind == kind).collect();
        if hits.is_empty() {
            continue;
        }
        writeln!(out, "{title}:")?;
        for hit in hits {
            write!(out, "  - {}", f(&hit.name))?;
            if !hit.tags.is_empty() {
                write!(out, " [{}]", f(&hit.tags.join(", ")))?;
            }
            if !hit.source.is_empty() {
                write!(out, " ({})", f(&hit.source))?;
            }
            writeln!(out)?;
        }
    }
    if !results.remote.is_empty() {
        if !results.local.is_empty() {
            writeln!(out)?;
        }
        remote_table(out, &results.remote)?;
    }
    Ok(())
}

/// What the remotes publish, as a table whose Remote and Name columns are as
/// wide as their widest entry, so a name is never cut.
fn remote_table(out: &mut dyn Write, hits: &[RemoteFound]) -> io::Result<()> {
    let remote_width = hits
        .iter()
        .map(|h| text::width(&h.remote))
        .max()
        .unwrap_or(0)
        .max(text::width("Remote"));
    let name_width = hits
        .iter()
        .map(|h| text::width(&f(&h.name)))
        .max()
        .unwrap_or(0)
        .max(text::width("Name"));
    let row = |kind: &str, remote: &str, name: &str, tags: &str| {
        format!(
            "  {kind:<8} \u{2502} {} \u{2502} {} \u{2502} {tags}",
            text::pad(remote, remote_width),
            text::pad(name, name_width)
        )
    };
    let rule = |width: usize| "\u{2500}".repeat(width);
    writeln!(out, "Remote:")?;
    writeln!(out, "{}", row("Type", "Remote", "Name", "Tags"))?;
    writeln!(
        out,
        "  {}\u{253c}{}\u{253c}{}\u{253c}{}",
        rule(9),
        rule(remote_width + 2),
        rule(name_width + 2),
        rule(12)
    )?;
    for hit in hits {
        let tags = f(&hit.tags.join(", "));
        let tags = if tags.chars().count() > 20 {
            format!("{}...", tags.chars().take(17).collect::<String>())
        } else {
            tags
        };
        writeln!(out, "{}", row(&hit.kind, &hit.remote, &f(&hit.name), &tags))?;
    }
    writeln!(out)?;
    writeln!(
        out,
        "Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom deps pull"
    )
}

/// `run --dry-run`: the sections this build can show.
pub fn dry_run(out: &mut dyn Write, plan: &DryRun) -> io::Result<()> {
    if !plan.agent.is_empty() {
        let (workspace, runtime) = &plan.axes;
        writeln!(out, "=== Agent ===")?;
        writeln!(
            out,
            "{} (workspace: {workspace}, runtime: {runtime})",
            plan.agent
        )?;
    }
    writeln!(out, "=== Engine ===")?;
    writeln!(out, "{}", plan.engine)?;
    if let Some(settings) = &plan.settings {
        dry_run_settings(out, settings, &plan.settings_agent, &plan.engine)?;
    }
    writeln!(out, "\n=== Profiles ===")?;
    listed(out, &plan.profiles, "(no profiles)")?;
    writeln!(out, "\n=== Fragments Loaded ===")?;
    listed(out, &plan.fragments, "(no fragments)")?;
    writeln!(out, "\n=== Assembled Context (~{} tokens) ===", plan.tokens)?;
    if plan.context.is_empty() {
        writeln!(out, "(no context)")?;
    } else {
        writeln!(out, "{}", plan.context)?;
    }
    writeln!(out, "\n=== Prompt ===")?;
    if plan.prompt.is_empty() {
        writeln!(out, "(interactive mode)")
    } else {
        writeln!(out, "{}", plan.prompt)
    }
}

/// Ruling 72: each setting a run takes, with the place in the config that
/// said it (the JSON's `settings` says the same by word).
fn dry_run_settings(
    out: &mut dyn Write,
    s: &AgentSettings,
    agent: &str,
    engine: &str,
) -> io::Result<()> {
    // An agent's model, effort and permissions are in its block for the
    // engine; its runtime and env are on the agent itself.
    let place = |source: &str, in_block: bool| match source {
        "agent" if in_block => format!("agents.{agent}.engines.{engine}"),
        "agent" => format!("agents.{agent}"),
        "engine" => format!("engines.{engine}"),
        "project" => "runtime".to_string(),
        other => other.to_string(),
    };
    writeln!(out, "\n=== Settings ===")?;
    let row = |out: &mut dyn Write, key: &str, value: &str, at: String| {
        writeln!(out, "  {key:<11}  {value}  ({at})")
    };
    for (key, setting) in [("model", &s.model), ("effort", &s.effort)] {
        let value = setting
            .value
            .as_deref()
            .unwrap_or("engine default, no flag passed");
        row(out, key, value, place(setting.source, true))?;
    }
    // No permission mode reaches the engine yet: the row says the mode the
    // config names, and that it is not passed.
    let permissions = if s.permissions.value.is_empty() {
        NO_MODE.to_string()
    } else {
        format!("{}, not passed to the engine yet", s.permissions.value)
    };
    row(
        out,
        "permissions",
        &permissions,
        place(s.permissions.source, true),
    )?;
    let mut at = place(s.runtime.source, false);
    if let (Some(replaced), Some(change)) = (&s.runtime.overrides, s.runtime.change) {
        let how = change_words(change);
        let from = place(s.runtime.overrides_source.unwrap_or("default"), false);
        at = format!("{at}; replaces {from} {replaced}: {how}");
    }
    row(out, "runtime", &s.runtime.value, at)?;
    if s.env.keys.is_empty() {
        return row(out, "env", "none", place(s.env.source, false));
    }
    let each: Vec<String> = s
        .env
        .keys
        .iter()
        .map(|key| format!("{}  ({})", key.name, place(key.source, false)))
        .collect();
    writeln!(out, "  {:<11}  {}", "env", each.join(", "))
}

fn listed(out: &mut dyn Write, items: &[String], none: &str) -> io::Result<()> {
    if items.is_empty() {
        return writeln!(out, "{none}");
    }
    for item in items {
        writeln!(out, "  {item}")?;
    }
    Ok(())
}

#[cfg(test)]
#[path = "text.test.rs"]
mod tests;
