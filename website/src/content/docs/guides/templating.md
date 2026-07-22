---
title: "Templating"
---

Without templating, a fragment that mentions your project name or language is stuck to one project — reuse it elsewhere and you're hand-editing a copy per repo, and the copies drift. Write the fragment once with placeholders, and each project's profile fills them in with its own values: one fragment, as many projects as you have profiles for.

Fragments support [Mustache](https://mustache.github.io/) templating to make this work.

:::note[Commands are different]
This page describes templating in **fragments**. A command exported as a slash command uses a narrower, unrelated mechanism: `{{VAR}}` placeholders are rewritten to positional shell arguments (`$1`, `$2`, … in first-occurrence order) at export time — the profile's `variables:` map is never consulted. Only the plain `{{word}}` pattern is recognized, so sections, inverted sections, raw output, and comments below do not work in commands.
:::

## Basic Syntax

Use double braces for variable substitution:

```yaml
fragments:
  project-info:
    content: |
      # {{PROJECT_NAME}} Guidelines
      This project uses {{LANGUAGE}}.
      Team: {{TEAM}}
```

## Defining Variables

Template data comes entirely from the resolved profile's `variables:` map — there are no built-in variables. An undefined **plain** variable (`{{FOO}}`) renders verbatim — the literal text `{{FOO}}` reaches the assembled context unchanged, not blanked — and logs a warning naming both the variable and the fragment it came from (`ctxloom: warning: undefined variable: {{FOO}} (fragment "bundle#fragments/name")`), so the mistake is both visible in the output and traceable to its source. An undefined **section or inverted-section** name (`{{#FOO}}`/`{{^FOO}}`) is unaffected by this — its presence-toggle behavior (below, under "Sections (Conditionals)") is unchanged: absence still means falsy. (The `CTXLOOM_ROOT` shell environment variable overrides project-root detection; it is not available inside templates.)

### In Profiles

```yaml
# .ctxloom/profiles/developer.yaml
variables:
  PROJECT_NAME: "my-app"
  LANGUAGE: "Go"
  LOG_LEVEL: "debug"
  TEAM: "backend"
```

### In Config

```yaml
# .ctxloom/config.yaml
profiles:
  definitions:
    quick:
      variables:
        MODE: "fast"
```

## Variable Inheritance

When using parent profiles, variables inherit and can be overridden:

```yaml
# base.yaml
variables:
  LANGUAGE: "Python"
  FRAMEWORK: "FastAPI"
  LOG_LEVEL: "info"

# child.yaml
parents:
  - base
variables:
  PROJECT_NAME: "my-app"    # New variable
  FRAMEWORK: "Django"       # Override parent
  # LANGUAGE and LOG_LEVEL inherited from base
```

## Mustache Features

### Simple Variables

```mustache
Hello, {{name}}!
```

If `name` isn't in the profile's `variables:` map, this renders as `Hello, {{name}}!` — the literal tag, unchanged — and logs a warning (see [Error Handling](#error-handling)).

### Sections (Conditionals)

Profile variables are strings, so a section acts as a presence toggle: any non-empty value enables it, and an unset or empty variable skips it. Lists are not supported, so section iteration is not available. This presence-toggle behavior is exact key-absence, unaffected by the verbatim rendering described above for plain variables: an undefined section name is still simply missing, never seeded with a literal value.

```yaml
variables:
  DEBUG: "true"
```

```mustache
{{#DEBUG}}
Debug mode is enabled.
{{/DEBUG}}
```

### Inverted Sections (Falsy Check)

```mustache
{{^PRODUCTION}}
This is not production - be careful!
{{/PRODUCTION}}
```

### Raw Output (Unescaped)

```mustache
{{{HTML_CONTENT}}}
```

### Comments

```mustache
{{! This comment won't appear in output }}
```

## Error Handling

- **Undefined plain variables** (`{{NAME}}`): Logged as a warning naming the fragment (`ctxloom: warning: undefined variable: {{NAME}} (fragment "bundle#fragments/name")`), rendered **verbatim** — the literal `{{NAME}}` text reaches the assembled context, not blanked. This applies to `{{name}}`, `{{{name}}}`, and `{{&name}}` alike (all render as `{{name}}` when undefined — the raw/escaped distinction isn't preserved for an undefined tag).
- **Undefined sections/inverted sections** (`{{#NAME}}`/`{{^NAME}}`): Unaffected by the above — an undefined name is still simply falsy, so `{{#NAME}}...{{/NAME}}` omits its body and `{{^NAME}}...{{/NAME}}` emits its. This is the documented presence-toggle idiom (see [Sections (Conditionals)](#sections-conditionals)); it is never flipped by verbatim rendering, even when the same name is also used as a plain variable elsewhere in the same fragment — that collision case renders the plain occurrence as empty (not verbatim), protecting the section's polarity.
- **Render failures**: Original content returned unchanged
- **All variables are strings**: Converted to `map[string]interface{}`

## Writing Literal `{{...}}` in Prose

A fragment that *documents* another `{{...}}`-flavored syntax — a `justfile`'s
`{{TOP}}` / `{{justfile_directory()}}`, a Jinja2 template, a Vue/Handlebars
binding — collides with fragment templating: every such tag looks like a
ctxloom variable reference, so it's checked and (if the name isn't one of the
profile's variables) logged as an undefined-variable warning. As of the fix
described above, the literal text now survives verbatim in the assembled
output by default — `{{ARGS}}` reaches the engine as `{{ARGS}}`, not nothing —
but the warning still fires on every assembly, which is noisy for prose that
will never bind a real variable of that name.

Mustache's standard "Set Delimiter" tag remains the more surgical fix: it
silences the warning entirely (not just the corruption), by temporarily
switching delimiters, writing the literal braces as plain text, then
switching back.

```mustache
{{=<% %>=}}
Use `{{ARGS}}` to forward recipe arguments; `{{justfile_directory()}}` is
scoped to the local file, not a composing parent.
<%={{ }}=%>
```

Everything between the two Set Delimiter tags is plain text as far as the
templating engine is concerned — `{{ARGS}}` above is never parsed as a tag,
never warned about, and reaches the engine byte-for-byte. Real ctxloom
variables outside the escaped block still substitute normally.

Prefer this whenever a fragment includes example code or prose for another
tool that happens to share Mustache's `{{ }}` delimiter: verbatim rendering
is the safety net for text that slips through unescaped, not a replacement
for escaping it deliberately.

:::danger[Always close the block]
Both tags are required. If you open with `{{=<% %>=}}` and never write the
closing `<%={{ }}=%>`, everything after the opening tag stays literal for the
rest of the fragment — including your *real* variables, which arrive as
`{{name}}` rather than their values.

No warning is emitted. The delimiters were switched, so nothing downstream
looks like a tag any more, and the check that reports undefined variables has
nothing left to report. An unclosed block fails more quietly than the
corruption it was meant to prevent — verbatim rendering of undefined
variables (above) doesn't help here either: this text was never tokenized as
a tag in the first place, so there's nothing for that mechanism to catch.
The one difference is cosmetic: a swallowed real variable now *looks*
exactly like an undefined one (both render as literal `{{name}}`), but only
the undefined case gets a warning — this footgun still gets none.
:::

## Examples

### Project Context

```yaml
# Profile
variables:
  PROJECT: "api-server"
  LANGUAGE: "Go"
  VERSION: "1.0"

# Fragment
content: |
  # {{PROJECT}} ({{VERSION}})

  This {{LANGUAGE}} project follows these standards:
  - Use gofmt for formatting
  - Write tests for all public functions
```

### Conditional Content

```yaml
# Profile
variables:
  USE_DOCKER: "true"
  CI_PLATFORM: "github"

# Fragment
content: |
  ## Deployment

  {{#USE_DOCKER}}
  Build with: docker build -t app .
  {{/USE_DOCKER}}

  {{#CI_PLATFORM}}
  CI runs on: {{CI_PLATFORM}}
  {{/CI_PLATFORM}}
```

## Best Practices

1. **Use descriptive names** - `PROJECT_NAME` not `pn`
2. **Document required variables** - Note them in the fragment's `notes:` field
3. **Provide defaults in profiles** - Avoid undefined variable warnings
4. **Keep templates simple** - Complex logic belongs in code
5. **Use sections for optionals** - `{{#VAR}}...{{/VAR}}` handles missing gracefully
