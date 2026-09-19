package isolation

import (
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// EngineFacts are the facts about one engine the cells adapter reads to
// prepare a cell: how its home relocates and what credential material seeds
// it, what delivery its material accepts, how a container of it is built and
// authenticated, how its own top-level config is written into a provisioned
// home, and whether it ships by default. They are the paired hosting
// record's declarations, handed in through ONE port; when Engine.Home() and
// Engine.Container() are the engine's own declarations, that port reads
// them off the engine and nothing here changes.
type EngineFacts struct {
	Home           agent.Declared[agent.EngineHome]
	Provisioning   agent.Declared[agent.ProvisioningPolicy]
	Container      agent.Declared[agent.EngineContainer]
	InstanceConfig agent.Declared[func(agent.SettingsOptions) agent.InstanceConfigWriter]
	Distribution   engine.Distribution
}

// Facts is the one accessor the cells adapter reads engine facts through:
// the facts for a named engine, and the names it can answer for. The
// composition root installs it once (UseFacts); nothing here registers an
// engine.
type Facts interface {
	For(name string) (EngineFacts, bool)
	Names() []string
}

var (
	factsMu sync.RWMutex
	facts   Facts
)

// UseFacts installs the accessor. Called once by the composition that pairs
// engine kinds with their hosting records; a test installs an overlay and
// restores what it replaced.
func UseFacts(f Facts) (restore func()) {
	factsMu.Lock()
	defer factsMu.Unlock()
	prev := facts
	facts = f
	return func() {
		factsMu.Lock()
		defer factsMu.Unlock()
		facts = prev
	}
}

// factsFor reads one engine's facts; ok is false for an engine the
// accessor does not know, or when none is installed.
func factsFor(name string) (EngineFacts, bool) {
	factsMu.RLock()
	f := facts
	factsMu.RUnlock()
	if f == nil {
		return EngineFacts{}, false
	}
	return f.For(name)
}

// factNames lists, sorted, every engine the accessor answers for.
func factNames() []string {
	factsMu.RLock()
	f := facts
	factsMu.RUnlock()
	if f == nil {
		return nil
	}
	names := f.Names()
	sort.Strings(names)
	return names
}

// credentialSeedDeclared is the engine's credential seed: what its declared
// home says seeds it, or the home's own absence reason.
func credentialSeedDeclared(name string) (agent.Declared[agent.CredentialSeed], bool) {
	f, ok := factsFor(name)
	if !ok {
		return agent.Declared[agent.CredentialSeed]{}, false
	}
	home, ok := f.Home.Get()
	if !ok {
		return agent.Absent[agent.CredentialSeed](f.Home.AbsentReason()), true
	}
	return home.Credentials, true
}

func credentialSeedFor(name string) (agent.CredentialSeed, bool) {
	d, ok := credentialSeedDeclared(name)
	if !ok {
		return agent.CredentialSeed{}, false
	}
	return d.Get()
}

func provisioningPolicyDeclared(name string) (agent.Declared[agent.ProvisioningPolicy], bool) {
	f, ok := factsFor(name)
	if !ok {
		return agent.Declared[agent.ProvisioningPolicy]{}, false
	}
	return f.Provisioning, true
}

// engineContainerRegistration is one engine's container story with the
// shipping policy that decides whether it composes into the default image.
type engineContainerRegistration struct {
	container    agent.Declared[agent.EngineContainer]
	distribution engine.Distribution
}

func engineContainerDeclared(name string) (engineContainerRegistration, bool) {
	f, ok := factsFor(name)
	if !ok || !f.Container.Decided() {
		return engineContainerRegistration{}, false
	}
	return engineContainerRegistration{container: f.Container, distribution: f.Distribution}, true
}

// registeredEngineContainers is every engine with a decided container story.
func registeredEngineContainers() map[string]engineContainerRegistration {
	out := map[string]engineContainerRegistration{}
	for _, name := range factNames() {
		if r, ok := engineContainerDeclared(name); ok {
			out[name] = r
		}
	}
	return out
}

// instanceConfigWriterFor is the engine's own writer of its top-level config
// into a provisioned home; nil when it declares none.
func instanceConfigWriterFor(name string) agent.InstanceConfigWriter {
	f, ok := factsFor(name)
	if !ok {
		return nil
	}
	w, ok := f.InstanceConfig.Get()
	if !ok {
		return nil
	}
	return w(agent.SettingsOptions{})
}
