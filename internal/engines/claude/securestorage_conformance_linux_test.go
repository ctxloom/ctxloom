//go:build conformance && linux

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestClaudeSecureStorage_FollowsTheVar is the conformance probe for the one
// fact a HOST run's shared login stands on (claudeAuth.Credentials for auth login): the
// installed claude takes its credential storage from SecureStorageEnv, apart
// from ConfigDirEnv. A session-home agent and the human's own claude share
// one credential and one lock pair only while BOTH refresh locks —
// rw()/.oauth_refresh.lock and the legacy realpath(rw()).lock — derive from
// that var. If a claude release drops the var, or keeps it for the file but
// moves a lock, two refreshers stop excluding each other and a single-use
// refresh token can be spent twice. This probe goes red first.
//
// It is BEHAVIOURAL: it runs the real binary, `claude auth status`, which
// with an EXPIRED fixture credential attempts a refresh, takes both locks,
// and fails because the run has no network (unshare -rn: a fresh user and
// network namespace, so no auth call can leave the machine). It observes the
// filesystem through inotify, because both locks are directories created and
// removed within the run.
//
// Every run gets a FAKE HOME and a fixture credential. The human's real
// claude home is never read.
func TestClaudeSecureStorage_FollowsTheVar(t *testing.T) {
	bin, err := locateClaudeBinary()
	if err != nil {
		t.Skipf("claude not found on PATH; nothing to conform against: %v", err)
	}
	if err := exec.Command("unshare", "-rn", "true").Run(); err != nil {
		t.Skipf("cannot create a network-less user namespace (unshare -rn), and the probe never runs claude with network: %v", err)
	}

	t.Run("an explicit dir is the storage, and realpath names the legacy lock", func(t *testing.T) {
		f := newStorageFixture(t)
		// The var names a SYMLINK to the store, so rw() (the path as given)
		// and realpath(rw()) differ, and each lock shows which one it used.
		store := filepath.Join(f.root, "store", "real")
		mkdirs(t, store)
		link := filepath.Join(f.root, "link")
		if err := os.Symlink(store, link); err != nil {
			t.Fatal(err)
		}
		f.writeCredential(t, store)
		obs := f.run(t, bin, map[string]string{SecureStorageEnv: link}, store, filepath.Dir(store))

		obs.requireLoggedInFromFixture(t)
		obs.requireOpened(t, store, credentialFile)
		obs.requireCreated(t, store, refreshLockName)
		obs.requireCreated(t, filepath.Dir(store), filepath.Base(store)+".lock")
		obs.requireNotCreated(t, f.root, "link.lock")
		f.requireNoStorageIn(t, obs, f.session, filepath.Join(f.home, ConfigDirName))
	})

	t.Run("empty resolves to HOME/.claude, the human's default", func(t *testing.T) {
		f := newStorageFixture(t)
		store := filepath.Join(f.home, ConfigDirName)
		f.writeCredential(t, store)
		obs := f.run(t, bin, map[string]string{SecureStorageEnv: ""}, store, f.home)

		obs.requireLoggedInFromFixture(t)
		obs.requireOpened(t, store, credentialFile)
		obs.requireCreated(t, store, refreshLockName)
		obs.requireCreated(t, f.home, ConfigDirName+".lock")
		f.requireNoStorageIn(t, obs, f.session)
	})

	// The control: without the var the same fixture is NOT found (claude looks
	// in the session home) and nothing locks the store. Without it, the cases
	// above could pass on a claude that reads HOME/.claude whatever the env.
	t.Run("negative control: unset leaves the storage in the config dir", func(t *testing.T) {
		f := newStorageFixture(t)
		store := filepath.Join(f.root, "real")
		mkdirs(t, store)
		f.writeCredential(t, store)
		obs := f.run(t, bin, nil, store, f.root)

		if obs.status.LoggedIn {
			t.Fatalf("with %s unset claude still found the fixture credential outside its config dir; the positive cases no longer isolate the var:\n%s", SecureStorageEnv, obs.out)
		}
		obs.requireNotCreated(t, store, refreshLockName)
		obs.requireNotCreated(t, f.root, "real.lock")
	})
}

const (
	credentialFile  = ".credentials.json"
	refreshLockName = ".oauth_refresh.lock"
	// fixtureSubscription is what `auth status` can only report by reading
	// the fixture: nothing else in the fake home names a subscription.
	fixtureSubscription = "max"
)

// storageFixture is one probe run's fake world: HOME, the session home
// (ConfigDirEnv) and the root every other dir hangs under.
type storageFixture struct{ root, home, session string }

func newStorageFixture(t *testing.T) storageFixture {
	t.Helper()
	// EvalSymlinks: inotify reports under the path it was given, and the
	// realpath lock is named from the resolved one.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := storageFixture{root: root, home: filepath.Join(root, "home"), session: filepath.Join(root, "session")}
	mkdirs(t, filepath.Join(f.home, ConfigDirName), f.session)
	return f
}

// writeCredential writes an EXPIRED fixture credential with a refresh token
// into dir: expired, so `auth status` attempts the refresh that takes the
// locks. The tokens are shaped like claude's and authenticate nothing.
func (f storageFixture) writeCredential(t *testing.T, dir string) {
	t.Helper()
	cred := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-CTXLOOM-PROBE-FIXTURE","refreshToken":"sk-ant-ort01-CTXLOOM-PROBE-FIXTURE","expiresAt":1000,"scopes":["user:inference","user:profile"],"subscriptionType":"` + fixtureSubscription + `"}}`
	if err := os.WriteFile(filepath.Join(dir, credentialFile), []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run executes `claude auth status` with the host run's env shape — a fake
// HOME, ConfigDirEnv at the session home, OAuthTokenEnv blanked — plus
// extra, watching watched and every fixture dir.
func (f storageFixture) run(t *testing.T, bin string, extra map[string]string, watched ...string) observation {
	t.Helper()
	w := watchDirs(t, append([]string{f.root, f.home, filepath.Join(f.home, ConfigDirName), f.session}, watched...)...)
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + f.home, ConfigDirEnv + "=" + f.session, OAuthTokenEnv + "="}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "-rn", bin, "auth", "status")
	cmd.Dir = f.root
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	runErr := cmd.Run()
	obs := observation{out: stdout.String(), events: w.drain()}
	if ctx.Err() != nil {
		t.Fatalf("claude auth status did not finish within the probe's timeout:\n%s", obs.out)
	}
	// auth status exits non-zero when logged out; the output decides.
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &obs.status); err != nil {
		t.Fatalf("claude auth status printed no status JSON (exit %v): %v\n%s", runErr, err, obs.out)
	}
	return obs
}

// requireNoStorageIn fails when claude left a credential or a refresh lock
// in any of dirs: the storage the var moved away must stay empty.
func (f storageFixture) requireNoStorageIn(t *testing.T, obs observation, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		obs.requireNotCreated(t, d, credentialFile)
		obs.requireNotCreated(t, d, refreshLockName)
		obs.requireNotCreated(t, filepath.Dir(d), filepath.Base(d)+".lock")
	}
}

type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	SubscriptionType string `json:"subscriptionType"`
}

type observation struct {
	out    string
	status authStatus
	events []fsEvent
}

func (o observation) requireLoggedInFromFixture(t *testing.T) {
	t.Helper()
	if !o.status.LoggedIn || o.status.SubscriptionType != fixtureSubscription {
		t.Fatalf("claude did not authenticate from the fixture credential in %s's dir (want loggedIn with subscription %q):\n%s", SecureStorageEnv, fixtureSubscription, o.out)
	}
}

func (o observation) requireOpened(t *testing.T, dir, name string) {
	t.Helper()
	if !o.saw(dir, name, syscall.IN_OPEN) {
		t.Fatalf("claude never opened %s: the credential was not read from %s's dir", filepath.Join(dir, name), SecureStorageEnv)
	}
}

func (o observation) requireCreated(t *testing.T, dir, name string) {
	t.Helper()
	if !o.saw(dir, name, syscall.IN_CREATE) {
		t.Fatalf("claude never created the refresh lock %s: it no longer derives from %s, so it no longer excludes the human's own claude", filepath.Join(dir, name), SecureStorageEnv)
	}
}

func (o observation) requireNotCreated(t *testing.T, dir, name string) {
	t.Helper()
	if o.saw(dir, name, syscall.IN_CREATE) || o.saw(dir, name, syscall.IN_MOVED_TO) {
		t.Fatalf("claude created %s, outside the storage %s names", filepath.Join(dir, name), SecureStorageEnv)
	}
}

func (o observation) saw(dir, name string, mask uint32) bool {
	for _, e := range o.events {
		if e.Dir == dir && e.Name == name && e.Mask&mask != 0 {
			return true
		}
	}
	return false
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

type fsEvent struct {
	Dir, Name string
	Mask      uint32
}

// fileWatch is an inotify instance over a fixed set of directories: it sees
// every entry created, renamed in or opened directly inside each, including
// ones removed again before the run ends.
type fileWatch struct {
	fd  int
	wds map[int32]string
}

func watchDirs(t *testing.T, dirs ...string) *fileWatch {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatalf("inotify: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	w := &fileWatch{fd: fd, wds: map[int32]string{}}
	for _, d := range dirs {
		wd, err := syscall.InotifyAddWatch(fd, d, syscall.IN_CREATE|syscall.IN_OPEN|syscall.IN_MOVED_TO|syscall.IN_ONLYDIR)
		if err != nil {
			t.Fatalf("watch %s: %v", d, err)
		}
		w.wds[int32(wd)] = d
	}
	return w
}

// drain reads every queued event; the run has exited, so the queue is
// complete.
func (w *fileWatch) drain() []fsEvent {
	var out []fsEvent
	buf := make([]byte, 1<<20)
	for {
		n, err := syscall.Read(w.fd, buf)
		if n <= 0 || err != nil {
			return out
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
			out = append(out, fsEvent{Dir: w.wds[ev.Wd], Name: string(bytes.TrimRight(raw, "\x00")), Mask: ev.Mask})
			off += syscall.SizeofInotifyEvent + int(ev.Len)
		}
	}
}

// locateClaudeBinary resolves the claude on PATH to its real, versioned
// binary (claude ships as a symlink from a launcher path into
// ~/.local/share/claude/versions/<ver>). CTXLOOM_CLAUDE_BINARY_CONFORMANCE
// overrides discovery, for pointing this probe at a specific version by
// hand.
func locateClaudeBinary() (string, error) {
	if p := os.Getenv("CTXLOOM_CLAUDE_BINARY_CONFORMANCE"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil // fall back to the unresolved path rather than fail discovery
	}
	return real, nil
}
