package bundles

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// This file holds Part B, slice B1 of the skill/command split: the SKILL
// PACKAGE DATA MODEL. A skill is a directory (SKILL.md + optional sibling
// files), not a single text blob — ParseSkillPackage reads that tree off an
// afero.Fs into a validated, manifest-bearing SkillPackage. Archive codec
// (zip pack/unpack), signing/trust wiring, and per-engine materialization are
// deliberately NOT here — those are B1b/B2/B3+.

// SkillLLMExports holds per-engine enablement for a skill, keyed by backend
// name. Unlike LLMExports (fragments/commands), a skill export carries only
// Enabled — a skill's name and description are SKILL.md frontmatter, the
// single source of truth, and never duplicated into bundle.yaml.
type SkillLLMExports struct {
	ClaudeCode SkillEngineExport `yaml:"claude-code"`
	Codex      SkillEngineExport `yaml:"codex"`
	Kiro       SkillEngineExport `yaml:"kiro"`
	Opencode   SkillEngineExport `yaml:"opencode"`
}

// SkillEngineExport is one engine's enablement setting for a skill.
type SkillEngineExport struct {
	Enabled *bool `yaml:"enabled"` // nil = true (opt-out model, mirrors ClaudeCodeConfig etc.)
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c SkillEngineExport) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// Skill authoring/validation constraints (Anthropic Agent Skills spec, VERIFIED
// hard constraints — see the skill/command split plan §3.1). Violating any of
// these fails LOUD at parse time; nothing here silently truncates or skips.
const (
	// SkillNameMaxLen is the maximum length of a skill's frontmatter `name`.
	SkillNameMaxLen = 64
	// SkillDescriptionMaxLen is the maximum length of a skill's frontmatter
	// `description`.
	SkillDescriptionMaxLen = 1024
	// DefaultMaxSkillPackageBytes is the total package size cap (Anthropic's
	// Skills-API upload constraint, <30MB) applied when ParseSkillPackage is
	// called with maxBytes<=0.
	DefaultMaxSkillPackageBytes int64 = 30 * 1024 * 1024
)

// skillNamePattern enforces "lowercase alphanumerics and hyphens only" on a
// skill's frontmatter name.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// skillReservedWords may not appear (as a substring, case-insensitive) in a
// skill's frontmatter name.
var skillReservedWords = []string{"anthropic", "claude"}

// SkillFrontmatter is SKILL.md's YAML frontmatter block. name and description
// are required and validated (see validateSkillFrontmatter); license,
// compatibility, metadata, and allowed-tools are optional passthrough —
// carried verbatim, never interpreted by ctxloom.
type SkillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  []string          `yaml:"allowed-tools,omitempty"`
}

// SkillManifestEntry is one file's identity within a skill package: its path
// relative to the package directory, content hash, and POSIX permission mode.
// The mode's exec bit is load-bearing for scripts/ entries — it must survive
// tree -> archive -> extract -> materialize.
type SkillManifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

// SkillManifest is a skill package's full per-file manifest — what B2's
// signing will cover and B1b's archive codec will pack. Serialize is
// deterministic (entries sorted by path before encoding), so the same tree
// always produces the same bytes, and therefore the same hash, regardless of
// directory-walk or map-iteration order.
type SkillManifest []SkillManifestEntry

// sorted returns a copy of m sorted by Path, leaving m untouched.
func (m SkillManifest) sorted() SkillManifest {
	out := slices.Clone(m)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Serialize returns the canonical, deterministic encoding of the manifest:
// entries sorted by path, then JSON-marshaled (struct field order is fixed by
// declaration order, the same contract mcpContentPayload/hookContentPayload
// rely on). Two SkillManifest values holding the same entries — in any
// insertion order — produce byte-identical Serialize() output.
func (m SkillManifest) Serialize() []byte {
	data, err := json.Marshal(m.sorted())
	if err != nil {
		return skillManifestSerializeFallback(m, err)
	}
	return data
}

// skillManifestSerializeFallback is Serialize's fallback for a marshal error
// that cannot currently happen: SkillManifestEntry holds only strings, and
// encoding/json cannot fail on those (pinned by
// TestSkillManifestEntry_HoldsOnlyStringsSoMarshalCannotFail).
//
// It is factored out so it can be exercised directly, and it is DISTINCT per
// manifest rather than a shared constant, for the same reason BundleMCP/
// BundleHook's ComputeContentHash fallbacks are distinct per server: these
// bytes are a signature PREIMAGE, so one constant standing in for many
// different manifests would make a single signature verify against all of them.
func skillManifestSerializeFallback(m SkillManifest, err error) []byte {
	identity := make([]string, 0, len(m))
	for _, e := range m.sorted() {
		identity = append(identity, e.Path+"@"+e.SHA256+"@"+e.Mode)
	}
	return fmt.Appendf(nil, "ctxloom:skill-manifest-serialize-error:%d:%s:%v",
		len(m), strings.Join(identity, ","), err)
}

// Hash returns the sha256 digest of Serialize() — the manifest hash B2's
// signing preimage will cover.
func (m SkillManifest) Hash() string {
	return hashContent(m.Serialize())
}

// SkillPackage is an Agent Skill package parsed from its source-tree
// directory: SKILL.md's validated frontmatter + body, plus the deterministic
// per-file manifest of every file in the tree (SKILL.md included). It is the
// parsed/runtime twin of BundleSkill (the authored bundle.yaml entry) — B1b's
// archive codec and B2's signing both operate on a SkillPackage, never on the
// directory directly.
type SkillPackage struct {
	Name        string // the package directory's basename
	Frontmatter SkillFrontmatter
	Body        string // SKILL.md content after the frontmatter block
	Manifest    SkillManifest
}

// frontmatterPattern splits a SKILL.md file into its YAML frontmatter block
// and body: a leading "---" line, YAML until a closing "---" line, then the
// rest of the file as body.
var frontmatterPattern = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---[ \t]*\r?\n?(.*)\z`)

// splitFrontmatter separates SKILL.md's YAML frontmatter from its body.
func splitFrontmatter(raw []byte) (frontmatter []byte, body string, err error) {
	text := strings.TrimPrefix(string(raw), "\ufeff") // tolerate a BOM
	m := frontmatterPattern.FindStringSubmatch(text)
	if m == nil {
		return nil, "", fmt.Errorf("SKILL.md must have YAML frontmatter delimited by `---` lines")
	}
	return []byte(m[1]), m[2], nil
}

// normalizeSkillDirName lowercases and folds underscores to hyphens, the
// case/underscore-insensitive comparison the Anthropic Skills-API top-level
// directory rule uses (D5): a directory "Financial_Skill" matches frontmatter
// name "financial-skill".
func normalizeSkillDirName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "-")
}

// validateSkillFrontmatter enforces the Anthropic Agent Skills hard
// constraints against a parsed frontmatter block, given the on-disk directory
// name it was read from. Every violation returns a precise, actionable error;
// none of them are ever silently downgraded to a truncate or skip.
func validateSkillFrontmatter(fm SkillFrontmatter, dirName string) error {
	if fm.Name == "" {
		return fmt.Errorf("skill directory %q: SKILL.md frontmatter is missing the required `name` field", dirName)
	}
	if len(fm.Name) > SkillNameMaxLen {
		return fmt.Errorf("skill directory %q: name %q is %d chars, exceeds the %d char limit", dirName, fm.Name, len(fm.Name), SkillNameMaxLen)
	}
	if !skillNamePattern.MatchString(fm.Name) {
		return fmt.Errorf("skill directory %q: name %q must be lowercase alphanumerics and hyphens only", dirName, fm.Name)
	}
	lowerName := strings.ToLower(fm.Name)
	for _, reserved := range skillReservedWords {
		if strings.Contains(lowerName, reserved) {
			return fmt.Errorf("skill directory %q: name %q must not contain the reserved word %q", dirName, fm.Name, reserved)
		}
	}
	if normalizeSkillDirName(fm.Name) != normalizeSkillDirName(dirName) {
		return fmt.Errorf("skill directory %q: SKILL.md name %q must match its directory name (case/underscore-insensitive)", dirName, fm.Name)
	}
	if fm.Description == "" {
		return fmt.Errorf("skill directory %q: SKILL.md frontmatter is missing the required `description` field", dirName)
	}
	if len(fm.Description) > SkillDescriptionMaxLen {
		return fmt.Errorf("skill directory %q: description is %d chars, exceeds the %d char limit", dirName, len(fm.Description), SkillDescriptionMaxLen)
	}
	return nil
}

// ParseSkillPackage parses an Agent Skill package rooted at dir (the skill's
// own directory, e.g. "<bundle-dir>/skills/<name>") on fsys: it reads and
// validates SKILL.md's frontmatter, then enumerates every file in the tree
// (SKILL.md included) into a deterministic, sha256+mode-stamped manifest.
// This is the one parse both authoring (skill create/sync) and the loader
// (resolving a bundle's skill from its source tree) go through.
//
// maxBytes caps the total package size (Anthropic's Skills-API <30MB
// constraint, simulate a smaller cap in tests rather than building a 30MB
// fixture); maxBytes<=0 uses DefaultMaxSkillPackageBytes.
//
// Every validation failure returns a loud, actionable error — a missing
// SKILL.md, an invalid frontmatter field, or an oversized package are never
// silently skipped or truncated (silent-no-op is this codebase's
// characteristic bug).
func ParseSkillPackage(fsys afero.Fs, dir string, maxBytes int64) (*SkillPackage, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSkillPackageBytes
	}
	name := filepath.Base(filepath.Clean(dir))

	skillMDPath := filepath.Join(dir, "SKILL.md")
	raw, err := afero.ReadFile(fsys, skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("skill directory %q: SKILL.md is required: %w", name, err)
	}

	frontRaw, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("skill directory %q: %w", name, err)
	}
	var fm SkillFrontmatter
	if err := yaml.Unmarshal(frontRaw, &fm); err != nil {
		return nil, fmt.Errorf("skill directory %q: invalid SKILL.md frontmatter: %w", name, err)
	}
	if err := validateSkillFrontmatter(fm, name); err != nil {
		return nil, err
	}

	manifest, err := buildSkillManifest(fsys, dir, name, maxBytes)
	if err != nil {
		return nil, err
	}

	return &SkillPackage{
		Name:        name,
		Frontmatter: fm,
		Body:        body,
		Manifest:    manifest,
	}, nil
}

// SkillManifestEntryFor renders one skill package file's manifest entry from its
// bytes and POSIX permission mode.
//
// It exists because a skill manifest is now built from TWO places — a directory
// walk (buildSkillManifest, below) and a tree read that has bytes but no
// filesystem (internal/content/convert.Read) — and the two must agree EXACTLY:
// VerifyExtractedManifest compares them field by field and rejects the package
// on any difference. The hash carries a "sha256:" prefix and the mode is
// four-digit octal; a second site spelling either of those from memory produces
// a package that extracts, verifies, fails, and is withheld with an integrity
// error that looks like tampering.
func SkillManifestEntryFor(relPath string, data []byte, perm os.FileMode) SkillManifestEntry {
	return SkillManifestEntry{
		Path:   filepath.ToSlash(relPath),
		SHA256: hashContent(data),
		Mode:   fmt.Sprintf("%04o", perm.Perm()),
	}
}

// buildSkillManifest walks dir on fsys, hashing every regular file into a
// SkillManifestEntry (path relative to dir, sha256, POSIX perm mode),
// enforcing maxBytes against the running total size as it goes.
func buildSkillManifest(fsys afero.Fs, dir, name string, maxBytes int64) (SkillManifest, error) {
	var manifest SkillManifest
	var total int64

	// Every failure below names the skill and the file, like the size-cap error
	// does: a bundle ships many skills, and a bare "permission denied" says
	// which of them the caller must go fix only by accident.
	err := afero.Walk(fsys, dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("skill directory %q: walking %s: %w", name, p, walkErr)
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return fmt.Errorf("skill directory %q: locating %s within the package: %w", name, p, relErr)
		}
		rel = filepath.ToSlash(rel)

		total += info.Size()
		if total > maxBytes {
			return fmt.Errorf("skill directory %q: package size exceeds the %d byte limit", name, maxBytes)
		}

		data, readErr := afero.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("skill directory %q: reading %s: %w", name, rel, readErr)
		}

		manifest = append(manifest, SkillManifestEntryFor(rel, data, info.Mode()))
		return nil
	})
	if err != nil {
		return nil, err
	}

	return manifest.sorted(), nil
}

// ResolveSkillDir returns the on-disk directory for a bundle's skill entry:
// entry.Path if set, defaulting to "skills/<skillName>", confined to
// bundleDir. A skill's Path is bundle-tree data — potentially
// remote-originated — so it must never escape the bundle directory on join;
// this mirrors agent.SafeCommandRelPath's confinement contract (empty/absolute
// paths and any ".." path element are rejected, and the joined result is
// re-verified to stay under bundleDir).
func ResolveSkillDir(bundleDir, skillName string, entry BundleSkill) (string, error) {
	rel := entry.Path
	if rel == "" {
		rel = path.Join("skills", skillName)
	}
	dir, ok := safeSkillRelJoin(bundleDir, rel)
	if !ok {
		return "", fmt.Errorf("skill %q: path %q escapes the bundle directory", skillName, rel)
	}
	return dir, nil
}

// safeSkillRelJoin joins rel onto base only if the result is a relative path
// confined to base: rejects empty/absolute rel, any ".." path element, and any
// join whose cleaned result resolves outside base.
func safeSkillRelJoin(base, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", false
	}
	for part := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return "", false
		}
	}
	joined := filepath.Join(base, filepath.FromSlash(rel))
	relCheck, err := filepath.Rel(base, joined)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", false
	}
	return joined, true
}
