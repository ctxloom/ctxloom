//go:build linux

package mountns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Environment keys carrying the shim's instructions across the re-exec. They
// are SCRUBBED from the environment the shim hands to the real argv, so a
// grandchild that happens to be ctxloom cannot be turned into a second shim
// by an inherited marker.
const (
	envShim     = "CTXLOOM_MOUNTNS_SHIM"
	envBinds    = "CTXLOOM_MOUNTNS_BINDS"
	envArgv     = "CTXLOOM_MOUNTNS_ARGV"
	envProbePut = "CTXLOOM_MOUNTNS_PROBE_WRITE"
	envReadback = "CTXLOOM_MOUNTNS_READBACK"
	// envProbing marks that a probe is already in flight somewhere up this
	// process tree. See Supported for what it defends against.
	envProbing = "CTXLOOM_MOUNTNS_PROBING"
)

// shimEnvKeys is every marker this package plants. Listed once so scrubbing
// cannot fall out of step with planting.
var shimEnvKeys = []string{envShim, envBinds, envArgv, envProbePut, envReadback, envProbing}

// Command builds the process that runs argv inside a fresh user+mount
// namespace with binds established. It re-execs THIS binary as a shim (see
// the package doc for why a shim is unavoidable); the shim performs the
// mounts and then execs argv, replacing itself.
//
// The returned Cmd carries no stdio: the caller wires that, exactly as it
// would for a direct spawn.
func Command(ctx context.Context, binds []Bind, argv []string, env []string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("mountns: no argv to exec inside the namespace")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("mountns: locate self for the re-exec shim: %w", err)
	}
	bindsJSON, err := json.Marshal(binds)
	if err != nil {
		return nil, fmt.Errorf("mountns: encode binds: %w", err)
	}
	argvJSON, err := json.Marshal(argv)
	if err != nil {
		return nil, fmt.Errorf("mountns: encode argv: %w", err)
	}
	cmd := exec.CommandContext(ctx, self)
	cmd.Env = append(scrub(env),
		envShim+"=1",
		envBinds+"="+string(bindsJSON),
		envArgv+"="+string(argvJSON),
	)
	cmd.SysProcAttr = sysProcAttr()
	return cmd, nil
}

// sysProcAttr is the clone recipe, measured working from Go's multithreaded
// runtime: a new user namespace mapping this uid/gid to root inside it, plus
// a new mount namespace. Unshareflags is deliberately absent — it was not
// needed.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		// setgroups(2) must stay denied: an unprivileged user namespace may
		// not write gid_map while it is permitted.
		GidMappingsEnableSetgroups: false,
	}
}

// Supported reports whether this host can actually do it: nil means yes, and
// an error wrapping ErrUnsupported means no. scratch is a caller-owned
// directory the probe builds its throwaway files under; it is never the real
// material.
//
// The probe drives THE SAME shim the production path uses, rather than a
// parallel copy of it, so a shim that stopped working is a probe that starts
// failing. It proves three things in one run, because any of them failing
// makes the mechanism useless: the namespace is permitted at all, an IN-PLACE
// write through the mount reaches the host file (the arm a credential writer
// falls back to when rename(2) over a mount returns EBUSY), and the mount
// SURVIVES THE SHIM'S OWN EXEC so the process that actually matters sees it.
func Supported(ctx context.Context, scratch string) error {
	// RECURSION GUARD, and it is not theoretical: the probe re-execs
	// os.Executable(), so a binary that does NOT call RunChildIfRequested at
	// the top of main runs its ordinary startup instead of becoming a shim. If
	// that startup probes too — a test binary whose suite exercises this, for
	// one — each probe spawns a whole program that spawns more, and the fork
	// bomb is exponential. Observed, not imagined. The correct fix is for
	// every re-execable binary to honour the marker, and this is the bound
	// that holds while one does not.
	if os.Getenv(envProbing) != "" {
		return fmt.Errorf("%w: refusing to probe recursively; this process was spawned by a probe, which means the binary being re-exec'd does not call RunChildIfRequested at the top of main", ErrUnsupported)
	}
	dir, err := os.MkdirTemp(scratch, "mountns-probe-")
	if err != nil {
		return fmt.Errorf("mountns: probe scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	const payload = "written-inside-the-namespace"
	if err := seedProbeFiles(source, target); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("mountns: locate self for the probe: %w", err)
	}
	// The readback argv is THIS binary, not /bin/cat: a missing or differently
	// furnished cat would report the host as incapable when it is merely
	// differently furnished, and a capability probe that fails for an
	// unrelated reason is worse than none.
	cmd, err := Command(ctx, []Bind{{Source: source, Target: target}}, []string{self}, os.Environ())
	if err != nil {
		return err
	}
	cmd.Env = append(cmd.Env, envProbePut+"="+payload, envProbing+"=1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: %w%s", ErrUnsupported, err, stderrOf(err))
	}
	if saw := strings.TrimSpace(string(out)); saw != payload {
		return fmt.Errorf("%w: the mount did not survive the shim's exec (the exec'd process read %q, not the payload written through the mount)", ErrUnsupported, saw)
	}
	landed, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("mountns: reread the probe source: %w", err)
	}
	if string(landed) != payload {
		return fmt.Errorf("%w: an in-place write through the mount did not reach the host file (it still reads %q)", ErrUnsupported, string(landed))
	}
	return nil
}

// seedProbeFiles writes the probe's bind source and target, each with
// content the probe payload can be told apart from.
func seedProbeFiles(source, target string) error {
	if err := safefs.WriteFileInPlace(source, safefs.TruncateInPlace, []byte("original"), 0o600); err != nil {
		return fmt.Errorf("mountns: seed probe source: %w", err)
	}
	if err := safefs.WriteFileInPlace(target, safefs.TruncateInPlace, []byte("placeholder"), 0o600); err != nil {
		return fmt.Errorf("mountns: seed probe target: %w", err)
	}
	return nil
}

// RunChildIfRequested is the re-exec entry point. It returns immediately
// unless a marker is present, and NEVER RETURNS when one is. Call it at the
// top of main, before any other startup work.
func RunChildIfRequested() {
	// ORDER MATTERS. The shim plants the readback marker for its GRANDCHILD,
	// and both markers are therefore visible in the shim's own environment
	// during the window before it execs. Testing readback first made the shim
	// answer as the grandchild and exit before mounting anything, which the
	// probe reported as "this host cannot do it" — a false negative that looks
	// exactly like a hardened kernel.
	if os.Getenv(envShim) == "1" {
		if err := shim(); err != nil {
			fmt.Fprintln(os.Stderr, "mountns shim:", err)
			os.Exit(1)
		}
		// shim returns only by exec'ing, which does not return; reaching here
		// is a bug, and exiting non-zero beats falling through into the CLI.
		fmt.Fprintln(os.Stderr, "mountns shim: exec returned without an error")
		os.Exit(1)
	}
	if path := os.Getenv(envReadback); path != "" {
		// The probe's grandchild: the only witness that the mount outlived
		// the shim's exec.
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mountns readback:", err)
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}
}

// shim runs inside the new namespaces: make propagation private, bind, then
// become the real process.
func shim() error {
	binds, argv, err := decodeShimSpec()
	if err != nil {
		return err
	}
	// UNCONDITIONAL, even though CLONE_NEWUSER was measured to sever
	// child-to-host propagation on its own: that guarantee comes from the new
	// USER namespace owning this mount namespace, and it evaporates the moment
	// any future path creates a mount namespace without one. It costs nothing
	// and the observation was made on exactly one kernel.
	if err := unix.Mount("none", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mount propagation private: %w", err)
	}
	if err := applyBinds(binds); err != nil {
		return err
	}
	env := scrub(os.Environ())
	if payload := os.Getenv(envProbePut); payload != "" && len(binds) > 0 {
		// Probe only: the in-place arm a credential writer falls back to when
		// rename(2) over a bind mount returns EBUSY. Driven through the real
		// shim so the probe cannot pass while the shim is broken.
		if err := safefs.WriteFileInPlace(binds[0].Target, safefs.TruncateInPlace, []byte(payload), 0o600); err != nil {
			return fmt.Errorf("probe write through the mount: %w", err)
		}
		// The grandchild is told to read the mounted path back. Planted HERE,
		// past the scrub, so only the process on the far side of the exec
		// carries it — see RunChildIfRequested on why it must not be visible
		// to the shim.
		env = append(env, envReadback+"="+binds[0].Target)
	}
	return syscall.Exec(argv[0], argv, env)
}

// decodeShimSpec reads the binds and argv Command planted for the shim.
func decodeShimSpec() ([]Bind, []string, error) {
	var binds []Bind
	if err := json.Unmarshal([]byte(os.Getenv(envBinds)), &binds); err != nil {
		return nil, nil, fmt.Errorf("decode binds: %w", err)
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(envArgv)), &argv); err != nil {
		return nil, nil, fmt.Errorf("decode argv: %w", err)
	}
	if len(argv) == 0 {
		return nil, nil, errors.New("no argv to exec")
	}
	return binds, argv, nil
}

// applyBinds makes each bind in order, remounting the read-only ones.
func applyBinds(binds []Bind) error {
	for _, b := range binds {
		if err := unix.Mount(b.Source, b.Target, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s onto %s: %w", b.Source, b.Target, err)
		}
		if !b.ReadOnly {
			continue
		}
		// A read-only bind is a REMOUNT of the bind just made; MS_RDONLY on
		// the original mount(2) is silently ignored.
		if err := unix.Mount("", b.Target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("make %s read-only: %w", b.Target, err)
		}
	}
	return nil
}

// scrub removes every marker this package plants, so the process the shim
// becomes cannot inherit instructions meant for the shim.
func scrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if slices.Contains(shimEnvKeys, key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// stderrOf renders a failed child's stderr, which carries the shim's own
// errno message and is the only place a mount failure is explained.
func stderrOf(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if detail := strings.TrimSpace(string(exit.Stderr)); detail != "" {
			return ": " + detail
		}
	}
	return ""
}
