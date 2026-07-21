# Branch & Tag Naming Standard — the ctxloom repo family

**Status:** proposed standard. **Scope:** every repo in the family — ctxloom, tagma
(`kvtag`), reprise, ctxloom-vscode, ctxloom-default, ctxloom-personal, versionator,
angzarr, and peers.

---

## TL;DR — the one rule that fixes `v0.7.0-pre1`

**Branches name a *line of development*. Tags name a *point* (a version).**
`v0.7.0-pre1` is a version — a SemVer *pre-release* — so it belongs on a **tag**,
not a branch. Wherever a version identifier sits in branch-space, that's a category
error.

| Kind | Form | Example |
|---|---|---|
| Released line | `main` | `main` (advances only at a release) |
| Integration line (in-progress version) | `release/<major>.<minor>` *(or a permanent `develop`)* | `release/0.7` |
| Short-lived work | `<type>/<kebab-desc>` | `feat/tagma-adoption`, `fix/harp-collision` |
| Version / pre-release | **tag** `v<x>.<y>.<z>[-<pre><NNN>]` | `v0.7.0-pre001`, `v0.7.0-rc001`, `v0.7.0` |

You already do three of these four (`feat/*`, `release/*`, `vX.Y.Z` tags). The gap
is the fourth row collapsing into branch-space — `v0.7.0-pre1` is the *integration
line* wearing a *version* name.

**Your model (confirmed):** `main` is the **released** line — it moves only when a
version is tagged and released, then merged back. The in-progress version's work
lives on a **long-lived integration line**. So `main` sitting far behind mid-cycle
is *expected and correct*, not a smell — 0.7.0 hasn't shipped. This is Git Flow's
"explicitly-versioned" fit, minus the ceremony you don't need.

---

## 1. Two ref namespaces: refs that move vs refs that pin

- A **branch** is a *moving pointer* — it advances as you commit. It represents an
  ongoing line: a role (`main`), a release line (`release/0.7`), or a unit of work
  (`feat/x`).
- A **tag** is an *immutable pointer* to one commit. It represents a fixed point: a
  release or a pre-release.

A **version** (`0.7.0`, `0.7.0-pre1`, `0.7.0-rc1`) is by definition a fixed point —
the exact bits you shipped or are trialing. SemVer even defines a pre-release as
*part of the version string* ([semver.org](https://semver.org/) §9: "A pre-release
version MAY be denoted by appending a hyphen and a series of dot separated
identifiers…", e.g. `1.0.0-rc.1`). Fixed point → tag.

**Honest caveat on sourcing:** the SemVer spec is *silent* on git mechanics — it
never says "pre-releases must be tags." So "versions belong on tags" is not a rule
handed down by a spec; it's a convention that falls straight out of how git works
(a version is immutable; a tag is git's immutable pointer; a branch moves). It's
near-universal for exactly that reason.

**Why `v0.7.0-pre1`-as-a-branch bites, concretely:** a branch keeps moving, so the
name lies the instant you make the next commit — the branch called "0.7.0-pre1" now
holds work that isn't 0.7.0-pre1. And it has no answer to *"what do you name pre2?"*
Rename the branch (breaking every reference, worktree, and the two pushed copies)?
Cut a new `v0.7.0-pre2` and strand pre1? Both are the smell. Tags don't have this
problem: `v0.7.0-pre001` and `v0.7.0-pre002` are two pins on one moving line.

---

## 2. There is no formal standard — so "the standard" is *your consistency*

There is **no specification** for git branch names. Git enforces only low-level
character rules (§3); everything above — `feat/`, `release/x.y`, `develop` — is
**convention plus tooling defaults**, drawn from branching *models* and whatever
your hooks/CI enforce. Even "Conventional Branch"-style schemes are third-party
conventions, not a standards-body spec. Conventional Commits
([conventionalcommits.org](https://www.conventionalcommits.org/en/v1.0.0/)) governs
commit *messages* only and contains nothing about branch naming — branch prefixes
merely *borrow its vocabulary* (`feat`, `fix`, `chore`, …).

Practical upshot: the win is not picking the One True Prefix — it's picking one
scheme and applying it family-wide. Your core cluster already mostly has one; this
doc ratifies it and closes the gaps.

---

## 3. The only hard rules: `git check-ref-format`

The single layer git actually enforces
([git-scm.com/docs/git-check-ref-format](https://git-scm.com/docs/git-check-ref-format)).
A branch name may **not**: contain a space, `~ ^ : ? * [ \`, an ASCII control
character, `..`, `@{`, or `//`; begin or end with `/`; end with `.` or `.lock`; and
no path component may begin with `.`. Slashes are legal and create hierarchy
(`feat/x` → `refs/heads/feat/x`).

One more real constraint that is *not* in that man page (it comes from git's ref
**storage** — the "D/F conflict"): you cannot have both a branch `foo` and a branch
`foo/bar` — a ref can't be a leaf and a directory at once. So never use a bare
`feat` if you also use `feat/…` (you don't — good).

---

## 4. The landscape, on one screen

- **Git Flow** (Driessen, 2010; [nvie.com](https://nvie.com/posts/a-successful-git-branching-model/)):
  `main` + permanent `develop` + `feature/*` + `release/*` + `hotfix/*`. Heavyweight.
  **Its own author, in a 2020 note atop the original post, walks it back:** for
  continuously-delivered software "adopt a much simpler workflow (like GitHub
  flow)… If, however, you are building software that is **explicitly versioned**, or
  need to support multiple versions in the wild, then git-flow may still be … a good
  fit." You are explicitly-versioned — so you're inside his carve-out — but you do
  **not** need Git Flow's full ceremony (§5).
- **GitHub Flow** ([docs.github.com](https://docs.github.com/en/get-started/using-github/github-flow)):
  just `main` + short-lived branches, deploy from `main`. The CD default.
- **GitLab Flow**: `main` plus either environment branches
  (`staging`/`pre-production`/`production`, downstream-only) or long-lived
  version/`stable` branches for multi-version maintenance.
- **Trunk-Based Development** ([trunkbaseddevelopment.com](https://trunkbaseddevelopment.com/)):
  one `trunk`/`main`, *very* short-lived branches (≤ a couple of days), release
  branches cut "just-in-time… a few days before the release," fixes made
  trunk-first then cherry-picked. The modern CI/CD reference model.

**2024–25 direction of travel:** short-lived branches + trunk + tag-based releases,
away from heavyweight Git Flow. Real projects' release/maintenance branches are
**minor-level**, with patches riding as tags: Django `stable/A.B.x`, Kubernetes
`release-1.30`, Linux `linux-6.6.y`, Rails `7-1-stable`. **Nobody cuts a branch per
patch** — patch precision lives in tags.

---

## 5. The standard for the family

A Git-Flow-lite model for explicitly-versioned software: **`main` = released**, a
**long-lived integration line** carries the in-progress version, work happens on
short-lived branches off the integration line, and **releases are tags** — cut on
the integration line, then merged down to `main`.

### Long-lived
- **`main` — the released line.** It advances *only* when a version is released:
  the integration line merges in and the release is tagged (`v0.7.0`). Between
  releases it legitimately lags — that's the point of it. Don't develop on `main`
  directly.
- **The integration line — `release/<major>.<minor>` (e.g. `release/0.7`).** This
  is what `v0.7.0-pre1` *is*; it just needs a role-name, not a version-name. A fresh
  line per minor series carries all of that version's work and its pre-release/RC
  tags; a big release like 0.7.0 fully earns a long-lived one. (A single permanent
  `develop` branch is the classic alternative, but the family standard is the
  per-version `release/x.y` line — it matches how you already think in versions and
  gives clean `0.7.x` maintenance later.)

### Release/version mechanics (the "merge to main after tag" you described)
- Pre-releases and the release itself are **tags on the integration line**:
  `v0.7.0-pre001`, `v0.7.0-rc001`, then `v0.7.0`.
- At release: **merge the integration line into `main`** and ensure the `v0.7.0`
  tag sits on that released commit. `main` now == 0.7.0.
- `.z` patches are tags too — never a branch per patch. This replaces the current
  `release/vX.Y.Z`-**per-patch** habit (eleven such branches in ctxloom); real
  projects branch per *minor* and tag patches. If you use `release/<major>.<minor>`
  as the line, patch releases (`v0.7.1`) are just later tags on `release/0.7`.
- **Line, not point:** `release/0.7`, not `release/0.7.0`. One line carries 0.7.0,
  0.7.1, 0.7.2…

### Short-lived work — `<type>/<kebab-description>`
- Types (ratifying what you already use; vocabulary from Conventional Commits):
  `feat/  fix/  hotfix/  chore/  docs/  refactor/  test/  ci/  perf/  build/  spike/`.
  Prefer the short forms (`feat`/`fix`) over `feature`/`bugfix` — your cluster
  already leans `feat/`.
- Description: short, kebab-case, imperative. Optionally lead with a harp/issue id:
  `feat/swift-amber-add-ranking`.
- Branch off `main` (or off a `release/x.y` for a fix specific to that line). Merge,
  then **delete** — your worktree lifecycle already says *done = merged + branch
  deleted*.

### Versions & pre-releases — always tags, never branches
- Release: `v<x>.<y>.<z>` (you already do this cleanly — keep it).
- Pre-release: `v<x>.<y>.<z>-<id><NNN>` — `-pre001`, `-rc001`, `-alpha001`. The
  counter is **zero-padded to exactly three digits**.

  **Why padding, and why not `-pre.1`.** SemVer compares a pre-release as
  dot-separated identifiers: a *numeric* identifier compares numerically, an
  *alphanumeric* one compares lexically ([semver.org](https://semver.org/) §11).
  `pre1` is a single **alphanumeric** identifier, so it sorts lexically — giving
  `pre1 < pre10 < pre11 < pre2 < pre9`. That is the bug. Two fixes exist:

  | Form | Sorts under SemVer | Sorts under a plain string sort | Ceiling |
  |---|---|---|---|
  | `-pre1` | ✗ `pre10 < pre2` | ✗ | — |
  | `-pre.1` | ✓ (numeric identifier) | ✗ `pre.1 < pre.10 < pre.2` | none |
  | **`-pre001`** | **✓** | **✓** | **999** |

  We use `-pre001`. It stays a single alphanumeric identifier, but zero-padding
  makes its *lexical* order match its numeric order — so it sorts correctly
  **everywhere**, not just in SemVer-aware tools. `git tag --list`, `ls`, `sort`,
  and any script that string-sorts all agree with `semver.Compare`. `-pre.1` is
  more canonical SemVer, but it only sorts correctly in tools that parse SemVer;
  a bare `git tag --list` (which sorts lexically unless given
  `--sort=version:refname`) gets it wrong.

  **The cap is real:** padding fixes ordering only up to the padded width —
  `pre1000 < pre999` lexically. Three digits means at most 999 pre-releases in a
  series, which is not a constraint any release line will meet. Do not drop to two
  digits.

  Verified against `github.com/Masterminds/semver/v3`: `0.7.0-pre001 <
  0.7.0-pre002 < 0.7.0-pre009 < 0.7.0-pre010 < 0.7.0-pre011 < 0.7.0`.
- Keep **versionator** as the authority so tags and build stamps agree (your stamp
  is `v<maj.min.patch>-<short-sha>-<date>`). Agents don't cut tags (ltk blocks
  `git tag`); human/CI cuts them — correct and unchanged.
- Fix the one tag outlier: `ox`'s `v0.1.0-mvp` → `v0.1.0-mvp001` (or just `v0.1.0`)
  so it stays SemVer-sortable.

### Cross-repo release trains — coordinate by *version*, not a shared branch name
The 0.7.0 train spans ctxloom + reprise + ctxloom-vscode. Coordinate it with a
**shared version/tag** (`v0.7.0-pre001` tagged in each repo at the train point) and,
if you need stabilization, `release/0.7` in each — **not** a shared `v0.7.0-pre1`
*branch* in each. A shared version-named branch re-creates the category error once
per repo and lets the three drift independently.

### Default branch
`main` everywhere. Finish the `master`→`main` migration on the older repos
(versionator is on `master`; several peers too) so the family is uniform and tooling
that assumes `main` — including your own harness's "main branch for PRs" default —
stops guessing.

### Worktree tie-in (already standardized)
Worktrees live flat at `~/workspace/worktrees/<project>--<branch-slug>`, slugifying
`/`→`-`. This scheme is compatible: `feat/tagma-adoption` →
`ctxloom--feat-tagma-adoption`. Keep it.

---

## 6. Diagnosis — `v0.7.0-pre1`

1. **A version in branch-space.** `-pre1` is a SemVer pre-release identifier; it
   belongs on a tag (§1).
2. **It ignores your *own* convention.** ctxloom already has `release/vX.Y.Z`
   branches; this line uses neither the `release/` prefix nor a tag — a third,
   ad-hoc shape.
3. **It has no corresponding tag.** There is no `v0.7.0-pre1` *tag* anywhere — the
   pre-release exists only as a moving branch, so nothing pins what "0.7.0-pre1"
   actually was. (The nearest artifact is the safety tag `backup/pre1-pre-scrub`.)
4. **`main` far behind is fine — the *name* is the problem.** `main…v0.7.0-pre1`
   = **0 behind, 701 ahead**. Under your model (`main` = released) that's *exactly
   right*: 0.7.0 hasn't shipped. So the branch's job (long-lived integration line
   for a big version) is legitimate — it's only wearing a *version* name instead of
   a *role* name. Rename it, don't reconcile `main` to it.
5. **The pre2 problem.** There's no clean name for the next pre-release increment
   while it stays a branch (§1).
6. **It's replicated 3×.** ctxloom + reprise (pushed) and ctxloom-vscode (local) all
   carry `v0.7.0-pre1` — so the fix is a family pattern, not a one-branch rename.

---

## 7. Migration (careful — this branch is holding a lot of live work)

Don't disrupt the in-flight adoption. The move is: **rename the version-branch to a
role-name, keep `main` as the released line, and make the pre-releases tags.**

```
# in each of the 3 train repos (ctxloom, reprise, ctxloom-vscode):

# 1. rename the integration line (release/0.7 shown; use `develop` if you pick that)
git branch -m v0.7.0-pre1 release/0.7

# 2. move the pushed ref: publish the new name, delete the old
git push origin release/0.7
git push origin :v0.7.0-pre1          # delete origin/v0.7.0-pre1
#   (update the local checkout's upstream: git branch -u origin/release/0.7)

# 3. pin the pre-releases you actually cut, as TAGS on this line
git tag -a v0.7.0-pre001 <the commit that was "pre1">
git push origin v0.7.0-pre001
#   ...and v0.7.0-rc001 as you reach RC, etc.
```

Then, **at release**: `git checkout main && git merge --no-ff release/0.7`, tag
`v0.7.0` on that commit, push. `main` now == 0.7.0 (its first advance since 0.6.4 —
correct). Keep `release/0.7` for `0.7.x` patch tags; start 0.8 work on a fresh
`release/0.8` (or continue on `develop`).

`main` stays the released line throughout — you never fast-forward unreleased work
onto it. And no branch is ever named after a version again.

**Sequencing:** do the rename *after* the current tagma-adoption merge lands, at a
clean checkpoint — not mid-flight. The rename is cheap but it moves a pushed ref
three other repos/worktrees may track, so do it deliberately.

---

## 8. Cheat sheet

| You want to… | Name it | Not |
|---|---|---|
| The released line | `main` | `master`, `trunk`, a version |
| Integrate an in-progress version | `release/<x>.<y>` *(or `develop`)* | `vX.Y.Z-preN`, `release/x.y.z` |
| Build a feature | `feat/<desc>` (off the integration line) | `feature-x`, bare `add-thing` |
| Fix a bug | `fix/<desc>` | `bugfix/x`, `patch-1` |
| Urgent fix to a released version | `hotfix/<desc>` (off `main`/the release line) | |
| Mark a release | tag `v<x>.<y>.<z>`, then merge the line → `main` | a branch |
| Mark a pre-release | tag `v<x>.<y>.<z>-rc001` / `-pre001` on the line | a branch |
| Coordinate a multi-repo train | shared **version/tag** | a shared version **branch** |

---

*Sources:* nvie.com (Git Flow + 2020 reflection note), docs.github.com (GitHub
Flow), GitLab Flow docs, trunkbaseddevelopment.com, semver.org §9,
git-scm.com/docs/git-check-ref-format, conventionalcommits.org; release-branch
examples from Django (`stable/A.B.x`), Kubernetes (`release-1.30`), Linux
(`linux-6.6.y`), Rails (`7-1-stable`). No formal branch-naming standard exists;
tag-vs-branch is convention grounded in git ref semantics, not a SemVer rule; the
D/F collision is ref-store behavior, not a `check-ref-format` line.
