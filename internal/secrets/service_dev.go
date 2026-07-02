//go:build !production

package secrets

// serviceName is the keychain service name in dev builds (`wails3 dev`,
// plain `go build`/`go test`). It mirrors paths.dataDirName so dev runs
// never touch the production keychain entry.
const serviceName = "mintswitch-dev"
