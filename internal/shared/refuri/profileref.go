package refuri

// ProfileSelector is the selector prefix addressing a profile shipped INSIDE a
// bundle ("<bundle>#profiles/<name>"). Profiles are an ungated, COMPOUND bundle
// item kind — a profile composes leaves (fragments/commands/mcp/hooks/llm/parents/
// variables) — so the selector is the profile counterpart to remote.FragmentSelector /
// remote.CommandSelector, keeping the bundle-item grammar in one place. Unlike those,
// there is no trust kind for profiles: a profile definition is orchestration/
// config, carrying no review state and never gated. Its constituent leaves still gate at
// their own chokes (fragments/commands at content assembly, mcp/hooks at the exec
// choke) — only the profile definition itself is ungated.
const ProfileSelector = "#profiles/"
