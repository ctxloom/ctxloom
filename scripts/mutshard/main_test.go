package main

import (
	"reflect"
	"testing"
)

// gremlins --diff needs git's diff.context at 0; a caller's own
// GIT_CONFIG_COUNT entries are kept and the setting goes at the next index.
func TestZeroDiffContext(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{"none set", []string{"HOME=/h"},
			[]string{"HOME=/h", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=diff.context", "GIT_CONFIG_VALUE_0=0"}},
		{"caller set two", []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=y"},
			[]string{"GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=y",
				"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_2=diff.context", "GIT_CONFIG_VALUE_2=0"}},
		{"unparseable count", []string{"GIT_CONFIG_COUNT=junk"},
			[]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=diff.context", "GIT_CONFIG_VALUE_0=0"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := zeroDiffContext(c.env); !reflect.DeepEqual(got, c.want) {
				t.Errorf("zeroDiffContext(%q)\n got %q\nwant %q", c.env, got, c.want)
			}
		})
	}
}
