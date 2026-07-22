package antigravity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// antigravitySkillsDir is the workspace skills directory agy reads, relative
// to the workspace root.
const antigravitySkillsDir = AgentsDir + "/skills"

// antigravityManifest tracks ctxloom-written COMMAND files for clean removal,
// distinct from antigravitySkillManifest (skillfiles.go) so the two surfaces'
// cleanup never collides — mirrors kiro's kiroManifest / kiroSkillManifest
// split (kiro/surfaces.go, kiro/skillfiles.go).
const antigravityManifest = ".ctxloom-manifest"

// AntigravityCommands writes commands for Antigravity CLI as Agent Skill
// directories under .agents/skills/.
type AntigravityCommands struct{}

// RegisterFromContent writes skill files from host-resolved command exports.
// The host maps bundle content (with antigravity enablement + metadata) to
// these agent-agnostic exports, so this stays config/bundle-free.
func (s *AntigravityCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// WriteCommandFiles writes enabled command exports as agy Agent Skill
// directories (.agents/skills/<name>/SKILL.md, generated YAML frontmatter) and
// records them in the manifest. Previously manifest-listed files are removed
// first, so the written set always mirrors the current exports (see
// agent.WriteManagedCommandFiles for the shared mechanics; the directory is
// shared with user-authored skills, never wiped wholesale).
//
// G3 FIX (was the silent no-op): this writer used to emit flat `<name>.md`
// files, which agy's skill scanner NEVER discovers — agy only walks
// `.agents/skills/<name>/SKILL.md` DIRECTORIES (VERIFIED against agy's own
// bundled docs, see skillfiles.go's doc comment; every builtin skill is a
// dir). ctxloom slash-command exports landed on disk but were invisible to
// agy. Fixed by rendering the SAME `<name>/SKILL.md` shape kiro already used —
// agent.RenderCommandAsSkillFile is the shared renderer both engines' writers
// call (reprise flagged the two as byte-for-byte duplicates when this was a
// local copy), so the two engines' generated frontmatter can't silently drift
// apart.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(antigravitySkillsDir))
	return agent.WriteManagedCommandFiles(fs, skillsDir, antigravityManifest, cmds,
		agent.RenderCommandAsSkillFile,
		agent.WithManifestTrailingNewline())
}

// agy's command/skill name-collision guard: agy has ONE native directory,
// .agents/skills/<name>/, that would serve BOTH a command rendered as
// SKILL.md (this file) and a true Agent Skill package (skillfiles.go) of the
// same name — both now want .agents/skills/<name>/SKILL.md. Resolved as
// SKILL-WINS by the shared agent.FilterCommandsClaimedBySkills (invoked from
// surfaces.go's NewSurfaces via agent.NewSkillShapedCommandsAndSkills — kiro's
// identical D6 resolution uses the same helper): a name claimed by an enabled
// skill is dropped from the commands set before WriteCommandFiles ever sees
// it, so antigravityManifest (commands) and antigravitySkillManifest (skills)
// never both claim the same path — a later re-materialize (skill
// disabled/removed) lets the command reclaim the name cleanly, and neither
// writer's cleanup can strand the other's live file.

// tough-cloud S5: AntigravitySessionHistory (the transcript_full.jsonl
// scraper — GetCurrentSession/ListSessions/GetSession/GetSessionByPath/
// TranscriptPathFromHook, its transcript-line parser, and the SessionHistory
// wiring in backend.go) was DELETED outright, not demoted to an importer —
// the user's explicit decision (tall-grab: this reader mis-keyed the global
// brain store and could hand back the wrong workspace's conversation; agy's
// structured Chat driver already streams the real conversation through ACP,
// captured canonically instead, see internal/transcript). Antigravity's
// Backend.History() now returns nil.
//
// agyConversationMap survives the deletion: it is NOT a session-history
// reader, it is the live continuation lookup chat.go's
// resolveChatConversationID depends on every oneshot turn (agy -p never
// prints its conversation id back, so Chat detects/refreshes it by re-reading
// this cache file after each turn — see chat.go). It happens to read from the
// same agy-owned directory tree the deleted scraper did, but never touches a
// transcript.

// agyConversationMap resolves agy's own workDir -> conversation-UUID cache
// (cache/last_conversations.json). The embedded agent.SessionStore supplies
// only its FS/HomeDir test-injection seam (ResolveHomeDir) — this type never
// parses a transcript.
type agyConversationMap struct {
	agent.SessionStore
}

// agyConversationMapOption configures agyConversationMap.
type agyConversationMapOption func(*agyConversationMap)

// withAgyConversationMapHomeDir sets a custom home directory for testing.
func withAgyConversationMapHomeDir(dir string) agyConversationMapOption {
	return func(m *agyConversationMap) { m.HomeDir = dir }
}

// newAgyConversationMap builds the conversation-map reader over the OS
// filesystem (tests override via withAgyConversationMapHomeDir).
func newAgyConversationMap(opts ...agyConversationMapOption) agyConversationMap {
	m := agyConversationMap{SessionStore: agent.NewSessionStore()}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// path returns agy's workspace -> conversation-UUID map file (VERIFIED
// shape: ~/.gemini/antigravity-cli/cache/last_conversations.json, a flat
// JSON object keyed by the ABSOLUTE workspace path agy was invoked against).
// Sibling cache/projects.json maps workspace -> PROJECT uuid — a different
// id, not the conversation id Chat needs.
func (m agyConversationMap) path() (string, error) {
	homeDir, err := m.ResolveHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "cache", "last_conversations.json"), nil
}

// read reads agy's workspace -> conversation-UUID map. Returns a nil map
// (not an error) when the file is simply absent — a fresh install, or a
// workspace agy has never been invoked against, which resolveChatConversationID
// treats as "no entry" (ok=false). A file that EXISTS but fails to parse is a
// real problem (agy changed the format, or the file is corrupt) and is
// surfaced as an error rather than silently discarded.
func (m agyConversationMap) read() (map[string]string, error) {
	path, err := m.path()
	if err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(agent.GetFS(m.FS), path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read last_conversations.json: %w", err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("failed to parse last_conversations.json: %w", err)
	}
	return out, nil
}
