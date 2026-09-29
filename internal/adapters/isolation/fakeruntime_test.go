package isolation

import "context"

// fakeRuntime is a Runtime stub for the degrade-path tests: it reports a
// configurable availability and binary without touching a real daemon.
type fakeRuntime struct {
	name      string
	binary    string
	available bool
}

func (f fakeRuntime) Name() string             { return f.name }
func (f fakeRuntime) Binary() string           { return f.binary }
func (f fakeRuntime) Available() bool          { return f.available }
func (fakeRuntime) RunArgs(RunSpec) []string   { return nil }
func (fakeRuntime) RemoveArgs(string) []string { return nil }

// reachRoute is empty: a fake runner's env passes through un-re-minted.
func (fakeRuntime) reachRoute(context.Context) (hostRoute, error) { return hostRoute{}, nil }
func (fakeRuntime) gatewayInspectArgs() []string                  { return ociRuntime{}.gatewayInspectArgs() }
func (fakeRuntime) containerByIDArgs(id string) []string          { return ociRuntime{}.containerByIDArgs(id) }
func (fakeRuntime) selfInspectArgs(id string) []string            { return ociRuntime{}.selfInspectArgs(id) }

// The CLI grammar is the shared OCI default, so a call site routed through the
// seam renders the same argv against the fake as against a real runtime.
func (fakeRuntime) inspectRunningArgs(name string) []string {
	return ociRuntime{}.inspectRunningArgs(name)
}
func (fakeRuntime) imageInspectArgs(format string, images ...string) []string {
	return ociRuntime{}.imageInspectArgs(format, images...)
}
func (fakeRuntime) imageListArgs(filter string) []string { return ociRuntime{}.imageListArgs(filter) }
func (fakeRuntime) containerListAllArgs() []string       { return ociRuntime{}.containerListAllArgs() }
func (fakeRuntime) containerImageArgs(ids ...string) []string {
	return ociRuntime{}.containerImageArgs(ids...)
}
func (fakeRuntime) imageRemoveArgs(refs ...string) []string {
	return ociRuntime{}.imageRemoveArgs(refs...)
}
func (fakeRuntime) canonicalRef(ref string) string { return ociRuntime{}.canonicalRef(ref) }

// imageUniqueSizes answers through the docker grammar, so a scripted
// probeExec (image_prune_test.go) serves it like any other call.
func (f fakeRuntime) imageUniqueSizes(ctx context.Context) (map[string]int64, error) {
	return dockerUniqueSizes(ctx, f.binary)
}
func (fakeRuntime) buildArgs(image, file, contextDir string, flags buildFlags) []string {
	return ociRuntime{}.buildArgs(image, file, contextDir, flags)
}
func (fakeRuntime) daemonNameTemplate() string { return ociRuntime{}.daemonNameTemplate() }
func (fakeRuntime) removeOutcome(stdout []byte, err error) removeOutcome {
	return ociRuntime{}.removeOutcome(stdout, err)
}
func (fakeRuntime) passesPUID() bool { return ociRuntime{}.passesPUID() }

// Expose is the OCI identity bind mount, so tests that route delivery mounts
// through the runtime (sessionStateMounts, gitDirMounts) see the same mount
// the literal produced.
func (fakeRuntime) expose(host, target string, readOnly bool) mount {
	return mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeMapped mirrors ociRuntime's real behavior: it routes hostPath through
// f.mapper() rather than hardcoding Host==Container, so a call site that
// skips ExposeMapped/mapper() entirely produces output distinguishable from
// one that used it (see prefixMapper's doc, pathmapper_test.go).
func (f fakeRuntime) exposeMapped(hostPath string, readOnly bool) (mount, error) {
	return exposeThrough(f.mapper(), hostPath, readOnly)
}

// mapper is a non-identity prefixMapper — deliberately NOT identityMapper.
// Under identity, exposeMapped(p) == mount{p, p} whether or not a call site
// actually threads its path through the mapper, so a deleted mapper() call
// is byte-identical to a correct one and every container test that exercises
// this fake was structurally unable to prove the mapper seam is reachable.
// prefixMapper breaks that: it is injective (distinct host paths stay
// distinct after mapping), so a test comparing against the ACTUAL mapped
// value catches a call site that silently reverts to the raw host path.
func (fakeRuntime) mapper() pathMapper { return prefixMapper{prefix: "/ctr"} }

// Enumerate is a no-op default for the many tests that never exercise the
// container-reap sweep; container_reap_test.go defines its own fake that
// embeds fakeRuntime and overrides this.
func (fakeRuntime) Enumerate(context.Context, string) ([]ContainerInfo, error) { return nil, nil }
