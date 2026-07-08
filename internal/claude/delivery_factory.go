package claude

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// deliveryFactory builds claude's per-surface delivery strategies for a given
// Placement. It implements agent.DeliveryFactory: context is delivered via the
// append-flag strategy (appendFlagDelivery) into the placement it's handed
// (Setup targets the session's ephemeral dir), while MCP/skills/settings are
// delivered via the file-template strategy (fileTemplateDelivery) into the
// working dir. NewClaudeCode constructs one, passes it to InitLaunch so Setup
// drives it, and keeps the same instance so buildArgs can read the framed
// context file's path.
//
// The factory records the context strategy it builds so buildArgs (which runs
// after Setup) can point --append-system-prompt-file at the framed file via
// ContextPath(); the other strategies are stateless per call.
type deliveryFactory struct {
	fs      afero.Fs
	context *appendFlagDelivery
}

// newDeliveryFactory constructs claude's delivery factory. A nil fs defaults to
// the OS filesystem (agent.GetFS), matching the strategies it builds.
func newDeliveryFactory(fs afero.Fs) *deliveryFactory {
	return &deliveryFactory{fs: fs}
}

// ContextDelivery builds (and records) the append-flag context strategy writing
// into place. Recording it lets ContextPath expose the framed file to buildArgs.
func (f *deliveryFactory) ContextDelivery(place agent.Placement) agent.ContextDelivery {
	if place == nil {
		return nil
	}
	f.context = newAppendFlagDelivery(place, f.fs)
	return f.context
}

// MCPDelivery builds the file-template MCP strategy writing into place.
func (f *deliveryFactory) MCPDelivery(place agent.Placement) agent.MCPDelivery {
	if place == nil {
		return nil
	}
	return newFileTemplateDelivery(place, f.fs)
}

// SkillsDelivery builds the file-template skills strategy writing into place.
func (f *deliveryFactory) SkillsDelivery(place agent.Placement) agent.SkillsDelivery {
	if place == nil {
		return nil
	}
	return newFileTemplateDelivery(place, f.fs)
}

// SettingsDelivery builds the file-template settings strategy writing into place.
func (f *deliveryFactory) SettingsDelivery(place agent.Placement) agent.SettingsDelivery {
	if place == nil {
		return nil
	}
	return newFileTemplateDelivery(place, f.fs)
}

// ContextPath returns the framed context file the last-built context strategy
// wrote, or "" when none was built or nothing was delivered (empty context, or a
// delivery failure that fell back to the injection hook). buildArgs reads it to
// point --append-system-prompt-file at the framed file.
func (f *deliveryFactory) ContextPath() string {
	if f.context == nil {
		return ""
	}
	return f.context.Path()
}

var _ agent.DeliveryFactory = (*deliveryFactory)(nil)
