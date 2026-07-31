package cli

import "github.com/spf13/cobra"

// helpArgName is the one positional value some commands read as "the caller
// fumbled for help" rather than as a resource name. The literal lives here
// once: spread across eleven call sites it was connascence of MEANING, with
// nothing tying the copies together.
//
// IT IS A FALLBACK, NOT A GUARD. A bundle of help docs or an agent called
// "help" is a legal resource, so a command consults this only after the named
// resource turns out not to exist — or, for the create/set commands, not at
// all, since naming the thing to create is unambiguous. Guarding on the
// literal made every such resource UNADDRESSABLE and turned a genuine request
// into "print help, exit 0": `bundle create help` reported success and created
// nothing. Cobra's own --help and `ctxloom help <path>` are the unambiguous
// ways to ask, and are unaffected.
//
// THE PACKAGE IS MID-MIGRATION AND THE TWO READINGS COEXIST. `agent`,
// `bundle edit` and `bundle list` consult it as the fallback described above.
// `profile.go` still calls helpShortcut below, which is the older pre-config
// GUARD — so a profile literally named "help" remains unaddressable. Fixing
// those four sites is tracked; do not "unify" them by reverting the fallback.
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
