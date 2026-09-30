package isolation

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

const (
	selfID    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherSelf = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// ciJobContainerInspect is the self inspect of a GitHub Actions job container
// as the narrow template renders it: a user-defined network, the workspace
// bind (/home/runner/work → /__w) with a nested _temp bind, /tmp shared at the
// same path, the daemon socket, and a tmpfs that names nothing on the daemon.
const ciJobContainerInspect = `{"Id":"` + selfID + `","NetworkMode":"github_network_4f2a","Networks":{"github_network_4f2a":{"IPAddress":"172.18.0.2","GlobalIPv6Address":""}},"Mounts":[` +
	`{"Type":"bind","Source":"/home/runner/work","Destination":"/__w"},` +
	`{"Type":"bind","Source":"/home/runner/work/_temp","Destination":"/__w/_temp"},` +
	`{"Type":"bind","Source":"/tmp","Destination":"/tmp"},` +
	`{"Type":"bind","Source":"/var/run/docker.sock","Destination":"/var/run/docker.sock"},` +
	`{"Type":"volume","Source":"/var/lib/docker/volumes/home/_data","Destination":"/root/.ctxloom"},` +
	`{"Type":"tmpfs","Source":"","Destination":"/run/scratch"}]}`

// stubSelfCandidates fixes the ids this process "may be".
func stubSelfCandidates(t *testing.T, ids ...string) {
	t.Helper()
	orig := selfIDCandidates
	selfIDCandidates = func() []string { return ids }
	t.Cleanup(func() { selfIDCandidates = orig })
}

// scriptExec answers probeExec per argv (joined with spaces, binary first),
// failing the test on any argv it was not scripted for, and records calls.
func scriptExec(t *testing.T, script map[string]func() (string, error)) *[]string {
	t.Helper()
	var calls []string
	orig := probeExec
	probeExec = func(_ context.Context, bin string, args []string) (string, error) {
		argv := strings.Join(append([]string{bin}, args...), " ")
		calls = append(calls, argv)
		if fn, ok := script[argv]; ok {
			return fn()
		}
		t.Errorf("unscripted exec: %s", argv)
		return "", errors.New("unscripted")
	}
	t.Cleanup(func() { probeExec = orig })
	return &calls
}

func out(s string) func() (string, error) { return func() (string, error) { return s, nil } }

var dockerPS = "docker ps -a -q --no-trunc --filter id="

func dockerInspectSelf(id string) string {
	return "docker " + strings.Join(ociRuntime{}.selfInspectArgs(id), " ")
}

func TestFindSelf_NoCandidatesAsksTheDaemonNothing(t *testing.T) {
	stubSelfCandidates(t)
	calls := scriptExec(t, nil)
	_, ok, err := findSelf(context.Background(), Docker{})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, *calls)
}

func TestFindSelf_ACandidateTheDaemonDoesNotKnowIsNotUs(t *testing.T) {
	stubSelfCandidates(t, otherSelf)
	scriptExec(t, map[string]func() (string, error){dockerPS + otherSelf: out("\n")})
	_, ok, err := findSelf(context.Background(), Docker{})
	require.NoError(t, err)
	assert.False(t, ok, "not a container of this daemon (dind over TCP, a foreign socket)")
}

func TestFindSelf_ADaemonThatCannotAnswerIsAnError(t *testing.T) {
	stubSelfCandidates(t, selfID)
	scriptExec(t, map[string]func() (string, error){dockerPS + selfID: func() (string, error) { return "", errors.New("exit status 1") }})
	_, ok, err := findSelf(context.Background(), Docker{})
	require.Error(t, err)
	assert.False(t, ok)
}

// TestFindSelf_DecodesTheCIJobContainer: the first candidate the daemon knows
// is inspected (by the full id it reported) and decoded: its joinable network
// and its daemon-side mounts; a tmpfs names nothing the daemon can bind.
func TestFindSelf_DecodesTheCIJobContainer(t *testing.T) {
	stubSelfCandidates(t, otherSelf, selfID[:12])
	scriptExec(t, map[string]func() (string, error){
		dockerPS + otherSelf:      out(""),
		dockerPS + selfID[:12]:    out(selfID + "\n"),
		dockerInspectSelf(selfID): out(ciJobContainerInspect + "\n"),
	})
	s, ok, err := findSelf(context.Background(), Docker{})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, selfContainer{
		id:      selfID,
		network: selfNetwork{name: "github_network_4f2a", ip: "172.18.0.2"},
		mounts: []selfMount{
			{source: "/home/runner/work", destination: "/__w"},
			{source: "/home/runner/work/_temp", destination: "/__w/_temp"},
			{source: "/tmp", destination: "/tmp"},
			{source: "/var/run/docker.sock", destination: "/var/run/docker.sock"},
			{source: "/var/lib/docker/volumes/home/_data", destination: "/root/.ctxloom"},
		},
	}, s)
}

// TestFindSelf_AnAmbiguousShortIDIsNotUs: a short candidate matching several
// containers names none of them.
func TestFindSelf_AnAmbiguousShortIDIsNotUs(t *testing.T) {
	stubSelfCandidates(t, "0123456789ab")
	scriptExec(t, map[string]func() (string, error){dockerPS + "0123456789ab": out(selfID + "\n" + otherSelf + "\n")})
	_, ok, err := findSelf(context.Background(), Docker{})
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestDecodeSelf_Network: which of self's networks a sibling joins.
func TestDecodeSelf_Network(t *testing.T) {
	stubPrimary(t, "10.5.0.7")
	cases := []struct {
		name     string
		mode     string
		networks string
		want     selfNetwork
		hostNet  bool
	}{
		{"one network", "bridge", `{"bridge":{"IPAddress":"172.17.0.3"}}`, selfNetwork{"bridge", "172.17.0.3"}, false},
		{"several: the one the default route leaves by", "a", `{"a":{"IPAddress":"10.4.0.2"},"b":{"IPAddress":"10.5.0.7"}}`, selfNetwork{"b", "10.5.0.7"}, false},
		{"several, none primary: first by name", "b", `{"b":{"IPAddress":"10.9.0.2"},"a":{"IPAddress":"10.8.0.2"}}`, selfNetwork{"a", "10.8.0.2"}, false},
		{"IPv6 only", "v6", `{"v6":{"IPAddress":"","GlobalIPv6Address":"fd00::2"}}`, selfNetwork{"v6", "fd00::2"}, false},
		{"IPv4 preferred", "d", `{"d":{"IPAddress":"10.1.0.2","GlobalIPv6Address":"fd00::2"}}`, selfNetwork{"d", "10.1.0.2"}, false},
		{"an entry without an address is not joinable", "pasta", `{"pasta":{"IPAddress":""}}`, selfNetwork{}, false},
		{"pasta: none", "pasta", `null`, selfNetwork{}, false},
		{"host network", "host", `{"host":{"IPAddress":""}}`, selfNetwork{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := decodeSelf(`{"Id":"` + selfID + `","NetworkMode":"` + tc.mode + `","Networks":` + tc.networks + `,"Mounts":[]}`)
			require.NoError(t, err)
			assert.Equal(t, tc.want, s.network)
			assert.Equal(t, tc.hostNet, s.hostNet)
		})
	}
}

func TestSelfNetworkRoute(t *testing.T) {
	r, ok, err := selfNetworkRoute(selfContainer{id: selfID, network: selfNetwork{"github_network_4f2a", "172.18.0.2"}})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, hostRoute{dial: "172.18.0.2", listen: present.Listen{Addr: "172.18.0.2"}, network: "github_network_4f2a"}, r)

	_, ok, err = selfNetworkRoute(selfContainer{id: selfID, hostNet: true})
	require.NoError(t, err)
	assert.False(t, ok, "--network host shares the daemon host's stack: today's routes apply")

	_, ok, err = selfNetworkRoute(selfContainer{id: selfID})
	require.ErrorIs(t, err, errNoSelfNetwork)
	assert.False(t, ok)
}

func withSelf(s selfContainer) ociRuntime { return ociRuntime{self: &s} }

var ciSelf = selfContainer{id: selfID, network: selfNetwork{"github_network_4f2a", "172.18.0.2"}}

// TestReachRoute_SelfFirst: once the daemon has confirmed this process is one
// of its containers, its container network beats every host route — the
// loopback translator, the public fallback and the bridge gateway, which is
// never even asked for.
func TestReachRoute_SelfFirst(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	stubLocal(t, false, nil)
	calls := scriptExec(t, nil)
	want := hostRoute{dial: "172.18.0.2", listen: present.Listen{Addr: "172.18.0.2"}, network: "github_network_4f2a"}
	for _, rt := range []Runtime{
		Docker{ociRuntime: withSelf(ciSelf)},
		Docker{ociRuntime: withSelf(ciSelf), rootless: true},
		Podman{ociRuntime: withSelf(ciSelf)},
		Podman{ociRuntime: withSelf(ciSelf), rootless: true, rootlessNet: "pasta"},
	} {
		got, err := rt.reachRoute(context.Background())
		require.NoError(t, err, rt.Name())
		assert.Equal(t, want, got, rt.Name())
	}
	assert.Empty(t, *calls, "the bridge gateway is never inspected")
}

// TestReachRoute_SelfOnTheHostNetworkFallsThrough: a container on the host's
// own stack reaches what the host does, so the host route stands (docker0's
// gateway on a shared kernel).
func TestReachRoute_SelfOnTheHostNetworkFallsThrough(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	stubLocal(t, true, nil)
	stubGateway(t, "172.17.0.1\n", nil)
	want, err := Docker{}.hostReach(context.Background())
	require.NoError(t, err)
	got, err := Docker{ociRuntime: withSelf(selfContainer{id: selfID, hostNet: true})}.reachRoute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestReachRoute_RootlessPodmanWithoutANetworkSharesTheNamespace: the owner's
// ruling — ctxloom's container on pasta/slirp has no network a sibling can
// join, so the runner shares ctxloom's network namespace and dials the
// loopback as minted, WITH a warning that names what isolation is lost.
func TestReachRoute_RootlessPodmanWithoutANetworkSharesTheNamespace(t *testing.T) {
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var warned bytes.Buffer
	t.Cleanup(clidiag.SetSink(&warned))
	got, err := Podman{ociRuntime: withSelf(selfContainer{id: selfID}), rootless: true, rootlessNet: "pasta"}.reachRoute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, hostRoute{network: "container:" + selfID}, got)
	assert.Contains(t, warned.String(), "localhost")
	assert.Contains(t, warned.String(), selfID)
}

// TestSettleReach_SelfWithoutANetworkIsRefused: every other runtime refuses a
// self with no joinable network, naming the fix — never a namespace share the
// owner did not rule for.
func TestSettleReach_SelfWithoutANetworkIsRefused(t *testing.T) {
	resetStrictness(t)
	for _, rt := range []Runtime{
		Docker{ociRuntime: withSelf(selfContainer{id: selfID})},
		Podman{ociRuntime: withSelf(selfContainer{id: selfID})},
	} {
		mark := strictness.Checkpoint()
		route, err := settleReach(context.Background(), rt)
		require.ErrorIs(t, err, errNoSelfNetwork, rt.Name())
		assert.Equal(t, hostRoute{}, route)
		found := strictness.Since(mark)
		require.Len(t, found, 1)
		assert.Equal(t, report.KindIsolation, found[0].Kind)
		assert.Equal(t, noSelfNetworkRemedy, found[0].Remedy)
	}
}

// TestResolveSelf_UndecidableIsAWarningAndNotSelf: a daemon whose CLI cannot
// list containers (socket permission, a restricted proxy) leaves the question
// "is this one of yours" undecidable. The ruled outcome: construction records
// NO finding (nothing for strict mode to refuse on), the runtime is not-self so
// the host routes and their honest refusals decide, and ONE warning — however
// many times the runtime is built in this process — names the likely cause.
func TestResolveSelf_UndecidableIsAWarningAndNotSelf(t *testing.T) {
	resetStrictness(t)
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var warned bytes.Buffer
	t.Cleanup(clidiag.SetSink(&warned))
	prevInfo := engineInfo
	t.Cleanup(func() { engineInfo = prevInfo })
	engineInfo = func(context.Context, string, string) (string, error) { return "[name=seccomp]", nil }
	stubSelfCandidates(t, selfID)
	scriptExec(t, map[string]func() (string, error){
		dockerPS + selfID: func() (string, error) { return "", errors.New("permission denied on the socket") },
	})

	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })
	for range 2 {
		d, _ := newDockerRuntime(func(string) bool { return true })
		assert.Nil(t, d.self, "an undecidable lookup proceeds as not-self")
	}
	assert.Empty(t, strictness.Since(mark), "an undecidable self-lookup is not a finding")
	require.NoError(t, strictness.Mode{}.FindingsError(mark), "strict mode must not refuse on it")

	got := warned.String()
	assert.Equal(t, 1, strings.Count(got, "warning:"), "warned exactly once per process; got %q", got)
	assert.Contains(t, got, "permission denied on the socket")
	assert.Contains(t, got, selfLookupRemedy("docker"))
}

// TestResolveSelf_DecidedAnswersAreKept: a confirmed self is kept and a
// candidate the daemon does not know is not-self, both silently.
func TestResolveSelf_DecidedAnswersAreKept(t *testing.T) {
	orig := findSelf
	t.Cleanup(func() { findSelf = orig })
	var warned bytes.Buffer
	t.Cleanup(clidiag.SetSink(&warned))

	findSelf = func(context.Context, Runtime) (selfContainer, bool, error) { return ciSelf, true, nil }
	assert.Equal(t, &ciSelf, resolveSelf(Docker{}))

	findSelf = func(context.Context, Runtime) (selfContainer, bool, error) { return selfContainer{}, false, nil }
	assert.Nil(t, resolveSelf(Docker{}))
	assert.Empty(t, warned.String())
}

// TestDescribe_SelfRoutes: the reach names the container network a runner
// joins, and a shared namespace says so.
func TestDescribe_SelfRoutes(t *testing.T) {
	c := Container{runtime: Docker{}}
	assert.Equal(t, "172.18.0.2 (container network github_network_4f2a)",
		c.describe(hostRoute{dial: "172.18.0.2", listen: present.Listen{Addr: "172.18.0.2"}, network: "github_network_4f2a"}).Reach)
	assert.Equal(t, "loopback (sharing this process's container network namespace)",
		c.describe(hostRoute{network: "container:" + selfID}).Reach)
	assert.Equal(t, "169.254.1.3", c.describe(hostRoute{dial: "169.254.1.3", network: "pasta:--map-host-loopback,169.254.1.3"}).Reach,
		"a translator route is described as before")
}
