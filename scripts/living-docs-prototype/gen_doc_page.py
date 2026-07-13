#!/usr/bin/env python3
"""
PROTOTYPE generator for the "living docs" proposal (docs/living-docs-plan.md).

NOT wired into `just build`, `just gen-docs`, or CI. Run by hand to produce
one Starlight page from three real inputs:

  1. A .feature file        — the business-readable spec (Gherkin).
  2. A .doc.md companion     — human narration, keyed to scenario names via
                               <!-- doc:scenario: <name> --> markers.
  3. A capture directory     — one JSON file per scenario, written by the
                               tests/acceptance @doc capture sidecar
                               (tests/acceptance/steps_doc_capture.go) during
                               an ACTUAL passing godog run. Each file holds
                               every step's pass/fail status plus whatever
                               real CLI output / mock-engine recorded input
                               the harness observed.

The honesty rule this script enforces: a scenario capture with ANY step not
"passed" aborts the whole generation. There is no partial-credit mode — if
the suite is red, this script refuses to produce a page, on purpose. A
scenario with NO capture file at all (never run in this pass — e.g. a
credential-gated @live scenario) is rendered from the Gherkin alone, clearly
labeled as not captured in this build; that is different from "ran and
failed" and is not an error.

Usage:
    python3 gen_doc_page.py \\
        --feature tests/acceptance/features/j1_setup.feature \\
        --narration tests/acceptance/features/j1_setup.doc.md \\
        --capture-dir /path/to/captured/json/dir \\
        --out website/src/content/docs/journeys/j1-setup.md \\
        --url-path /journeys/j1-setup/
"""
import argparse
import json
import os
import re
import sys
from collections import defaultdict


# --------------------------------------------------------------------------
# .feature parsing (deliberately minimal — not a general Gherkin parser; it
# knows just enough about THIS project's feature-file shape: '#'-comments,
# '@tag' lines, and 'Scenario:'/'Scenario Outline:' headers).
# --------------------------------------------------------------------------

def parse_feature(path):
    with open(path, encoding="utf-8") as f:
        lines = f.read().splitlines()

    feature_name = None
    feature_desc = []
    scenarios = []
    current_tags = []
    i = 0
    n = len(lines)
    seen_feature = False

    while i < n:
        raw = lines[i]
        stripped = raw.strip()

        if stripped.startswith("#"):
            i += 1
            continue

        if stripped.startswith("@"):
            current_tags = stripped.split()
            i += 1
            continue

        if stripped.startswith("Feature:"):
            feature_name = stripped[len("Feature:"):].strip()
            seen_feature = True
            current_tags = []
            i += 1
            # description lines up to the first tag/scenario/comment/blank-run
            while i < n:
                s2 = lines[i].strip()
                if s2.startswith("@") or s2.startswith("#") or \
                   s2.startswith("Scenario:") or s2.startswith("Scenario Outline:"):
                    break
                if s2:
                    feature_desc.append(s2)
                i += 1
            continue

        if stripped.startswith("Scenario Outline:") or stripped.startswith("Scenario:"):
            keyword = "Scenario Outline" if "Outline" in stripped.split(":")[0] else "Scenario"
            name = stripped.split(":", 1)[1].strip()
            tags = current_tags
            current_tags = []
            body = [raw]
            i += 1
            while i < n:
                nxt = lines[i]
                s2 = nxt.strip()
                if s2.startswith("#"):
                    i += 1
                    continue
                if s2.startswith("@"):
                    break  # next scenario's tag line
                if s2.startswith("Scenario:") or s2.startswith("Scenario Outline:"):
                    break  # next scenario, untagged (no leading '@' line)
                body.append(nxt)
                i += 1
            while body and body[-1].strip() == "":
                body.pop()
            scenarios.append({
                "keyword": keyword,
                "name": name,
                "tags": tags,
                "body": "\n".join(body),
            })
            continue

        i += 1

    if not seen_feature:
        raise ValueError(f"{path}: no 'Feature:' line found")
    return {"name": feature_name, "description": feature_desc, "scenarios": scenarios}


# --------------------------------------------------------------------------
# .doc.md narration parsing
# --------------------------------------------------------------------------

INTRO_RE = re.compile(r"<!--\s*doc:intro\s*-->(.*?)<!--\s*/doc:intro\s*-->", re.S)
OUTRO_RE = re.compile(r"<!--\s*doc:outro\s*-->(.*?)<!--\s*/doc:outro\s*-->", re.S)
SCENARIO_RE = re.compile(
    r"<!--\s*doc:scenario:\s*(.*?)\s*-->(.*?)<!--\s*/doc:scenario\s*-->", re.S
)


def parse_narration(path):
    with open(path, encoding="utf-8") as f:
        text = f.read()
    intro = next(iter(INTRO_RE.findall(text)), "").strip()
    outro = next(iter(OUTRO_RE.findall(text)), "").strip()
    per_scenario = {}
    for name, body in SCENARIO_RE.findall(text):
        per_scenario[name.strip()] = body.strip()
    return {"intro": intro, "outro": outro, "scenarios": per_scenario}


# --------------------------------------------------------------------------
# Capture loading
# --------------------------------------------------------------------------

def load_captures(capture_dir):
    """Returns {scenario_name: [capture_dict, ...]}, sorted by filename."""
    by_name = defaultdict(list)
    if not capture_dir or not os.path.isdir(capture_dir):
        return by_name
    for fname in sorted(os.listdir(capture_dir)):
        if not fname.endswith(".json"):
            continue
        with open(os.path.join(capture_dir, fname), encoding="utf-8") as f:
            cap = json.load(f)
        by_name[cap["scenario"]].append(cap)
    return by_name


def assert_all_passed(scenario_name, captures):
    for cap in captures:
        for step in cap["steps"]:
            if step["status"] != "passed":
                raise SystemExit(
                    f"REFUSING TO GENERATE: scenario {scenario_name!r} has a "
                    f"non-passed step ({step['status']!r}: {step['text']!r}) "
                    f"in a capture file. A red scenario cannot be documented."
                )


# --------------------------------------------------------------------------
# Rendering
# --------------------------------------------------------------------------

def safe_fence(text):
    """Returns a backtick fence at least one longer than the longest run of
    backticks already present in text, so captured content that itself
    contains a ```-fenced example (e.g. a skill's own markdown) can never
    prematurely close our wrapping fence."""
    runs = re.findall(r"`+", text)
    longest = max((len(r) for r in runs), default=0)
    return "`" * max(3, longest + 1)


def fenced(text, lang=""):
    fence = safe_fence(text)
    return f"{fence}{lang}\n{text.rstrip(chr(10))}\n{fence}"


# Two-column row wrappers. The grid uses auto-fit + minmax(min(100%, …)) so it
# is responsive WITHOUT a media query: two columns when the container is wide
# enough, one stacked column on narrow screens (and never wider than 100% on a
# very narrow phone). min-width:0 on the columns lets a wide <pre> scroll inside
# its column instead of blowing the grid track out. Styles are inline so they
# survive Starlight's markdown pass-through regardless of whether a <style>
# block would be hoisted; the class names are there for a future shared
# stylesheet to hook if desired.
ROW_OPEN = (
    '<div class="living-doc-row" '
    'style="display:grid;'
    'grid-template-columns:repeat(auto-fit,minmax(min(100%,340px),1fr));'
    'gap:1.25rem;margin:1.5rem 0;align-items:start;">'
)
COL_OPEN = '<div class="living-doc-col" style="min-width:0;overflow-x:auto;">'
DIV_CLOSE = '</div>'


def render_two_col(left_md, right_md):
    """Wraps two already-rendered markdown blocks into a responsive two-column
    grid. The blank lines around each column's content are load-bearing: an
    HTML block (the <div>) only yields back to the markdown parser after a
    blank line, so without them the fenced code inside would render as literal
    text rather than a code block."""
    return "\n".join([
        ROW_OPEN,
        COL_OPEN,
        "",
        left_md,
        "",
        DIV_CLOSE,
        COL_OPEN,
        "",
        right_md,
        "",
        DIV_CLOSE,
        DIV_CLOSE,
    ])


def render_captured_output(captures):
    """Right-column content: the per-step pass checklist plus the real captured
    CLI output / mock-engine-received payload, for one or more Examples-row
    captures."""
    lines = []
    for idx, cap in enumerate(captures):
        if len(captures) > 1:
            lines.append(f"**Captured run {idx + 1}**")
            lines.append("")
        lines.append("Every step below actually ran against a real `ctxloom` "
                     "binary; nothing here is hand-written.")
        lines.append("")
        for step in cap["steps"]:
            mark = "✓" if step["status"] == "passed" else f"✗ ({step['status']})"
            lines.append(f"- {mark} {step['text']}")
        lines.append("")
        for step in cap["steps"]:
            if step.get("cli_output"):
                lines.append(f"CLI output — `{step['text']}`:")
                lines.append("")
                lines.append(fenced(step["cli_output"], "text"))
                lines.append("")
            if step.get("mock_recorded"):
                lines.append(f"What the mock engine received — `{step['text']}`:")
                lines.append("")
                lines.append(fenced(step["mock_recorded"], "text"))
                lines.append("")
    return "\n".join(lines).rstrip()


NOT_CAPTURED_MD = (
    "> **Not captured in this build.** This scenario was not exercised in the "
    "run that generated this page (for example, a `@live` scenario without "
    "credentials in this environment). The Gherkin at left is still the live "
    "spec — just without a proof-of-passing run attached yet."
)


def render_scenario(sc, narration, captures):
    out = []
    out.append(f"## {sc['name']}")
    out.append("")
    tag_str = " ".join(sc["tags"]) if sc["tags"] else ""
    if tag_str:
        out.append(f"*Tags: {tag_str}*")
        out.append("")

    # The two-column row: Gherkin (left) beside its captured evidence (right).
    left = fenced(sc["body"], "gherkin")
    right = render_captured_output(captures) if captures else NOT_CAPTURED_MD
    out.append(render_two_col(left, right))
    out.append("")

    # The narration prose sits full-width BELOW the row.
    if sc["name"] in narration["scenarios"]:
        out.append(narration["scenarios"][sc["name"]])
        out.append("")
    return "\n".join(out)


def generate(feature, narration, captures_by_name, url_path):
    lines = []
    lines.append("---")
    lines.append(f'title: "{feature["name"]}"')
    lines.append("---")
    lines.append("<!-- GENERATED (prototype) by scripts/living-docs-prototype/gen_doc_page.py")
    lines.append("     from tests/acceptance/features/j1_setup.feature +")
    lines.append("     tests/acceptance/features/j1_setup.doc.md, using evidence captured")
    lines.append("     from a PASSING acceptance run. Do not hand-edit; edit the narration")
    lines.append("     companion or the .feature file and regenerate. -->")
    lines.append(":::note")
    lines.append(
        "This page is generated from a Gherkin acceptance journey "
        "(`j1_setup.feature`) plus real terminal output captured from an "
        "actual passing run of it — not hand-written. See "
        "[the living-docs proposal](https://github.com/ctxloom/ctxloom/blob/main/docs/living-docs-plan.md) "
        "for how."
    )
    lines.append(":::")
    lines.append("")
    if narration["intro"]:
        lines.append(narration["intro"])
        lines.append("")

    if feature["description"]:
        lines.append("> " + "\n> ".join(feature["description"]))
        lines.append("")

    for sc in feature["scenarios"]:
        caps = captures_by_name.get(sc["name"], [])
        assert_all_passed(sc["name"], caps)
        lines.append(render_scenario(sc, narration, caps))

    if narration["outro"]:
        lines.append(narration["outro"])
        lines.append("")

    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--feature", required=True)
    ap.add_argument("--narration", required=True)
    ap.add_argument("--capture-dir", required=True)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    feature = parse_feature(args.feature)
    narration = parse_narration(args.narration)
    captures_by_name = load_captures(args.capture_dir)

    page = generate(feature, narration, captures_by_name, args.out)

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(page)
    print(f"wrote {args.out} ({len(page)} bytes)")


if __name__ == "__main__":
    main()
