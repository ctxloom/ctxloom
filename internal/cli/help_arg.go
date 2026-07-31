package cli

import "github.com/spf13/cobra"

// helpArgName is the name-position help shortcut: `ctxloom profile show help`
// renders the command's help rather than looking for an item called "help".
//
// Every `<verb> <name>` command in this package honours it, so the literal
// lives here once. Spread across eleven call sites it was connascence of
// MEANING — nothing tied the copies together, and changing what the shortcut
// is would have meant finding all of them.
const helpArgName = "help"

// helpShortcut renders cmd's help when name is the help shortcut, reporting
// whether it did. Callers guard their name argument with it:
//
//	if shown, err := helpShortcut(cmd, name); shown {
//		return err
//	}
//
// It must be checked BEFORE any config load: the shortcut's whole value is
// working in a directory that has no ctxloom config to load.
func helpShortcut(cmd *cobra.Command, name string) (bool, error) {
	if name != helpArgName {
		return false, nil
	}
	return true, cmd.Help()
}
