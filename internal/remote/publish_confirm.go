package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// The publish-remote confirmation: the first publish to a given remote URL
// asks a human and RECORDS the answer; every later publish to that same remote
// is silent. A session with nobody to ask REFUSES.
//
// WHAT THIS IS FOR, stated honestly. Publishing is intentionally outward and
// the content is signed to be verified by strangers, so this is
// MISTAKE-PREVENTION, not exfiltration defence — anyone who can run the
// publish can also run `git push` directly. It guards three things:
//
//  1. a typo or a stale remote in config. A typo produces a DIFFERENT URL and
//     therefore prompts; a routine republish to the same URL never does.
//  2. a committable .ctxloom/ config carrying a remote the user never chose —
//     a cloned repo can bring one.
//  3. an AGENT publishing. This project already treats that as needing a human
//     (ltk blocks `git tag` and release commands outright), and generic-git
//     publish is what makes pushing signed content reachable from inside an
//     agent session at all.
//
// It is consistent with the companion trust-on-first-use decision: ask once
// per new thing, remember, refuse rather than assume when nobody can be asked.
//
// TWO ALTERNATIVES WERE REJECTED. Printing the destination and proceeding
// leaves an agent free to publish and trains people to skim routine output.
// An always-confirm with a `--yes` escape hatch puts the flag into every
// script on day one and makes the prompt reflexive.

// PublishRemoteAsk asks a human whether publishing to remoteURL — a remote
// nothing has been published to before — may proceed. It returns the answer;
// an error means the question could not be put (a closed stdin, a read
// failure), which is never an affirmative.
//
// A nil PublishRemoteAsk is the NON-INTERACTIVE case and is the deliberate
// default: package remote has no terminal and must not acquire one. The
// frontend supplies this only when it actually has a human attached (see
// internal/cli), so an agent, an MCP tool call, a CI job and a piped
// invocation all reach the refusal rather than a prompt written into a pipe.
type PublishRemoteAsk func(ctx context.Context, remoteURL string) (bool, error)

// ConfirmedRemotes is the record of which remotes a human confirmed as publish
// destinations: one marker file per confirmed remote URL, under a single
// directory.
//
// The shape follows the approvals store's UNSIGNED-marker path (spec §9.5,
// countersign.Store record kind 2): EXISTENCE IS THE ENTIRE RECORD. Nothing
// here is signed, so anything that can write this directory can forge one.
// That is the documented cost of a decision record that must work with no key
// present — and it is exactly why the store is user-scoped
// (paths.HomePublishRemotesPath) and has no committable twin. A shareable
// "yes, publish there" with no signature would pre-answer the question for
// everyone who clones the repo, which is one of the three mistakes this
// mechanism exists to catch.
type ConfirmedRemotes struct {
	dir string
	fs  afero.Fs
}

// NewConfirmedRemotes builds a store rooted at dir, backed by fs. dir need not
// exist — it is created on the first Record, and a store over a nonexistent
// directory reads as "nothing confirmed yet", which is the correct starting
// state.
//
// An EMPTY dir is NOT that state: it is a store nobody managed to configure
// (the real trigger is an unresolvable $HOME). Because filepath.Join("", x)
// == x, such a store would read the PROCESS WORKING DIRECTORY — and since a
// marker's mere existence is the confirmation, a stray file at a repo root
// would authorise a push. An unconfigured store therefore answers nothing and
// refuses every write, exactly as countersign.Store does for the same reason.
func NewConfirmedRemotes(dir string, fs afero.Fs) *ConfirmedRemotes {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	return &ConfirmedRemotes{dir: dir, fs: fs}
}

// DefaultConfirmedRemotes is the production store, at
// ~/.ctxloom/publish-remotes. An unresolvable home yields an UNCONFIGURED
// store rather than an error, so construction stays total; the fault surfaces
// at the gate, where it fails closed with the home error attached.
func DefaultConfirmedRemotes() *ConfirmedRemotes {
	dir, err := paths.HomePublishRemotesPath()
	if err != nil {
		return &ConfirmedRemotes{dir: "", fs: afero.NewOsFs()}
	}
	return NewConfirmedRemotes(dir, afero.NewOsFs())
}

// Dir reports where this store lives, for messages that must tell a human
// where the answer was (or would be) recorded. Empty means unconfigured.
func (c *ConfirmedRemotes) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// confirmKey is the marker filename for a remote URL: the sha256 of the URL's
// NORMALIZED identity, hex, plus a fixed extension.
//
// Hashing keeps every URL spelling — ports, paths, scp colons, Windows drive
// letters — inside one filesystem-safe name. It is a pure INDEX and never read
// back as identity: the marker's body carries the URL for humans, and no code
// path parses a filename to decide anything.
//
// The key is taken over NormalizeURL rather than the raw string, so the same
// repository reached as "git@host:o/r.git" and "https://host/o/r" is ONE
// confirmed destination — the same identity rule that keys the trust namespace
// and lockfile entries. A URL that normalizes to nothing (an unparseable
// spelling) falls back to its raw text, which cannot silently collide two
// different destinations onto one confirmation.
func confirmKey(remoteURL string) string {
	id := NormalizeURL(remoteURL)
	if id == "" {
		id = strings.TrimSpace(remoteURL)
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".confirmed"
}

// configured reports the "nobody gave this store a directory" fault. See
// NewConfirmedRemotes for why it is a fault and not an empty store.
func (c *ConfirmedRemotes) configured() error {
	if c != nil && c.dir != "" {
		return nil
	}
	dir, err := paths.HomePublishRemotesPath()
	if err != nil {
		return fmt.Errorf("confirmed publish-remote store: %w", err)
	}
	return fmt.Errorf("confirmed publish-remote store: no directory configured (expected %s)", dir)
}

// Confirmed reports whether remoteURL has already been confirmed.
//
// A directory that does not exist yet is "nothing confirmed" (false, nil). Any
// OTHER read fault is returned rather than folded onto false: this store's
// answer decides whether a push happens, and "I could not read the store" must
// not read as "you never confirmed it" — that would ask a human to re-confirm
// a remote they already approved, teaching them to say yes to the prompt on
// reflex, which is the whole value of the mechanism.
func (c *ConfirmedRemotes) Confirmed(remoteURL string) (bool, error) {
	if err := c.configured(); err != nil {
		return false, err
	}
	path := filepath.Join(c.dir, confirmKey(remoteURL))
	ok, err := afero.Exists(c.fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read confirmed publish-remote store %s: %w", c.dir, err)
	}
	return ok, nil
}

// Record persists the confirmation for remoteURL. The body is for HUMANS —
// the URL as written, its normalized identity and when it was confirmed — so
// the store can be read, audited and pruned with `ls` and `cat`. Nothing
// parses it back.
func (c *ConfirmedRemotes) Record(remoteURL string, now time.Time) error {
	if err := c.configured(); err != nil {
		return err
	}
	if err := c.fs.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("create confirmed publish-remote store %s: %w", c.dir, err)
	}
	body := fmt.Sprintf("url: %s\nidentity: %s\nconfirmed_at: %s\n",
		strings.TrimSpace(remoteURL), NormalizeURL(remoteURL), now.UTC().Format(time.RFC3339))
	path := filepath.Join(c.dir, confirmKey(remoteURL))
	if err := afero.WriteFile(c.fs, path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("record confirmed publish remote %s: %w", remoteURL, err)
	}
	return nil
}

// authorizeRemote is the gate every non-GitHub publish passes before any
// network write.
//
// SCOPE: generic-git remotes only. GitHub publish is deliberately untouched —
// it goes through the forge API with a token that must already carry push
// rights to that specific repository, and folding it in here would change the
// behaviour of an existing, acceptance-covered path as a side effect of adding
// a new one. Widening the gate to GitHub is a separate decision with its own
// migration for everyone already publishing there.
func (pm *PublishManager) authorizeRemote(ctx context.Context, remoteURL string, forge ForgeType) error {
	if forge == ForgeGitHub {
		return nil
	}
	store := pm.confirmed
	if store == nil {
		store = DefaultConfirmedRemotes()
	}

	confirmed, err := store.Confirmed(remoteURL)
	if err != nil {
		return fmt.Errorf("cannot tell whether %s is a confirmed publish remote, so refusing to publish to it: %w", remoteURL, err)
	}
	if confirmed {
		return nil
	}
	if pm.ask == nil {
		return unconfirmedRemoteError(remoteURL, store.Dir())
	}

	yes, err := pm.ask(ctx, remoteURL)
	if err != nil {
		return fmt.Errorf("refusing to publish to %s: its confirmation could not be put to a human: %w", remoteURL, err)
	}
	if !yes {
		return fmt.Errorf("refusing to publish to %s: not confirmed", remoteURL)
	}
	// Record BEFORE publishing. A confirmation that is only written after a
	// successful push is lost whenever the push fails, so the retry asks
	// again — and a prompt that reappears after every hiccup is one people
	// learn to dismiss. The human answered the question that was asked ("is
	// this the remote you meant"), and that answer is true regardless of
	// whether the push then succeeded.
	if err := store.Record(remoteURL, time.Now()); err != nil {
		return fmt.Errorf("refusing to publish to %s: it was confirmed but the confirmation could not be recorded (it would be asked again every time): %w", remoteURL, err)
	}
	return nil
}

// unconfirmedRemoteError is the NON-INTERACTIVE refusal: it names the remote,
// says why it stopped, and says how to confirm it. It never assumes yes and
// never writes a prompt into a pipe.
func unconfirmedRemoteError(remoteURL, storeDir string) error {
	where := storeDir
	if where == "" {
		where = "~/" + paths.AppDirName + "/" + paths.PublishRemotesDirName
	}
	return fmt.Errorf(
		"refusing to publish to %s: this remote has never been confirmed as a publish destination, "+
			"and this session has no terminal to confirm it on (an agent, an editor, a CI job or a piped command). "+
			"Check the URL is the one you meant, then run the same publish once from an interactive terminal and answer yes; "+
			"the answer is recorded in %s and you will not be asked again",
		remoteURL, where)
}
