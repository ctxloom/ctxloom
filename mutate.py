#!/usr/bin/env python3
"""U2 mutation harness.

For each mutation: patch internal/trust/bundleref.go (PRODUCTION ONLY, never a
test), run the trust package, record which tests died, revert.

A SURVIVING mutation means the test that names that rule is a tautology.
"""
import subprocess
import sys

SRC = "internal/trust/bundleref.go"

# (id, rule, description, old, new)
MUTATIONS = [
    ("M1", "R1", "unreserved percent-escapes are NOT decoded before parse",
     '\tif !strings.Contains(s, "%") {\n\t\treturn s, nil\n\t}',
     '\tif true {\n\t\treturn s, nil\n\t}'),

    ("M2", "R1", "percent-escape hex is NOT uppercased by the decoder",
     "func upperHex(c byte) byte {\n\tif 'a' <= c && c <= 'f' {\n\t\treturn c - 'a' + 'A'\n\t}\n\treturn c\n}",
     "func upperHex(c byte) byte {\n\treturn c\n}"),

    ("M3", "R1", "host is NOT lowercased",
     "\t\tr.Host = strings.ToLower(u.Host)",
     "\t\tr.Host = u.Host"),

    ("M4", "R1", "trailing slash is NOT dropped (empty segment kept)",
     '\t\tcase "", ".":\n\t\t\t// An empty segment here can only come from a trailing slash,\n\t\t\t// which R1 drops.',
     '\t\tcase ".":'),

    # R1's empty-query clause has NO single point to mutate: see the note in
    # ParseBundleRef. render builds a fresh url.URL, so the drop is a
    # consequence of the architecture rather than of a statement. The nearest
    # single-point mutation is to make render carry the query across, which is
    # an ADDITION to render and not a mutation of anything R1 wrote.
    ("M5", "R1", "render carries the parsed URL's ForceQuery across",
     "func (r BundleRef) render(withVersion bool) string {\n\tu := url.URL{Scheme: schemePrefix + string(r.Class)}",
     "func (r BundleRef) render(withVersion bool) string {\n\tu := url.URL{Scheme: schemePrefix + string(r.Class), ForceQuery: true}"),

    ("M6", "R1", "path components are NOT percent-encoded on output",
     "\tu := url.URL{Path: p}\n\treturn u.EscapedPath()",
     "\treturn p"),

    ("M7", "R1", "internal-class opaque name is emitted RAW",
     "\t\topaque := escapeAt(escapePath(r.Bundle))",
     "\t\topaque := r.Bundle"),

    ("M8", "R1", "control characters are NOT refused",
     "\tif i := strings.IndexFunc(raw, isRefControlRune); i >= 0 {",
     "\tif i := -1; i >= 0 {"),

    ("M9", "R1", "encoded slash (%2F) is NOT refused",
     "\tif i := indexEncodedSlash(raw); i >= 0 {",
     "\tif i := -1; i >= 0 {"),

    ("M10", "R2", "forge-case rejection removed entirely",
     "\tif r.Class == ClassGit && knownCaseFoldForges[r.Host] {\n\t\tif lower := strings.ToLower(repoPath); lower != repoPath {",
     "\tif false {\n\t\tif lower := strings.ToLower(repoPath); lower != repoPath {"),

    ("M11", "R2", "uppercase repo path is SILENTLY REWRITTEN instead of rejected",
     '\t\t\treturn fmt.Errorf("%w: %s treats repository paths case-insensitively; write %q, not %q",\n\t\t\t\tErrRefForgeCase, r.Host, lower, repoPath)',
     "\t\t\trepoPath = lower"),

    ("M12", "R2", "rejection GENERALIZED to every host, not just folding forges",
     "\tif r.Class == ClassGit && knownCaseFoldForges[r.Host] {",
     "\tif r.Class == ClassGit {"),

    ("M13", "R3", "bundle/item name case is FOLDED instead of preserved",
     "\tr.RepoPath = repoPath\n\tr.Bundle = bundle",
     "\tr.RepoPath = repoPath\n\tr.Bundle = strings.ToLower(bundle)"),

    ("M14", "R3", "fold-collision detection disabled",
     "\tseen := make(map[string]BundleRef, len(refs))\n\tvar msgs []string",
     "\tif true {\n\t\treturn nil\n\t}\n\tseen := make(map[string]BundleRef, len(refs))\n\tvar msgs []string"),

    ("M15", "R3", "collision bucket OVER-BROAD: repo path folded into it too",
     "\t\tr.Host,\n\t\tr.RepoPath,",
     "\t\tr.Host,\n\t\tstrings.ToLower(r.RepoPath),"),

    ("M16", "R4", "resolveDotSegmentsEachSide REPLACED BY path.Clean (the required mutation)",
     '\tbefore, after, found := strings.Cut(escPath, repoBundleSeparator)\n\tif !found {\n\t\treturn "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path",\n\t\t\tErrRefSyntax, repoBundleSeparator)\n\t}\n\treturn removeDotSegments(before), removeDotSegments(after), nil',
     '\tcleaned := path.Clean(escPath)\n\tbefore, after, found := strings.Cut(cleaned, repoBundleSeparator)\n\tif !found {\n\t\treturn "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path",\n\t\t\tErrRefSyntax, repoBundleSeparator)\n\t}\n\treturn before, after, nil'),

    ("M17", "R4", "dot segments NOT removed at all",
     '\tabsolute := strings.HasPrefix(p, "/")\n\tout := make([]string, 0, 8)',
     '\tif true {\n\t\treturn p\n\t}\n\tabsolute := strings.HasPrefix(p, "/")\n\tout := make([]string, 0, 8)'),

    ("M18", "R4", "halves resolved TOGETHER, re-split by searching FORWARD for the marker",
     "\treturn removeDotSegments(before), removeDotSegments(after), nil",
     '\twhole := removeDotSegments(before + "/" + after)\n\ti := strings.Index(whole, "/"+bundleMarker)\n\tif i < 0 {\n\t\treturn "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path", ErrRefSyntax, repoBundleSeparator)\n\t}\n\treturn whole[:i], whole[i+1:], nil'),

    ("M21", "R4", "halves resolved TOGETHER, re-split by searching BACKWARD for the marker",
     "\treturn removeDotSegments(before), removeDotSegments(after), nil",
     '\twhole := removeDotSegments(before + "/" + after)\n\ti := strings.LastIndex(whole, "/"+bundleMarker)\n\tif i < 0 {\n\t\treturn "", "", fmt.Errorf("%w: missing %q separator between repository path and bundle path", ErrRefSyntax, repoBundleSeparator)\n\t}\n\treturn whole[:i], whole[i+1:], nil'),

    ("M19", "R5", "version LEAKS into Identity",
     "func (r BundleRef) Identity() string {\n\treturn r.render(false)\n}",
     "func (r BundleRef) Identity() string {\n\treturn r.render(true)\n}"),

    ("M20", "R5", "'@' in a name is NOT escaped, so it re-reads as a version",
     '\treturn strings.ReplaceAll(s, "@", "%40")',
     "\treturn s"),
]


def run_tests():
    p = subprocess.run(["just", "test-pkg", "./internal/trust/"],
                       capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def failing_tests(out):
    names = []
    for line in out.splitlines():
        s = line.strip()
        if s.startswith("--- FAIL:"):
            names.append(s.split("--- FAIL:")[1].strip().split(" ")[0])
    seen, uniq = set(), []
    for n in names:
        top = n.split("/")[0]
        if top not in seen:
            seen.add(top)
            uniq.append(top)
    return uniq


def revert():
    subprocess.run(["git", "checkout", "--", SRC], check=True)


def main():
    # revert() restores SRC from the INDEX, so any uncommitted edit to it is
    # destroyed on the first mutation. That has already happened once: a
    # production change made between a commit and a harness run vanished, and
    # the only symptom was a mutation flipping from NOT-APPLIED to SURVIVED.
    # Refuse to start rather than eat the work.
    dirty = subprocess.run(["git", "status", "--porcelain", "--", SRC],
                           capture_output=True, text=True).stdout.strip()
    if dirty:
        print(f"refusing to run: {SRC} has uncommitted changes that revert()"
              f" would destroy. Commit or stash first.\n  {dirty}")
        return 2

    base = open(SRC).read()
    needs_path_import = 'ctxloom-mutation-path-import'
    results = []
    for mid, rule, desc, old, new in MUTATIONS:
        if base.count(old) != 1:
            results.append((mid, rule, desc, "NOT-APPLIED",
                            f"anchor matched {base.count(old)} times"))
            continue
        mutated = base.replace(old, new, 1)
        if mid == "M16":
            mutated = mutated.replace('\t"net/url"', '\t"net/url"\n\t"path"', 1)
        open(SRC, "w").write(mutated)
        code, out = run_tests()
        if code == 0:
            results.append((mid, rule, desc, "SURVIVED", "tests still green"))
        else:
            fails = failing_tests(out)
            if not fails:
                results.append((mid, rule, desc, "KILLED(build)",
                                "did not compile — not a behavioural kill"))
            else:
                results.append((mid, rule, desc, "KILLED", ", ".join(fails)))
        revert()
        _ = needs_path_import

    print("\n" + "=" * 100)
    print(f"{'ID':<5} {'RULE':<5} {'VERDICT':<14} DESCRIPTION / KILLED BY")
    print("=" * 100)
    survived = 0
    for mid, rule, desc, verdict, detail in results:
        if verdict == "SURVIVED":
            survived += 1
        print(f"{mid:<5} {rule:<5} {verdict:<14} {desc}")
        print(f"{'':<26} -> {detail}")
    print("=" * 100)
    print(f"{len(results)} mutations, {survived} SURVIVED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
