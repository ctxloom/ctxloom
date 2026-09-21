package isolation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The macOS credential arm, built against a FAKE `security` on this Linux
// host: a shell script backed by a directory, one file per item, that logs
// every argv it is called with. Every command shape claude's own store uses
// (2.1.278) is pinned here by the argv the fake records; the real Mac run is
// unverifiable on this host, and this file says so rather than pretending.

// fakeSecurityScript stands in for /usr/bin/security. Items live at
// $FAKE_KEYCHAIN_DIR/<service>; every invocation appends its argv to
// $FAKE_KEYCHAIN_LOG. Exit 44 is security's own "item not found".
const fakeSecurityScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_KEYCHAIN_LOG"
cmd="$1"; shift
acct=""; svc=""; hexdata=""; want_w=0; update=0
while [ $# -gt 0 ]; do
  case "$1" in
    -a) acct="$2"; shift 2;;
    -s) svc="$2"; shift 2;;
    -X) hexdata="$2"; shift 2;;
    -w) want_w=1; shift;;
    -U) update=1; shift;;
    *) shift;;
  esac
done
case "$cmd" in
  find-generic-password)
    if [ -f "$FAKE_KEYCHAIN_DIR/$svc" ]; then
      [ "$want_w" = 1 ] && cat "$FAKE_KEYCHAIN_DIR/$svc"; exit 0
    fi
    echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2; exit 44;;
  add-generic-password)
    if [ -f "$FAKE_KEYCHAIN_DIR/$svc" ] && [ "$update" != 1 ]; then
      echo "security: SecKeychainItemCreateFromContent: The specified item already exists in the keychain." >&2; exit 45
    fi
    perl -e 'print pack("H*", $ARGV[0])' "$hexdata" > "$FAKE_KEYCHAIN_DIR/$svc"; exit 0;;
  delete-generic-password)
    if [ -f "$FAKE_KEYCHAIN_DIR/$svc" ]; then rm -f "$FAKE_KEYCHAIN_DIR/$svc"; exit 0; fi
    echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2; exit 44;;
  dump-keychain)
    for f in "$FAKE_KEYCHAIN_DIR"/*; do
      [ -f "$f" ] || continue
      echo "keychain: \"/Users/x/Library/Keychains/login.keychain-db\""
      echo "class: \"genp\""
      echo "attributes:"
      echo "    \"acct\"<blob>=\"x\""
      echo "    \"svce\"<blob>=\"$(basename "$f")\""
    done; exit 0;;
esac
echo "fake security: unknown command $cmd" >&2; exit 2
`

// fakeKeychain is the fake's store and log.
type fakeKeychain struct {
	dir, log string
}

// withFakeSecurity installs the fake `security`, selects the macOS arm on
// this host, and fixes $USER, so the account and every service the seed
// derives are the ones the assertions below compute.
func withFakeSecurity(t *testing.T) *fakeKeychain {
	t.Helper()
	testsupport.Isolate(t)
	bin := t.TempDir()
	script := filepath.Join(bin, "security")
	require.NoError(t, os.WriteFile(script, []byte(fakeSecurityScript), 0o755))
	fk := &fakeKeychain{dir: t.TempDir(), log: filepath.Join(t.TempDir(), "security.log")}
	t.Setenv("FAKE_KEYCHAIN_DIR", fk.dir)
	t.Setenv("FAKE_KEYCHAIN_LOG", fk.log)
	t.Setenv("USER", "alice")
	prevTool, prevPlatform := keychainTool, keychainPlatform
	keychainTool = script
	keychainPlatform = func() bool { return true }
	t.Cleanup(func() { keychainTool, keychainPlatform = prevTool, prevPlatform })
	return fk
}

// put writes an item directly into the fake store, as the user's own claude
// would have.
func (fk *fakeKeychain) put(t *testing.T, service, data string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(fk.dir, service), []byte(data), 0o600))
}

// item reads an item out of the fake store; ok is false when absent.
func (fk *fakeKeychain) item(t *testing.T, service string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fk.dir, service))
	if os.IsNotExist(err) {
		return "", false
	}
	require.NoError(t, err)
	return string(data), true
}

// calls is every argv the fake recorded, one per line.
func (fk *fakeKeychain) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(fk.log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// wantService is the session item's service for configDir, computed here
// independently of the implementation: the CLI's own derivation.
func wantService(base, configDir string) string {
	sum := sha256.Sum256([]byte(configDir))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

const hostKeychainCredential = `{"claudeAiOauth":{"accessToken":"kc-access","refreshToken":"kc-refresh","refreshTokenExpiresAt":2,"expiresAt":1}}`

func claudeKeychainStore(t *testing.T) engine.KeychainStore {
	t.Helper()
	seed := claudeSeed(t)
	require.NotNil(t, seed.Keychain, "claude declares its macOS store")
	return *seed.Keychain
}

// TestKeychainAccount_IsTheUserNameOrTheCLIsFallback pins the CLI's own
// account rule: $USER when it matches ^[a-zA-Z0-9._-]+$, else the fixed
// fallback the CLI uses.
func TestKeychainAccount_IsTheUserNameOrTheCLIsFallback(t *testing.T) {
	assert.Equal(t, "alice", keychainAccount("alice"))
	assert.Equal(t, "a.b-c_9", keychainAccount("a.b-c_9"))
	assert.Equal(t, "claude-code-user", keychainAccount(""))
	assert.Equal(t, "claude-code-user", keychainAccount("al ice"))
	assert.Equal(t, "claude-code-user", keychainAccount("al/ice"))
	assert.Equal(t, "claude-code-user", keychainAccount("ålice"))
}

// TestKeychainService_HashesTheNFCConfigDir pins the session item's service:
// the base name, a dash, the first eight hex digits of the sha256 of the
// NFC-normalised config dir. Two spellings of one dir (a precomposed and a
// decomposed "é") name the same item, because the CLI normalises the same
// way.
func TestKeychainService_HashesTheNFCConfigDir(t *testing.T) {
	assert.Equal(t, wantService("Claude Code-credentials", "/Users/alice/.ctxloom/sessions/h/home/claude"),
		keychainService("Claude Code-credentials", "/Users/alice/.ctxloom/sessions/h/home/claude"))
	precomposed := "/Users/rené/home/claude"
	decomposed := "/Users/rené/home/claude"
	assert.Equal(t, keychainService("S", precomposed), keychainService("S", decomposed), "NFC: one item whatever the spelling")
	assert.Equal(t, wantService("S", precomposed), keychainService("S", decomposed))
}

// TestKeychainSeed_ReadsTheDefaultItemAndWritesTheSessionsProjected: (a)
// the default item is read with `find-generic-password -a <user> -s <base>
// -w`, (b) the refresh token is stripped, (c) the session's item is written
// with `add-generic-password -U -a <user> -s <base>-<hash> -X <hex JSON>`.
func TestKeychainSeed_ReadsTheDefaultItemAndWritesTheSessionsProjected(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	fk.put(t, store.Service, hostKeychainCredential)
	configDir := filepath.Join(t.TempDir(), "home", "claude")

	result, res, err := provisionKeychainSeed("claude-code", store, configDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Close() })
	assert.Equal(t, seedOK, result)
	assert.Equal(t, DeliveryReplicated, res.Delivery)
	assert.Equal(t, keychainMechanism, res.Mechanism)

	service := wantService(store.Service, configDir)
	placed, ok := fk.item(t, service)
	require.True(t, ok, "the session's item %q was written", service)
	var cred map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(placed), &cred))
	assert.Equal(t, "kc-access", cred["claudeAiOauth"]["accessToken"])
	assert.NotContains(t, cred["claudeAiOauth"], "refreshToken")
	assert.NotContains(t, cred["claudeAiOauth"], "refreshTokenExpiresAt")
	host, _ := fk.item(t, store.Service)
	assert.Equal(t, hostKeychainCredential, host, "the default item is read, never rewritten")

	projected, err := store.Project([]byte(hostKeychainCredential))
	require.NoError(t, err)
	calls := fk.calls(t)
	assert.Contains(t, calls, "find-generic-password -a alice -s "+store.Service+" -w")
	assert.Contains(t, calls, "add-generic-password -U -a alice -s "+service+" -X "+hex.EncodeToString(projected))
}

// A host with no default item is "nothing seedable": seedNoSource, no item
// written, nothing left running.
func TestKeychainSeed_NoDefaultItemIsNoSource(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	configDir := filepath.Join(t.TempDir(), "home", "claude")

	result, res, err := provisionKeychainSeed("claude-code", store, configDir)
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result)
	assert.NoError(t, res.Close())
	_, ok := fk.item(t, wantService(store.Service, configDir))
	assert.False(t, ok, "no session item is written when there is nothing to seed it from")
	for _, c := range fk.calls(t) {
		assert.NotContains(t, c, "add-generic-password")
	}
}

// (d) Replication: the default item is polled and a change is rewritten
// into the session's item, projected again; an unchanged default item is
// not rewritten.
func TestKeychainSeed_PollsTheDefaultItemAndRewritesTheSessions(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	fk.put(t, store.Service, hostKeychainCredential)
	configDir := filepath.Join(t.TempDir(), "home", "claude")
	prev := keychainPollInterval
	keychainPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { keychainPollInterval = prev })

	_, res, err := provisionKeychainSeed("claude-code", store, configDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Close() })
	service := wantService(store.Service, configDir)

	time.Sleep(6 * keychainPollInterval)
	adds := 0
	for _, c := range fk.calls(t) {
		if strings.HasPrefix(c, "add-generic-password") {
			adds++
		}
	}
	assert.Equal(t, 1, adds, "an unchanged default item is not rewritten on every poll")

	fk.put(t, store.Service, `{"claudeAiOauth":{"accessToken":"kc-access-2","refreshToken":"kc-refresh-2","expiresAt":3}}`)
	require.Eventually(t, func() bool {
		placed, ok := fk.item(t, service)
		return ok && strings.Contains(placed, "kc-access-2")
	}, 5*time.Second, 10*time.Millisecond, "the host's rotation never reached the session item")
	placed, _ := fk.item(t, service)
	assert.NotContains(t, placed, "kc-refresh-2", "the re-copy is projected again")
}

// (e) Teardown: the run that CREATED the session's item deletes it at
// Close — `delete-generic-password -a <user> -s <base>-<hash>` — and a run
// that found the item already there (a second run of the same session)
// leaves it for the creator, and for the reaper.
func TestKeychainSeed_CloseDeletesTheItemItCreated(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	fk.put(t, store.Service, hostKeychainCredential)
	configDir := filepath.Join(t.TempDir(), "home", "claude")
	service := wantService(store.Service, configDir)

	_, first, err := provisionKeychainSeed("claude-code", store, configDir)
	require.NoError(t, err)
	_, second, err := provisionKeychainSeed("claude-code", store, configDir)
	require.NoError(t, err)

	require.NoError(t, second.Close())
	_, ok := fk.item(t, service)
	assert.True(t, ok, "a run that did not create the item leaves it for the creator")

	require.NoError(t, first.Close())
	_, ok = fk.item(t, service)
	assert.False(t, ok, "the creator deletes the session's item at teardown")
	assert.Contains(t, fk.calls(t), "delete-generic-password -a alice -s "+service)
	require.NoError(t, first.Close(), "Close is idempotent")
}

// (f) The reaper's hook: every keychain-storing engine's session item for a
// harp is deleted, by the service recomputed from the harp's session home;
// an item that is already gone is not an error.
func TestReapKeychainItems_DeletesEveryEnginesItemForTheHarp(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	home := withFakeHome(t)
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".ctxloom", "sessions", "dead-harp", "home", "claude")
	service := wantService(store.Service, configDir)
	fk.put(t, service, `{"claudeAiOauth":{"accessToken":"stale"}}`)

	require.NoError(t, ReapKeychainItems("dead-harp"))
	_, ok := fk.item(t, service)
	assert.False(t, ok, "the reaped harp's item is deleted")
	assert.Contains(t, fk.calls(t), "delete-generic-password -a alice -s "+service)

	require.NoError(t, ReapKeychainItems("dead-harp"), "an item already gone is not an error")
}

// (g) Doctor's listing: items whose service carries the base prefix but
// whose hash matches no config dir under the sessions root are orphans; a
// live session's item is not.
func TestOrphanedKeychainItems_NamesItemsNoSessionDirExplains(t *testing.T) {
	fk := withFakeSecurity(t)
	store := claudeKeychainStore(t)
	home := withFakeHome(t)
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".ctxloom", "sessions")
	liveDir := filepath.Join(sessions, "live-harp", "home", "claude")
	require.NoError(t, os.MkdirAll(liveDir, 0o700))
	live := wantService(store.Service, liveDir)
	orphan := wantService(store.Service, filepath.Join(sessions, "gone-harp", "home", "claude"))
	fk.put(t, live, `{}`)
	fk.put(t, orphan, `{}`)
	fk.put(t, store.Service, `{}`)

	got, err := OrphanedKeychainItems()
	require.NoError(t, err)
	assert.Equal(t, []string{orphan}, got, "only the item no session dir explains is an orphan; the default item and the live one are not")
	assert.Contains(t, fk.calls(t), "dump-keychain")
}

// Off darwin the arm is inert: nothing is read, nothing is listed.
func TestKeychainArm_IsSelectedOnlyOnDarwin(t *testing.T) {
	fk := withFakeSecurity(t)
	keychainPlatform = func() bool { return false }
	require.NoError(t, ReapKeychainItems("any-harp"))
	got, err := OrphanedKeychainItems()
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, fk.calls(t), "off darwin the security tool is never run")
}
