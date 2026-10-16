package isolation

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// engineCLIVersionLabel names the engine CLI version a pinned agent image
// installed. The version is ALSO in the tag and the provenance (which is what
// rebuilds the image on a host upgrade); the label is what lets a reader say
// WHICH version an image carries.
const engineCLIVersionLabel = "ctxloom.engine-cli-version"

// HostCLIVersionProbe reports the version of the named engine's CLI
// installed on the HOST. Its errors (engineversion's typed refusals in
// production) are reported as they read: each already names its cause.
type HostCLIVersionProbe func(ctx context.Context, engine string) (string, error)

var (
	hostCLIProbeMu sync.RWMutex
	hostCLIProbe   HostCLIVersionProbe
)

// UseHostCLIVersionProbe installs the host engine-CLI probe a pinned agent
// image follows. The composition root installs the one probe ctxloom has
// (the App's cached engine-version prober — isolation cannot import
// operations); a test installs its own and restores what it replaced.
func UseHostCLIVersionProbe(p HostCLIVersionProbe) (restore func()) {
	hostCLIProbeMu.Lock()
	defer hostCLIProbeMu.Unlock()
	prev := hostCLIProbe
	hostCLIProbe = p
	return func() {
		hostCLIProbeMu.Lock()
		defer hostCLIProbeMu.Unlock()
		hostCLIProbe = prev
	}
}

// pinnableVersion bounds a probed version before it is rendered into a
// Containerfile RUN line and an image tag: a docker tag's alphabet (so no
// "+build" metadata), and nothing a shell would interpret.
var pinnableVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

// withHostCLIPin resolves the version p's engine CLI is pinned to — the
// host's — into p.cliPin. Only an engine that declares a pinned install is
// asked about. "" leaves the image unpinned: the engine pins nothing, no
// composition root installed a probe, or the host's version cannot be read
// (announced — see unpinnedHostCLI).
func withHostCLIPin(p engineContainerSpec, engine string) engineContainerSpec {
	if p.pinnedInstall == nil {
		return p
	}
	hostCLIProbeMu.RLock()
	probe := hostCLIProbe
	hostCLIProbeMu.RUnlock()
	if probe == nil {
		return p
	}
	// The probe carries its own budget (engineversion.DefaultProbeTimeout)
	// and caches by binary fingerprint, so this is one exec per process.
	v, err := probe(context.Background(), engine)
	if err == nil && !pinnableVersion.MatchString(v) {
		err = fmt.Errorf("its version %q cannot be written into an image tag", v)
	}
	if err != nil {
		unpinnedHostCLI(engine, err)
		return p
	}
	p.cliPin = v
	return p
}

// unpinnedHostCLI announces an image whose engine CLI is NOT pinned to the
// host's, because the host's version could not be read: the image installs
// the latest release, as it did before pinning, and a later host upgrade will
// not rebuild it.
//
// OPEN RULING: whether such a host should instead be REFUSED a container run
// of this engine is the human's to decide; this keeps the pre-pin behaviour
// until they do.
func unpinnedHostCLI(engine string, err error) {
	clidiag.WarnOnce("ctxloom", "the %s agent image is not pinned to this host's %s CLI, because its version could not be read (%v); it installs the latest release, and will not be rebuilt when the host's CLI changes",
		engine, engine, err)
}

// pinTagSegment is the cliPin's segment of an image tag ("" when unpinned).
func pinTagSegment(cliPin string) string {
	if cliPin == "" {
		return ""
	}
	return "cli" + cliPin
}
