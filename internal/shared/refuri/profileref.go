package refuri

// ProfileSelector is the selector prefix addressing a profile shipped INSIDE a
// bundle ("<bundle>#profiles/<name>"). Profiles are a COMPOUND bundle item kind —
// a profile composes leaves (fragments/commands/mcp/hooks/llm/parents/variables) —
// so the selector is the profile counterpart to remote.FragmentSelector /
// remote.CommandSelector, keeping the bundle-item grammar in one place.
const ProfileSelector = "#profiles/"
