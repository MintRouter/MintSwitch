//go:build production

package secrets

// serviceName is the keychain service name in production builds
// (-tags production, set by the wails3 build/package tasks). It mirrors
// paths.dataDirName so prod and dev never share a keychain entry.
const serviceName = "mintswitch"
