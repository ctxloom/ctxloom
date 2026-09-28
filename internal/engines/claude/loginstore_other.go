//go:build !darwin

package claude

// loginStoreHomeRel is where claude keeps its credential storage under $HOME
// when SecureStorageEnv is "": the credentials file and its locks in
// ~/.claude.
const loginStoreHomeRel = ConfigDirName
