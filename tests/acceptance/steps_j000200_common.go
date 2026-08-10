//go:build acceptance

// J000200 shared harness: the "developer's assistant only sees what it's handed,
// signed, and trusted" journey (j000200_setup.feature, j000300_source_augmentation.feature).
//
// Deliberately does NOT drive a fresh `ctxloom init` for its hermetic
// scenarios: a first-time init on a NEW .ctxloom dir always clones the seeded
// "ctxloom-default" remote (internal/cli/init.go's setupNewCtxloomDir ->
// cloneConfiguredRemotes/pullSeededDependencies), so any scenario driving a
// first-time init reaches the network and cannot be hermetic. J000200's scenarios
// are not @network, so this harness authors config.yaml/profile/agent
// scaffolding directly (mirroring steps_fixture.go's minimalConfig approach)
// and only drives a REAL `ctxloom init` on an ALREADY-existing .ctxloom dir
// (which takes init.go's alreadyExists branch — no clone, no pull) when a
// scenario specifically needs to exercise the discovery-session launch path.
package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j000200Source is one named J000200 source fixture: a seeded git remote carrying one
// bundle, optionally signed and/or trusted, plus enough bookkeeping for the
// "adds ... as a source" wiring and later delivery assertions.
type j000200Source struct {
	name       string
	url        string // file:// seeded remote URL
	bundleName string
	marker     string // the distinctive content this source's item carries
	itemKind   string // "fragments" or "commands" — for building item refs
	itemName   string
	signer     *testenv.TestSigner
	principal  string
}

// source returns (lazily creating) the named j000200Source for this scenario.
func (w *World) source(name string) *j000200Source {
	if w.j000200Sources == nil {
		w.j000200Sources = map[string]*j000200Source{}
	}
	s, ok := w.j000200Sources[name]
	if !ok {
		s = &j000200Source{name: name, bundleName: "src"}
		w.j000200Sources[name] = s
	}
	return s
}

// runOK runs a ctxloom command and fails loudly on a non-zero exit — the J000200
// steps' analogue of steps_fixture.go's runFixture, operating on *World
// directly rather than a step context.
func runOK(w *World, args ...string) error {
	_ = w.env.Run(args...)
	if code := w.env.LastExitCode(); code != 0 {
		return fmt.Errorf("%v failed (exit %d): %s", args, code, w.env.LastOutput())
	}
	return nil
}

// fragmentSourceYAML builds a bundle manifest carrying one fragment named
// "marker" whose content IS the marker string — the payload J000200's trust-posture
// and delivery scenarios assert reached (or was withheld from) the assembled
// context / mock engine.
func fragmentSourceYAML(marker string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  marker:\n    content: %q\n", marker)
}

// commandSourceYAML builds a bundle manifest carrying an "agent-setup" command
// whose content is the marker string — j000300's augmentation payload.
func commandSourceYAML(marker string) string {
	return fmt.Sprintf("version: \"1.0.0\"\ncommands:\n  agent-setup:\n    content: %q\n", marker)
}

// seedSource seeds a remote for a named J000200 source carrying bundleYAML, whose
// single item is (kind, item) with the given marker content, optionally
// signed and/or trusted. Records the source in World for later "adds ... as a
// source" wiring and assertions.
func seedSource(w *World, name, kind, item, marker, bundleYAML string, sign, trustAsProject bool) (*j000200Source, error) {
	src := w.source(name)
	src.marker = marker
	src.itemKind = kind
	src.itemName = item
	rel := ".ctxloom/content/bundles/" + src.bundleName + ".yaml"
	files := map[string]string{rel: bundleYAML}

	var url string
	var err error
	if sign {
		signer, serr := testenv.GenerateTestSigner()
		if serr != nil {
			return nil, fmt.Errorf("generate signer for %q: %w", name, serr)
		}
		src.signer = signer
		url, err = w.env.SeedSignedRemote(files, []string{rel}, signer)
	} else {
		url, err = w.env.SeedRemote(files)
	}
	if err != nil {
		return nil, fmt.Errorf("seed remote %q: %w", name, err)
	}
	src.url = url
	if w.remoteBare == nil {
		w.remoteBare = map[string]string{}
	}
	w.remoteBare[name] = strings.TrimPrefix(url, "file://")

	if trustAsProject {
		if src.signer == nil {
			return nil, fmt.Errorf("cannot trust %q: it was not signed", name)
		}
		src.principal = name + "@example.com"
		if err := w.env.TrustSigner(src.signer, src.principal, true); err != nil {
			return nil, fmt.Errorf("trust the signing key for %q: %w", name, err)
		}
	}
	return src, nil
}

// addSourceAsRemote wires an already-seeded source into the project exactly
// as a developer adding a personal/company repo: `remote add` (an address),
// `profile modify <profile> --add-bundle <remote>/<bundle>` (reference it from
// the composed profile), then `deps pull` (fetch + lock the closure). This
// is the ONLY step that can make the source's content reachable — adding a
// remote never implies trust (spec §11); whether it is EXPOSED is decided
// later, per item, by the trust gate at materialize/assemble time.
func addSourceAsRemote(w *World, name, profile string) error {
	src := w.j000200Sources[name]
	if src == nil {
		return fmt.Errorf("source %q was never seeded", name)
	}
	if err := runOK(w, "remote", "create", name, src.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", profile, "--add-bundle", name+"/"+src.bundleName); err != nil {
		return err
	}
	return runOK(w, "deps", "pull")
}

// buildJ000200Config renders a hermetic config.yaml carrying one LLM entry labeled
// label (type engineType), a "default" agent bound to it, and default_agent
// set — the same shape `ctxloom init` would produce, minus the network-
// touching ctxloom-default remote/parent-profile scaffolding (see file doc).
func buildJ000200Config(label, engineType string) string {
	var b strings.Builder
	b.WriteString("version: 4\n")
	b.WriteString("llm:\n  configs:\n")
	fmt.Fprintf(&b, "    %s:\n      type: %s\n", label, engineType)
	fmt.Fprintf(&b, "  defaults:\n    primary: %s\n    fast: %s\n", label, label)
	fmt.Fprintf(&b, "agents:\n  default:\n    engine: %s\n    profiles:\n      - default\n", label)
	b.WriteString("default_agent: default\n")
	return b.String()
}

// scaffoldProjectWithConfig bootstraps a hermetic J000200 project from a fully
// rendered config.yaml (idempotent — a second call on an already-initialized
// World is a no-op): git init, write the config, then a self-contained
// "default" profile/agent so sources can be layered on top via
// addSourceAsRemote.
func scaffoldProjectWithConfig(w *World, configYAML string) error {
	if w.env.FileExists(".ctxloom/config.yaml") {
		return nil
	}
	if err := w.env.InitGitRepo(); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/config.yaml", configYAML); err != nil {
		return err
	}
	if err := runOK(w, "bundle", "create", "seed", "-d", "J000200 seed bundle"); err != nil {
		return err
	}
	return runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J000200 default profile")
}

// ensureProjectWithEngine is scaffoldProjectWithConfig for the common case: a
// single declared engine, rendered via buildJ000200Config.
func ensureProjectWithEngine(w *World, label, engineType string) error {
	return scaffoldProjectWithConfig(w, buildJ000200Config(label, engineType))
}

// materializeDefault materializes the "default" profile into target and
// returns the resulting CLAUDE.md content (materialize itself is expected to
// exit 0 even when it withholds pending content — that is a warning, not a
// failure — so only a read failure after the run is fatal here).
func materializeDefault(w *World, target string) (string, error) {
	_ = w.env.Run("profile", "materialize", "default", "--target", target)
	body, err := w.env.ReadFile(filepath.Join(target, "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("read materialized %s/CLAUDE.md (materialize output:\n%s): %w", target, w.env.LastOutput(), err)
	}
	return body, nil
}

// addMockAlongside appends an additional "mock" LLM entry to the project's
// EXISTING config.yaml (preserving every other line — agents, profiles, the
// real engine's own llm entry, etc.: a plain text append rather than a
// parse/marshal round-trip, so nothing about the file the real engine's
// config lives in is disturbed) and points CTXLOOM_MOCK_RECORD_FILE at a
// fresh path via env (see TestEnvironment.SetEnv — every ctxloom subprocess
// this environment spawns, and anything IT self-invokes with no explicit
// spawn env, inherits it; the mock backend's Execute falls back to
// os.Getenv for any CTXLOOM_MOCK_* key its config's own env map doesn't
// carry, internal/lm/backends/mock.go's getEnvFromMap). Used by scenarios
// that need to observe delivery via the mock while the rest of the project
// stays configured for a real (declared) engine.
func addMockAlongside(w *World) (recordFile string, err error) {
	recordFile = filepath.Join(w.env.Root, "mock-record.txt")
	body, err := w.env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		return "", fmt.Errorf("read config.yaml: %w", err)
	}
	if !strings.Contains(body, "\nllm:\n") && !strings.HasPrefix(body, "llm:\n") {
		return "", fmt.Errorf("config.yaml has no top-level llm: block to append alongside")
	}
	body = strings.Replace(body, "llm:\n  configs:\n", "llm:\n  configs:\n    mock:\n      type: mock\n", 1)
	if err := w.env.WriteFile(".ctxloom/config.yaml", body); err != nil {
		return "", fmt.Errorf("write config.yaml: %w", err)
	}
	w.env.SetEnv("CTXLOOM_MOCK_RECORD_FILE", recordFile)
	return recordFile, nil
}

// runFreshMockSession points the "default" agent at the mock backend (added
// via addMockAlongside) and runs a plain `ctxloom run` — a freshly launched
// engine process, precisely what a restart is — returning the mock's
// recorded input. This is the harness's stand-in for a "restart into the
// newly configured session": init itself no longer offers a relaunch prompt
// at all (offerSessionRelaunch was deleted, init-as-skill slice ④ — init
// hands off to the setup session and exits, full stop), so there is nothing
// left to drive interactively here even in principle. See the scenario-level
// comments in steps_j000200_setup.go for what this substitution still proves.
func runFreshMockSession(w *World) (string, error) {
	recordFile, err := addMockAlongside(w)
	if err != nil {
		return "", err
	}
	// `agent create` and `agent edit` are NOT an upsert (the verb-spine reorg
	// split the old upsert `agent set` in two), and this fixture runs both
	// against a project that may or may not already have a "default" agent
	// from the setup interview. Point it at whichever verb applies rather
	// than at whichever happens to work today.
	if err := repointDefaultAgentAtMock(w); err != nil {
		return "", err
	}
	// --agent (not --profile) is required to actually resolve THIS binding's
	// engine (mock, just repointed above): --profile bypasses agent
	// resolution entirely and runs against the project's own default LLM
	// (internal/cli/run.go marks --agent/--profile mutually exclusive
	// precisely because they are two different resolution paths).
	_ = w.env.Run("run", "--one-shot", "--agent", "default", "continue")
	data, err := os.ReadFile(recordFile)
	if err != nil {
		return "", fmt.Errorf("mock recorded no input (run output:\n%s): %w", w.env.LastOutput(), err)
	}
	return string(data), nil
}

// ptyWaitTimeout bounds every PTY-driven wait in this file: the discovery
// session spawns a real subprocess plugin handshake (`ctxloom llm serve
// mock`), which — mirroring tests/integration/viewer_pty_test.go's
// ptyRunTimeout — can take over a second under CI load.
const ptyWaitTimeout = 20 * time.Second

// driveDiscoverySessionViaMock drives a REAL `ctxloom init` (on an
// ALREADY-initialized .ctxloom dir, so init.go's alreadyExists branch runs —
// no network clone) over a real pty: the project's LLM default must already
// be the mock backend (see addMockAlongside / buildJ000200Config) with
// CTXLOOM_MOCK_RECORD_FILE set (via TestEnvironment.SetEnv). init.go's
// launchDiscovery pings the mock backend's auth (a cheap oneshot the mock
// backend answers immediately) before it launches the interactive session, so
// the FINAL record-file write — the one this function reads back — is the
// interactive launch's, not the ping's; then it spawns the mock and hands it
// discoverySessionPrompt(cfg): the composed built-in + every installed
// agent-setup command (repo bundle or companion loadout) this harness is
// proving delivery of. init now hands off and exits with no further prompt
// (the post-discovery relaunch offer and the inline review offer are both
// deleted — init-as-skill slice ④), so this just waits for the process to
// exit. Returns the mock's full recorded-input file.
func driveDiscoverySessionViaMock(w *World, recordFile string) (string, error) {
	sess, err := w.env.RunPTY(100, 30, "init")
	if err != nil {
		return "", fmt.Errorf("start pty session: %w", err)
	}
	defer sess.Close()

	exited, waitErr := sess.Wait(ptyWaitTimeout)
	if !exited {
		return "", fmt.Errorf("ctxloom init did not exit within %s; captured output:\n%s", ptyWaitTimeout, sess.Output())
	}
	if waitErr != nil {
		return "", fmt.Errorf("ctxloom init exited with an error: %w; captured output:\n%s", waitErr, sess.Output())
	}

	data, err := os.ReadFile(recordFile)
	if err != nil {
		return "", fmt.Errorf("mock recorded no input — the discovery session may never have launched it; captured output:\n%s: %w",
			sess.Output(), err)
	}
	return string(data), nil
}

// promptSection extracts the "=== Prompt ===" section the mock backend
// records (internal/lm/backends/mock.go's recordMockInput) — the exact text
// the discovery session handed the engine as its interview prompt. The
// marker's absence is a harness/product break (a mock that recorded
// something other than a prompt, or a record-file format change) — not an
// empty prompt — so it fails loud rather than returning "".
func promptSection(recorded string) (string, error) {
	i := strings.Index(recorded, "=== Prompt ===\n")
	if i < 0 {
		return "", fmt.Errorf("the mock's record carries no \"=== Prompt ===\" section; recorded:\n%s", recorded)
	}
	return recorded[i+len("=== Prompt ===\n"):], nil
}

// repointDefaultAgentAtMock binds the "default" agent to the mock engine,
// creating it when absent and editing it when present. `agent create` refuses
// an existing name and `agent edit` refuses an absent one — deliberately, so
// neither silently does the other's job — which means a fixture that has to
// work either way must ask which case it is in.
func repointDefaultAgentAtMock(w *World) error {
	verb := "create"
	if err := w.env.Run("agent", "show", "default"); err == nil {
		verb = "edit"
	}
	return runOK(w, "agent", verb, "default", "--engine", "mock", "--profiles", "default")
}
