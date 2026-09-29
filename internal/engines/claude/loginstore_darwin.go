package claude

// loginStoreHomeRel is "" on macOS: claude keeps its login in the Keychain
// (https://code.claude.com/docs/en/authentication, "Credential management"),
// which is no directory under $HOME, so the store can be shared in place and
// never presented inside a container.
const loginStoreHomeRel = ""
