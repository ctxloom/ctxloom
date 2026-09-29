//go:build !windows

// Pins the shared-kernel route order; a Windows host's containers always run in a VM (platform.ContainersInVM).

package isolation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestReachRoute pins the preference order per runtime mode on a shared
// kernel, as measured on this project's dev host (podman 5.4.2, rootless
// docker under RootlessKit): (1) the host's loopback through the rootless
// translator — no listener beyond loopback; (2) the rootful bridge gateway;
// (3) the primary address, Public, with the reason.
func TestReachRoute(t *testing.T) {
	if platform.ContainersInVM {
		t.Skip("in a VM every runtime answers its alias; this pins the shared-kernel order")
	}
	stubPrimary(t, "192.0.2.10")
	stubLocal(t, true, nil)
	public := func(why string) present.Listen {
		return present.Listen{Addr: "192.0.2.10", Public: true, Why: why}
	}
	cases := []struct {
		name       string
		rt         Runtime
		gateway    string
		wantDial   string
		wantListen present.Listen
		whyHas     string
	}{
		{"podman rootless pasta: translator loopback", Podman{rootless: true, rootlessNet: "pasta"}, "", "169.254.1.3", present.Listen{}, ""},
		{"podman rootless slirp4netns: translator loopback", Podman{rootless: true, rootlessNet: "slirp4netns"}, "", "10.0.2.2", present.Listen{}, ""},
		{"podman rootless unknown network: public", Podman{rootless: true}, "", "192.0.2.10", present.Listen{}, "no known route"},
		{"podman rootful: bridge gateway", Podman{}, "10.88.0.1\n", "10.88.0.1", present.Listen{Addr: "10.88.0.1"}, ""},
		{"docker rootless: public", Docker{rootless: true}, "", "192.0.2.10", present.Listen{}, "RootlessKit"},
		{"docker rootful: bridge gateway", Docker{}, "172.17.0.1\n", "172.17.0.1", present.Listen{Addr: "172.17.0.1"}, ""},
		{"docker rootful without a gateway: public", Docker{}, "<no value>\n", "192.0.2.10", present.Listen{}, "no bridge gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubGateway(t, tc.gateway, nil)
			got, err := tc.rt.reachRoute(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.wantDial, got.dial)
			if tc.whyHas != "" {
				assert.True(t, got.listen.Public, "the fallback is flagged public")
				assert.Equal(t, public(got.listen.Why).Addr, got.listen.Addr)
				assert.Contains(t, got.listen.Why, tc.whyHas)
				return
			}
			assert.Equal(t, tc.wantListen, got.listen)
		})
	}
}

// TestReachRoute_GatewayInspectIsTheRuntimes: the bridge gateway is read
// through each runtime's own network name and template.
func TestReachRoute_GatewayInspectIsTheRuntimes(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	got := stubGateway(t, "10.88.0.1", nil)
	_, _ = bridgeRoute(context.Background(), Podman{})
	assert.Equal(t, "podman network inspect podman --format {{range .Subnets}}{{.Gateway}}{{end}}", strings.Join(*got, " "))
	_, _ = bridgeRoute(context.Background(), Docker{})
	assert.Equal(t, "docker network inspect bridge --format {{(index .IPAM.Config 0).Gateway}}", strings.Join(*got, " "))
}

// TestReachRoute_NoRouteAtAll: no private route and no default route is
// ErrNoHostReach, never an empty route that would ship a URL nothing answers.
func TestReachRoute_NoRouteAtAll(t *testing.T) {
	stubPrimary(t, "")
	stubGateway(t, "", errors.New("no such network"))
	_, err := Docker{rootless: true}.reachRoute(context.Background())
	require.ErrorIs(t, err, ErrNoHostReach)
	_, err = Docker{}.reachRoute(context.Background())
	require.ErrorIs(t, err, ErrNoHostReach)
}

// TestRunNetwork_TheRouteDecidesIt: a run's --network has ONE producer, the
// route home. Podman's rootless translators keep the user's network and open
// its loopback route; every other route leaves the runtime's default. The
// runtime's own argv head never decides it.
func TestRunNetwork_TheRouteDecidesIt(t *testing.T) {
	if platform.ContainersInVM {
		t.Skip("in a VM every runtime answers its alias; this pins the shared-kernel routes")
	}
	stubPrimary(t, "192.0.2.10")
	stubLocal(t, true, nil)
	stubGateway(t, "10.88.0.1\n", nil)
	cases := []struct {
		name string
		rt   Runtime
		want string
	}{
		{"pasta", Podman{rootless: true, rootlessNet: "pasta"}, "pasta:--map-host-loopback,169.254.1.3"},
		{"slirp4netns", Podman{rootless: true, rootlessNet: "slirp4netns"}, "slirp4netns:allow_host_loopback=true"},
		{"podman rootless unknown network", Podman{rootless: true}, ""},
		{"podman rootful", Podman{}, ""},
		{"docker rootful", Docker{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, err := tc.rt.reachRoute(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, route.network)
		})
	}
	assert.NotContains(t, strings.Join(Podman{rootless: true, rootlessNet: "pasta"}.RunArgs(sampleSpec()), " "), "--network",
		"the argv head never decides the network; the spec carries the route's")
}

// TestRenderRunSpec_NetworkRenderedOnceBeforeTheImage: RunSpec.Network renders
// exactly once, as a run flag (before the image); empty renders none.
func TestRenderRunSpec_NetworkRenderedOnceBeforeTheImage(t *testing.T) {
	spec := sampleSpec()
	spec.Network = "ctxloom-net"
	for _, rt := range []Runtime{Docker{}, Podman{rootless: true, rootlessNet: "pasta"}} {
		args := rt.RunArgs(spec)
		n, at, img := 0, -1, -1
		for i, a := range args {
			if a == "--network=ctxloom-net" {
				n++
				at = i
			}
			if a == spec.Image && img < 0 {
				img = i
			}
		}
		assert.Equal(t, 1, n, "%s: one --network", rt.Name())
		assert.Less(t, at, img, "%s: --network is a run flag, before the image", rt.Name())
	}
	assert.NotContains(t, strings.Join(Docker{}.RunArgs(sampleSpec()), " "), "--network")
}

// TestBuildRunnerSpec_CarriesTheRoutesNetwork: the runner joins the network
// its route home names.
func TestBuildRunnerSpec_CarriesTheRoutesNetwork(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "docker", available: true}, "mock").WithImage("img")
	cw := newRunnerTestWorkspace()
	placeRoots(c, cw)
	cw.reach = hostRoute{dial: "172.18.0.5", network: "github_network_abc"}
	assert.Equal(t, "github_network_abc", c.buildRunnerSpec("mock", "name", cw, nil).Network)
}

// TestRemintReach: the coordinator's host-side URL is re-minted to the URL
// the route dials, port and path kept, every other key untouched; the
// caller's map is not written.
func TestRemintReach(t *testing.T) {
	cw := &containerWorkspace{reach: hostRoute{dial: "169.254.1.3"}}
	env := map[string]string{sessions.EnvCoordURL: "http://127.0.0.1:41234/mcp", sessions.EnvRunID: "r1"}
	got, err := remintReach(cw, env)
	require.NoError(t, err)
	assert.Equal(t, "http://169.254.1.3:41234/mcp", got[sessions.EnvCoordURL])
	assert.Equal(t, "r1", got[sessions.EnvRunID])
	assert.Equal(t, "http://127.0.0.1:41234/mcp", env[sessions.EnvCoordURL], "the host side is not overwritten")

	none, err := remintReach(cw, map[string]string{sessions.EnvRunID: "r1"})
	require.NoError(t, err)
	assert.NotContains(t, none, sessions.EnvCoordURL, "a launch without reach-back stays without one")
}

// TestSettleReach_NoRouteIsAFatalIsolationFinding: a container that could
// never dial home is refused at the workspace gate (ResolveWorkspace).
func TestSettleReach_NoRouteIsAFatalIsolationFinding(t *testing.T) {
	stubPrimary(t, "")
	stubGateway(t, "", errors.New("no such network"))
	mark := strictness.Checkpoint()
	_, err := settleReach(context.Background(), Docker{rootless: true})
	require.ErrorIs(t, err, ErrNoHostReach)
	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, "cannot dial home")
}

// TestSettleReach_ForeignBridgeGatewayIsAFatalIsolationFinding: a bridge
// gateway that is not one of this host's own addresses (ctxloom in a container
// driving a daemon it shares no network with) is refused at the gate with the
// docker-outside-of-docker remedy, and no route is handed on to listen on.
func TestSettleReach_ForeignBridgeGatewayIsAFatalIsolationFinding(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	stubGateway(t, "172.17.0.1\n", nil)
	asked := stubLocal(t, false, nil)
	mark := strictness.Checkpoint()
	route, err := settleReach(context.Background(), Docker{})
	require.ErrorIs(t, err, errBridgeNotLocal)
	assert.Equal(t, "172.17.0.1", *asked)
	assert.Equal(t, hostRoute{}, route, "nothing is handed on to listen on")
	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Equal(t, report.KindIsolation, found[0].Kind)
	assert.Contains(t, found[0].Text, "172.17.0.1")
	assert.Equal(t, foreignBridgeRemedy, found[0].Remedy)
}

// TestReachRoute_BridgeGatewayLocalityUnknownIsRefused: a host whose own
// addresses cannot be listed cannot vouch for the gateway, so it is refused
// rather than listened on blind.
func TestReachRoute_BridgeGatewayLocalityUnknownIsRefused(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	stubGateway(t, "172.17.0.1\n", nil)
	stubLocal(t, false, errors.New("netlink: permission denied"))
	_, err := Docker{}.reachRoute(context.Background())
	require.ErrorIs(t, err, ErrNoHostReach)
	assert.Contains(t, err.Error(), "netlink: permission denied")
}
