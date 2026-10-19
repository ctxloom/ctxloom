package toolchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollector_DedupesByCommandKeepingEverySource(t *testing.T) {
	var c Collector
	c.Add("serena", RequirementSource{KindMCP, "serena"})
	c.Add("cargo", RequirementSource{KindBundle, "rust-development"})
	c.Add("serena", RequirementSource{KindBundle, "code-nav"})
	c.Add("serena", RequirementSource{KindMCP, "serena"})

	assert.Equal(t, []Requirement{
		{Command: "cargo", Sources: []RequirementSource{{KindBundle, "rust-development"}}},
		{Command: "serena", Sources: []RequirementSource{{KindBundle, "code-nav"}, {KindMCP, "serena"}}},
	}, c.Requirements())
	assert.Equal(t, []string{"cargo", "serena"}, Commands(c.Requirements()))
}

func TestRequirementSource_String(t *testing.T) {
	assert.Equal(t, "bundle rust-development", RequirementSource{KindBundle, "rust-development"}.String())
}
