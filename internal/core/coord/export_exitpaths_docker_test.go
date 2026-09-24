//go:build docker_integration

package coord

// CrashCoordinator is crashCoordinator: the coordinator PROCESS dying, as the
// external docker suite's exit-path tests need it — listeners gone, no
// terminal synthesized, and no child torn down.
var CrashCoordinator = crashCoordinator
