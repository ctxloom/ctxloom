package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// selfContainer is THIS process's container as the driving daemon reports it:
// ctxloom running inside a container that drives its daemon through a mounted
// socket (docker-outside-of-docker — a devcontainer, a CI job container).
// Resolved once per runtime value, at selection (resolveSelf), so the route
// home, every run's mounts and the shared-fs probe read the same answer.
type selfContainer struct {
	id      string
	network selfNetwork // zero: none a sibling can join
	mounts  []selfMount
	hostNet bool // NetworkMode "host": docker0 is local, no self route
}

// selfNetwork is the network a sibling container joins to reach this process,
// and this process's address on it.
type selfNetwork struct{ name, ip string }

// selfMount is one of self's mounts: source is the daemon's path, destination
// is ours.
type selfMount struct{ source, destination string }

// selfIDCandidates proposes the ids this process's container may carry; a
// package var so tests decide it without the real /proc.
var selfIDCandidates = containerprobe.SelfIDCandidates

// findSelf resolves this process's container on rt's daemon. ok=false: not
// one of its containers (or not containerized). err: the daemon could not
// answer. Package var: unit tests script it.
//
// The trigger is behavioural: a candidate is us only when THIS daemon lists
// it. A candidate another daemon owns (a docker-in-docker sidecar reached over
// TCP) is simply absent here, so a run falls through to the host routes —
// correct, since that daemon's containers share no network with ours.
var findSelf = func(ctx context.Context, rt Runtime) (selfContainer, bool, error) {
	for _, cand := range selfIDCandidates() {
		out, err := probeExec(ctx, rt.Binary(), rt.containerByIDArgs(cand))
		if err != nil {
			return selfContainer{}, false, fmt.Errorf("%s: listing container %s: %w", rt.Name(), cand, err)
		}
		// The id filter matches by prefix: a short candidate matching several
		// containers names none of them.
		ids := strings.Fields(out)
		if len(ids) != 1 {
			continue
		}
		out, err = probeExec(ctx, rt.Binary(), rt.selfInspectArgs(ids[0]))
		if err != nil {
			return selfContainer{}, false, fmt.Errorf("%s: inspecting this process's container %s: %w", rt.Name(), ids[0], err)
		}
		s, err := decodeSelf(out)
		if err != nil {
			return selfContainer{}, false, fmt.Errorf("%s: this process's container %s: %w", rt.Name(), ids[0], err)
		}
		return s, true, nil
	}
	return selfContainer{}, false, nil
}

// selfInspectTemplate renders only the fields findSelf reads, as one JSON
// object, in the docker-compatible inspect shape podman shares.
const selfInspectTemplate = `{"Id":{{json .Id}},"NetworkMode":{{json .HostConfig.NetworkMode}},"Networks":{{json .NetworkSettings.Networks}},"Mounts":{{json .Mounts}}}`

// selfInspect is selfInspectTemplate's shape.
type selfInspect struct {
	ID          string `json:"Id"`
	NetworkMode string
	Networks    map[string]struct {
		IPAddress         string
		GlobalIPv6Address string
	}
	Mounts []struct {
		Type        string
		Source      string
		Destination string
	}
}

// decodeSelf reads one selfInspectTemplate render. A tmpfs (or any mount
// without a daemon-side source) is dropped: it names nothing a bind can.
func decodeSelf(raw string) (selfContainer, error) {
	var in selfInspect
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &in); err != nil {
		return selfContainer{}, fmt.Errorf("decoding its inspect: %w", err)
	}
	s := selfContainer{id: in.ID, hostNet: in.NetworkMode == "host"}
	for _, m := range in.Mounts {
		if (m.Type == "bind" || m.Type == "volume") && m.Source != "" {
			s.mounts = append(s.mounts, selfMount{source: m.Source, destination: m.Destination})
		}
	}
	if !s.hostNet {
		s.network = pickSelfNetwork(in)
	}
	return s, nil
}

// pickSelfNetwork chooses the network a sibling joins: the only one with an
// address; among several, the one this process's default route leaves by;
// else the first by name — deterministic, and named in Description.Reach.
// IPv4 is preferred over the global IPv6 address.
func pickSelfNetwork(in selfInspect) selfNetwork {
	var nets []selfNetwork
	for name, n := range in.Networks {
		ip := n.IPAddress
		if ip == "" {
			ip = n.GlobalIPv6Address
		}
		if ip != "" {
			nets = append(nets, selfNetwork{name: name, ip: ip})
		}
	}
	if len(nets) == 0 {
		return selfNetwork{}
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].name < nets[j].name })
	primary := primaryOutboundIP()
	for _, n := range nets {
		if n.ip == primary {
			return n
		}
	}
	return nets[0]
}

// resolveSelf is the constructors' one call into findSelf, with the error
// policy dockerIsRootless also takes: an undecidable answer routes a finding
// and proceeds as not-self, so today's routes decide — and on a foreign
// bridge they still refuse (foreignBridgeRemedy), never a silent wrong route.
func resolveSelf(rt Runtime) *selfContainer {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, ok, err := findSelf(ctx, rt)
	if err != nil {
		strictness.Fail(report.KindIsolation,
			"check that the "+rt.Name()+" CLI can list and inspect containers on its daemon, or pass --degraded to proceed as if ctxloom were not in one of its containers",
			"cannot tell whether this process runs in one of %s's containers (%v); proceeding as if it does not", rt.Name(), err)
		return nil
	}
	if !ok {
		return nil
	}
	return &s
}

// errNoSelfNetwork refuses a self whose container has no network a sibling
// container can join (none, pasta, slirp4netns, container:<x>).
var errNoSelfNetwork = errors.New("isolation: this process's container has no network a sibling container can join")

// noSelfNetworkRemedy names the fix for errNoSelfNetwork.
const noSelfNetworkRemedy = "attach ctxloom's own container to a network its agents can join (e.g. `docker network create ctxloom` then `docker network connect ctxloom <ctxloom's container>`), or run ctxloom on the daemon's host"

// selfNetworkRoute is the route home when this process is one of the daemon's
// containers: a runner joins self's network and dials self's address there,
// which is one of this process's own addresses, so the coordinator can listen
// on it. The same exposure class as the rootful bridge route: every container
// on that network can reach the (token-protected) listener. ok=false for a
// container on the host's network, where the host routes already hold.
func selfNetworkRoute(s selfContainer) (hostRoute, bool, error) {
	switch {
	case s.hostNet:
		return hostRoute{}, false, nil
	case s.network.ip == "":
		return hostRoute{}, false, fmt.Errorf("%w (container %s)", errNoSelfNetwork, s.id)
	}
	return hostRoute{dial: s.network.ip, listen: present.Listen{Addr: s.network.ip}, network: s.network.name}, true, nil
}

// selfFirst is every OCI runtime's route home: when the daemon has confirmed
// this process is one of its containers, the runner joins that container's
// network (selfNetworkRoute), ahead of every host route — none of which this
// process's namespace holds. A self with no joinable network shares its
// namespace where shareNamespace allows it, and is refused otherwise; a self
// on the host network, and a process that is no container of the daemon's,
// take host().
func selfFirst(self *selfContainer, shareNamespace bool, host func() (hostRoute, error)) (hostRoute, error) {
	if self == nil {
		return host()
	}
	r, ok, err := selfNetworkRoute(*self)
	switch {
	case errors.Is(err, errNoSelfNetwork) && shareNamespace:
		return sharedNamespaceRoute(*self), nil
	case err != nil || ok:
		return r, err
	}
	return host()
}

// sharedNamespacePrefix is the --network value that runs a container in
// another container's network namespace.
const sharedNamespacePrefix = "container:"

// sharedNamespaceRoute runs the runner INSIDE self's network namespace, where
// the coordinator's loopback listener is the runner's own loopback. Taken only
// where the owner ruled it (rootless podman whose container sits on
// pasta/slirp4netns, with no network to join), and warned, because it is a
// real isolation loss.
func sharedNamespaceRoute(s selfContainer) hostRoute {
	clidiag.WarnRemedyOnce("ctxloom",
		noSelfNetworkRemedy,
		"container agents will share ctxloom's container network namespace (%s): it has no network a sibling can join, so agents share localhost with each other and with every service listening on ctxloom's loopback", s.id)
	return hostRoute{network: sharedNamespacePrefix + s.id}
}
