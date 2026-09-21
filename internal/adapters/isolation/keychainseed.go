package isolation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The macOS credential arm.
//
// On darwin claude keeps its credential in the login Keychain, not in
// .credentials.json: a generic password whose account is the user's name and
// whose service is a fixed name for the default config dir — and that name
// suffixed with a hash of the config dir when CLAUDE_CONFIG_DIR relocates it.
// A relocated dir does NOT see the default item (2.1.278). So a session home
// on a Mac is seeded by writing the SESSION's own item, from the default
// one, with the same projection the file arm applies; kept in step by
// polling the default item (the Keychain has no watcher); and cleaned by
// deleting the item — at the creating run's teardown, by the reaper for
// every reaped harp, and named by doctor when neither reached it.
//
// Every shape below is the CLI's own, read from the 2.1.278 bundle: the
// account rule, the service derivation (sha256 of the NFC-normalised dir,
// first eight hex digits), and the three `security` invocations. They are
// invariants of the ENGINE, not of this package, and a claude that changes
// any of them changes where its own item is — which is why each is a named
// constant or function here and not a literal in a call.
//
// UNVERIFIABLE ON THIS HOST: every test of this file runs against a fake
// `security` on Linux. The command shapes are pinned; the Keychain's own
// behaviour is not.

// keychainMechanism names this implementation in a Result.
const keychainMechanism = "keychain-replication"

// keychainFallbackAccount is the CLI's account when $USER is unusable.
const keychainFallbackAccount = "claude-code-user"

// keychainAccountPattern is the CLI's rule for an acceptable $USER.
var keychainAccountPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// keychainPollInterval is how often the default item is re-read. The
// Keychain has no change notification a process can wait on, so polling is
// the whole of replication here; one second is the tradeoff between a
// `security` process per tick for the life of every session and the window
// in which the session presents a token the host has rotated.
var keychainPollInterval = time.Second

// keychainNotFoundExit is `security`'s exit status when no item matches.
const keychainNotFoundExit = 44

// keychainPlatform reports whether the keychain arm applies. A seam so the
// arm can be built and tested on a host that is not a Mac.
var keychainPlatform = func() bool { return runtime.GOOS == "darwin" }

// keychainTool is the `security` binary. A seam for the fake.
var keychainTool = "security"

// KeychainAccount is the Keychain account the CLI uses: $USER when it
// matches keychainAccountPattern, else keychainFallbackAccount. Exported
// for doctor, which names the delete command for an orphaned item.
func KeychainAccount(user string) string {
	if keychainAccountPattern.MatchString(user) {
		return user
	}
	return keychainFallbackAccount
}

// keychainService is the service of a relocated config dir's item: the
// store's default service, "-", and the first eight hex digits of the
// sha256 of the NFC-normalised dir — the CLI's own derivation.
func keychainService(base, configDir string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(configDir)))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// keychainSeed is one session's item for one engine's store.
type keychainSeed struct {
	engine  string
	store   engine.KeychainStore
	account string
	// service is the session item's service.
	service string
}

// newKeychainSeed derives the account and the session item's service for
// configDir.
func newKeychainSeed(name string, store engine.KeychainStore, configDir string) *keychainSeed {
	return &keychainSeed{
		engine:  name,
		store:   store,
		account: KeychainAccount(os.Getenv("USER")),
		service: keychainService(store.Service, configDir),
	}
}

// security runs one `security` command and returns its stdout.
func (k *keychainSeed) security(args ...string) ([]byte, error) {
	cmd := exec.Command(keychainTool, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", keychainTool, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// notFound reports whether err is `security`'s "no such item".
func notFound(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == keychainNotFoundExit
}

// readItem reads an item's password bytes; found is false when there is
// no such item.
func (k *keychainSeed) readItem(service string) (data []byte, found bool, err error) {
	out, err := k.security("find-generic-password", "-a", k.account, "-s", service, "-w")
	if err != nil {
		if notFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// `-w` prints the password followed by a newline of its own.
	return bytes.TrimSuffix(out, []byte("\n")), true, nil
}

// readDefault reads the store's default item — the user's own credential.
func (k *keychainSeed) readDefault() ([]byte, bool, error) {
	return k.readItem(k.store.Service)
}

// write creates or updates the session's item. -U updates in place; -X
// carries the bytes as hex, which is how the CLI itself writes them.
func (k *keychainSeed) write(data []byte) error {
	_, err := k.security("add-generic-password", "-U", "-a", k.account, "-s", k.service, "-X", hex.EncodeToString(data))
	return err
}

// delete removes the session's item. An item already gone is not an error:
// the run's teardown and the reaper may both reach for it.
func (k *keychainSeed) delete() error {
	_, err := k.security("delete-generic-password", "-a", k.account, "-s", k.service)
	if err != nil && !notFound(err) {
		return err
	}
	return nil
}

// view projects host bytes into the session item's.
func (k *keychainSeed) view(host []byte) ([]byte, error) {
	if k.store.Project == nil {
		return host, nil
	}
	out, err := k.store.Project(host)
	if err != nil {
		return nil, fmt.Errorf("keychain seed for %s: project the default item: %w", k.engine, err)
	}
	return out, nil
}

// provisionKeychainSeed is the keychain arm of hostCredentialSeed: reads the
// default item (none is "nothing seedable"), writes the session's item
// projected, and starts the poller that keeps it in step. The Result's
// Close stops the poller and — when THIS call created the item — deletes
// it. A second run of the same session finds the item already there and
// leaves it: the creator deletes at its own end, and the reaper deletes
// whatever a crashed creator left.
func provisionKeychainSeed(name string, store engine.KeychainStore, configDir string) (seedResult, Result, error) {
	k := newKeychainSeed(name, store, configDir)
	host, found, err := k.readDefault()
	if err != nil {
		return seedNoSource, Result{}, fmt.Errorf("keychain seed for %s: %w", name, err)
	}
	if !found {
		return seedNoSource, Result{}, nil
	}
	want, err := k.view(host)
	if err != nil {
		return seedNoSource, Result{}, err
	}
	_, existed, err := k.readItem(k.service)
	if err != nil {
		return seedNoSource, Result{}, fmt.Errorf("keychain seed for %s: %w", name, err)
	}
	if err := k.write(want); err != nil {
		return seedNoSource, Result{}, fmt.Errorf("keychain seed for %s: %w", name, err)
	}
	p := newKeychainPoller(k, sha256.Sum256(host), !existed)
	return seedOK, Result{Delivery: DeliveryReplicated, Mechanism: keychainMechanism, stop: p.Close}, nil
}

// keychainPoller re-reads the default item on keychainPollInterval and
// rewrites the session's item when it changed.
type keychainPoller struct {
	seed    *keychainSeed
	owner   bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	once    sync.Once
	closeMu sync.Mutex
	err     error
}

func newKeychainPoller(k *keychainSeed, lastHost [sha256.Size]byte, owner bool) *keychainPoller {
	ctx, cancel := context.WithCancel(context.Background())
	p := &keychainPoller{seed: k, owner: owner, cancel: cancel}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(keychainPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			host, found, err := k.readDefault()
			if err != nil || !found {
				// A missing or unreadable default item is the host's
				// concern (the user logged out); the session keeps what it
				// has until the item returns.
				continue
			}
			sum := sha256.Sum256(host)
			if sum == lastHost {
				continue
			}
			want, err := k.view(host)
			if err != nil {
				clidiag.Warn("ctxloom", "keychain replication for %s: %v", k.engine, err)
				continue
			}
			if err := k.write(want); err != nil {
				clidiag.Warn("ctxloom", "keychain replication for %s: %v", k.engine, err)
				continue
			}
			lastHost = sum
		}
	}()
	return p
}

// Close stops the poller and deletes the item this provisioning created.
// Idempotent.
func (p *keychainPoller) Close() error {
	p.once.Do(func() {
		p.cancel()
		p.wg.Wait()
		if p.owner {
			p.err = p.seed.delete()
		}
	})
	return p.err
}

// keychainStores lists every engine the facts accessor knows that declares
// a keychain store, with the seed it rides on.
func keychainStores() map[string]engine.CredentialSeed {
	out := map[string]engine.CredentialSeed{}
	for _, name := range factNames() {
		seed, ok := credentialSeedFor(name)
		if ok && seed.Keychain != nil {
			out[name] = seed
		}
	}
	return out
}

// keychainServicesFor derives, for every keychain-storing engine, the
// session item's service for the config dir harp's session home gives that
// engine — the same derivation the seed used, so the reaper and doctor
// name the items the seed wrote.
func keychainServicesFor(harp string) (map[string]string, error) {
	home, err := paths.HarpSessionHome(harp)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for name, seed := range keychainStores() {
		out[name] = keychainService(seed.Keychain.Service, filepath.Join(home, seed.Subdir))
	}
	return out, nil
}

// ReapKeychainItems deletes every keychain-storing engine's session item
// for harp: the reaper's hook, run beside the removal of the session home
// the item's hash was derived from. Off darwin it does nothing. An item
// already gone is not an error.
func ReapKeychainItems(harp string) error {
	if !keychainPlatform() {
		return nil
	}
	services, err := keychainServicesFor(harp)
	if err != nil {
		return err
	}
	var errs []error
	for name, service := range services {
		seed := keychainStores()[name]
		k := &keychainSeed{engine: name, store: *seed.Keychain, account: KeychainAccount(os.Getenv("USER")), service: service}
		if err := k.delete(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// OrphanedKeychainItems lists, sorted, every Keychain item whose service
// carries a keychain-storing engine's session prefix but whose hash
// matches no session home under the sessions root — an item a crashed run
// never deleted and the reaper has not yet reached. Recomputed per session
// dir, because the hash is the only link between an item and its session.
// Off darwin it lists nothing.
func OrphanedKeychainItems() ([]string, error) {
	if !keychainPlatform() {
		return nil, nil
	}
	stores := keychainStores()
	if len(stores) == 0 {
		return nil, nil
	}
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("scan %q: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		services, err := keychainServicesFor(e.Name())
		if err != nil {
			continue
		}
		for _, s := range services {
			expected[s] = true
		}
	}
	k := &keychainSeed{account: KeychainAccount(os.Getenv("USER"))}
	out, err := k.security("dump-keychain")
	if err != nil {
		return nil, err
	}
	var orphans []string
	for _, service := range keychainDumpServices(out) {
		for _, seed := range stores {
			if strings.HasPrefix(service, seed.Keychain.Service+"-") && !expected[service] {
				orphans = append(orphans, service)
			}
		}
	}
	sort.Strings(orphans)
	return orphans, nil
}

// keychainDumpServices reads the service attribute of every item in a
// `security dump-keychain` listing: the lines of the form
// `"svce"<blob>="<service>"`.
func keychainDumpServices(dump []byte) []string {
	var out []string
	for _, line := range strings.Split(string(dump), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, `"svce"<blob>="`)
		if !ok {
			continue
		}
		if service, ok := strings.CutSuffix(rest, `"`); ok {
			out = append(out, service)
		}
	}
	return out
}
