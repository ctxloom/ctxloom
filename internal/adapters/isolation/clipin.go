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

// withHostCLIPin resolves the version p's engine CLI is pinned to into
// p.cliPin: the host's, or — when the host's is absent, unreadable or not
// taggable — the engine's tested version (announced; see pinTestedVersion).
// Only an engine that declares a pinned install is asked about. "" (unpinned)
// is left only for an engine that pins nothing, or a process with no
// composition root to install a probe (a unit test).
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
		p.cliPin = pinTestedVersion(engine, p.testedVersion, err)
		return p
	}
	p.cliPin = v
	return p
}

// pinTestedVersion is the pin for a host whose engine CLI version could not
// be read: the version ctxloom is tested against, never latest and never a
// refusal (ruled 2026-10-16). It is announced, because the image's CLI then
// differs from whatever the host runs, and a host install or upgrade moves
// the image to that version instead.
func pinTestedVersion(engine, tested string, err error) string {
	clidiag.WarnOnce("ctxloom", "the %s agent image pins ctxloom's tested %s %s, not this host's %s CLI, because the host's version could not be read (%v)",
		engine, engine, tested, engine, err)
	return tested
}

// pinTagSegment is the cliPin's segment of an image tag ("" when unpinned).
func pinTagSegment(cliPin string) string {
	if cliPin == "" {
		return ""
	}
	return "cli" + cliPin
}
