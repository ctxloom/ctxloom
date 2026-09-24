package cli

import "github.com/spf13/cobra"

// helpArgName is the one positional value some commands read as "the caller
// fumbled for help" rather than as a resource name. It is reached only through
// helpFallback: spread across call sites, the literal and its reading were
// connascence of MEANING, with nothing tying the copies together.
// TestArch_HelpArgName_ReachedOnlyThroughHelpFallback holds it here.
const helpArgName = "help"

// helpFallback renders cmd's help when name is the help shortcut, reporting
// whether it did. Callers consult it at the point where the named resource
// has turned out NOT to exist:
//
//	if shown, herr := helpFallback(cmd, name); shown {
//		return herr
//	}
//	return err // the not-found error the command was about to return
//
// IT IS A FALLBACK, NOT A GUARD. A bundle of help docs, an agent or a profile
// called "help" is a legal resource. Guarding on the literal before the
// lookup made every such resource UNADDRESSABLE and turned a genuine request
// into "print help, exit 0": `bundle create help` reported success and created
// nothing. So a command reaches this only where it was going to fail anyway —
// and a create/set command not at all, since naming the thing to create is
// unambiguous. Cobra's own --help and `ctxloom help <path>` are the
// unambiguous ways to ask, and are unaffected.
func helpFallback(cmd *cobra.Command, name string) (bool, error) {
	if name != helpArgName {
		return false, nil
	}
	return true, cmd.Help()
}
