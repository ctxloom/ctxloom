package isolation

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Docker (opts.MountOpt.Set) and podman (specgenutilexternal.FindMountType)
// both split a --mount value with encoding/csv, so a field holding a comma or
// a quote must be CSV-quoted or it splits into fields that are not there.
func TestMountArgs_Golden(t *testing.T) {
	cases := []struct {
		m    mount
		want string
	}{
		{mount{Host: `C:\Users\ben\my proj`, Container: "/mnt/c/Users/ben/my proj"},
			`type=bind,source=C:\Users\ben\my proj,target=/mnt/c/Users/ben/my proj`},
		{mount{Host: "/a,b", Container: "/a,b", ReadOnly: true},
			`type=bind,"source=/a,b","target=/a,b",readonly`},
		{mount{Host: `/q"x`, Container: `/q"x`},
			`type=bind,"source=/q""x","target=/q""x"`},
	}
	for _, tc := range cases {
		got, err := mountArgs([]mount{tc.m}, pathSeam{})
		require.NoError(t, err)
		assert.Equal(t, []string{"--mount", tc.want}, got)
	}
}

// Whatever the path, the runtime's CSV parse gives back exactly the fields
// meant: source and target intact, nothing extra.
func TestMountArgs_RoundTripsThroughTheRuntimesCSVParse(t *testing.T) {
	for _, p := range []string{"/plain", "/a b", "/a,b", `/q"x`, `/"lead`, "/trail,", `C:\x,y "z"`} {
		args, err := mountArgs([]mount{{Host: p, Container: p, ReadOnly: true}}, pathSeam{})
		require.NoError(t, err)
		fields, err := csv.NewReader(strings.NewReader(args[1])).Read()
		require.NoError(t, err, args[1])
		assert.Equal(t, []string{"type=bind", "source=" + p, "target=" + p, "readonly"}, fields)
	}
}
