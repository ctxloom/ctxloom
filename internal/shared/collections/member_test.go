package collections

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type shade string

const (
	shadeLight shade = "light"
	shadeDark  shade = "dark"
)

func TestMember_ResolvesBySpelling(t *testing.T) {
	members := []shade{shadeLight, shadeDark}

	got, ok := Member(members, "dark")
	assert.True(t, ok)
	assert.Equal(t, shadeDark, got)

	got, ok = Member(members, "dusk")
	assert.False(t, ok, "a spelling outside the vocabulary is refused, never coined")
	assert.Equal(t, shade(""), got)

	_, ok = Member(members, "Dark")
	assert.False(t, ok, "membership is exact: the vocabulary owns the spelling")
}
