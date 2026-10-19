package wire

import (
	"fmt"
	"regexp"
)

// Requires is what a bundle's content needs to exist where its agent runs,
// declared by the bundle that assumes it (a rust bundle's fragments assume
// cargo). It is a struct rather than a bare list so it can grow other kinds of
// requirement without a second top-level key.
//
// Commands are bare command names resolved on the agent's PATH, never paths:
// a path would name one machine's layout and could not be checked in an image.
type Requires struct {
	Commands []string `yaml:"commands,omitempty" json:"commands,omitempty"`
}

// commandNamePattern is the one definition of a legal required-command name.
var commandNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Validate refuses a command name that is not a bare command name, naming the
// offender. The caller names the bundle.
func (r Requires) Validate() error {
	for _, c := range r.Commands {
		if !commandNamePattern.MatchString(c) {
			return fmt.Errorf("requires.commands: %q is not a command name (letters, digits, '.', '_', '+', '-'; no path separator)", c)
		}
	}
	return nil
}
