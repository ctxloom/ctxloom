package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// cloudFileFixture writes a fixture credential file under the fake home and
// returns its host path.
func cloudFileFixture(t *testing.T, home, rel string) string {
	t.Helper()
	p := filepath.Join(home, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte("[default]\n"), 0o600))
	return p
}

// CONTAINER + CLOUD: each credential FILE a provider var names is bound
// read-only where the runtime's seam routes it, and the var is rewritten to
// that in-container path. fakeRuntime's seam prefixes /ctr, so a var left at
// its host path, or a file bound at its raw host path, fails here.
func TestCredentials_ContainerCloudMountsEachCredentialFileReadOnly(t *testing.T) {
	home := fakeHostHome(t, "")
	awsConfig := cloudFileFixture(t, home, "elsewhere/aws-config")
	gcp := cloudFileFixture(t, home, "keys/adc.json")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("AWS_CONFIG_FILE", awsConfig)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", gcp)
	creds := claudeCredentials(t, engine.AuthCloud)

	pl, mounts := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), containerOf)
	for _, f := range []struct{ v, host string }{{"AWS_CONFIG_FILE", awsConfig}, {"GOOGLE_APPLICATION_CREDENTIALS", gcp}} {
		assert.Contains(t, mounts, mount{Host: f.host, Container: "/ctr" + f.host, ReadOnly: true}, "%s: a single-file, read-only bind at the seam's target", f.v)
		assert.Equal(t, "/ctr"+f.host, pl.Env[f.v], "%s: pointed at the mount, not the host path", f.v)
	}
}

// On the HOST nothing changes: the var keeps the human's path and nothing is
// mounted for it.
func TestCredentials_HostCloudKeepsTheCredentialFileVar(t *testing.T) {
	home := fakeHostHome(t, "")
	awsConfig := cloudFileFixture(t, home, "elsewhere/aws-config")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("AWS_CONFIG_FILE", awsConfig)

	pl, mounts := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, claudeCredentials(t, engine.AuthCloud)), t.TempDir(), hostRelocator{})
	assert.Equal(t, awsConfig, pl.Env["AWS_CONFIG_FILE"])
	assert.Empty(t, mounts)
}

// Two vars naming one file get one bind (a runtime refuses a duplicate
// mount point) and both are rewritten.
func TestCredentials_ContainerBindsAFileNamedTwiceOnce(t *testing.T) {
	home := fakeHostHome(t, "")
	f := cloudFileFixture(t, home, ".aws/credentials")
	creds := engine.Credentials{
		Env:      map[string]string{"AWS_CONFIG_FILE": f, "AWS_SHARED_CREDENTIALS_FILE": f},
		FileVars: []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"},
	}
	pl, mounts := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), containerOf)
	n := 0
	for _, m := range mounts {
		if m.Host == f {
			n++
		}
	}
	assert.Equal(t, 1, n)
	assert.Equal(t, "/ctr"+f, pl.Env["AWS_CONFIG_FILE"])
	assert.Equal(t, "/ctr"+f, pl.Env["AWS_SHARED_CREDENTIALS_FILE"])
}

// A var naming no file a container can bind — missing, a directory, or a
// relative path — refuses the run, typed, with a remedy naming the var. The
// relative one EXISTS from the cwd, so it is refused for being relative.
func TestCredentials_ContainerRefusesACredentialFileItCannotBind(t *testing.T) {
	home := fakeHostHome(t, "")
	cloudFileFixture(t, home, "adc.json")
	t.Chdir(home)
	for name, p := range map[string]string{
		"missing":   filepath.Join(home, "absent.json"),
		"directory": home,
		"relative":  "adc.json",
	} {
		t.Run(name, func(t *testing.T) {
			creds := engine.Credentials{Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": p}, FileVars: []string{"GOOGLE_APPLICATION_CREDENTIALS"}}
			_, mounts, err := relocateOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), containerOf)
			require.ErrorIs(t, err, engine.ErrNoCredential)
			assert.Nil(t, mounts)
			fix, ok := clifmt.RemedyOf(err)
			require.True(t, ok)
			assert.Contains(t, fix, "GOOGLE_APPLICATION_CREDENTIALS")
		})
	}
}

// A credential file the runtime cannot route is refused as
// present.ErrUnreachableRoot, as every other root is — never bound at a
// guessed path.
func TestCredentials_ContainerRefusesAnUnroutableCredentialFile(t *testing.T) {
	home := fakeHostHome(t, "")
	f := cloudFileFixture(t, home, "keys/adc.json")
	creds := engine.Credentials{Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": f}, FileVars: []string{"GOOGLE_APPLICATION_CREDENTIALS"}}
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, m: unroutableMapper{under: filepath.Join(home, "keys")}}
	r := containerRelocator{rt: rt, instanceHome: defaultContainerInstanceHome, home: defaultContainerHome}

	_, mounts, err := relocateOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), r)
	require.ErrorIs(t, err, present.ErrUnreachableRoot)
	require.ErrorIs(t, err, errNoRoute)
	assert.Nil(t, mounts)

	// A Windows share path is refused by the drive-letter mapper by name.
	_, _, err = containerRelocator{rt: windowsDocker, home: defaultContainerHome}.relocateFiles(engine.Credentials{
		Env:      map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": `\\wsl.localhost\Ubuntu\home\u\adc.json`},
		FileVars: []string{"GOOGLE_APPLICATION_CREDENTIALS"},
	})
	require.ErrorIs(t, err, present.ErrUnreachableRoot)
	require.ErrorIs(t, err, errUNCPath)
}
